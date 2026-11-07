// Package adbwrap is a thin wrapper around the platform-tools adb binary.
//
// The tool does not embed adb (the binaries are large and platform specific);
// it uses a copy on PATH or at the standard Android SDK location.
package adbwrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrUnavailable means no adb binary was found.
var ErrUnavailable = errors.New("adb not found: install Android Platform Tools or put adb on PATH")

// ADB runs commands through one adb binary.
type ADB struct {
	bin     string
	verbose bool
}

// New finds adb and returns a wrapper.
func New(verbose bool) (*ADB, error) {
	for _, p := range candidates() {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return &ADB{bin: p, verbose: verbose}, nil
		}
	}
	if p, err := exec.LookPath("adb"); err == nil {
		return &ADB{bin: p, verbose: verbose}, nil
	}
	return nil, ErrUnavailable
}

// Bin returns the adb path in use.
func (a *ADB) Bin() string { return a.bin }

func candidates() []string {
	var out []string
	if p := os.Getenv("ADB"); p != "" {
		out = append(out, p)
	}
	home, _ := os.UserHomeDir()
	return append(out, platformCandidates(home)...)
}

// redact keeps verbose logs readable without dumping script bodies, which can
// contain a password hash.
func redact(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if len(a) > 60 {
			out[i] = fmt.Sprintf("<%d bytes>", len(a))
			continue
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

// run executes adb with args.
func (a *ADB) run(ctx context.Context, args ...string) (string, error) {
	if a.verbose {
		fmt.Printf("    $ adb %s\n", redact(args))
	}
	cmd := exec.CommandContext(ctx, a.bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return out.String(), fmt.Errorf("adb %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return out.String(), nil
}

// State returns the adb transport state ("device", "recovery", ...).
func (a *ADB) State(ctx context.Context) string {
	out, err := a.run(ctx, "get-state")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// WaitForDevice waits until a transport reports "device" or "recovery".
func (a *ADB) WaitForDevice(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st := a.State(ctx); st == "device" || st == "recovery" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("no adb device after %s", timeout)
}

// Shell runs one command and returns stdout.
func (a *ADB) Shell(ctx context.Context, cmd string) (string, error) {
	return a.run(ctx, "shell", cmd)
}

// Root restarts adbd as root when needed.
func (a *ADB) Root(ctx context.Context) error {
	out, err := a.run(ctx, "root")
	if err != nil {
		// "adbd is already running as root" is not an error for us.
		if strings.Contains(strings.ToLower(out), "already") {
			return nil
		}
		return err
	}
	time.Sleep(2 * time.Second)
	_ = a.runWait(ctx, "wait-for-device")
	return nil
}

func (a *ADB) runWait(ctx context.Context, sub string) error {
	_, err := a.run(ctx, sub)
	return err
}

// Push copies a file to the device.
func (a *ADB) Push(ctx context.Context, local, remote string) error {
	_, err := a.run(ctx, "push", local, remote)
	return err
}

// Model identifies the attached device as EE71 or EE72. The kernel line is
// authoritative: EE71 runs 3.10.49, EE72 runs 5.4.x. Distinctive paths are used
// as a fallback, and /jrd-resource is deliberately not one of them — both
// models have it.
func (a *ADB) Model(ctx context.Context) string {
	const probe = `if [ -d /systemrw ] && [ -d /data/jrd/usr ]; then echo EE72; ` +
		`elif [ -e /usr/oem/core_app ] || [ -e /etc/mobileap_cfg.xml ]; then echo EE71; ` +
		`else case "$(uname -r)" in 3.10*) echo EE71 ;; 5.*) echo EE72 ;; esac; fi`
	out, err := a.Shell(ctx, probe)
	if err != nil {
		return ""
	}
	switch strings.TrimSpace(out) {
	case "EE71", "EE72":
		return strings.TrimSpace(out)
	}
	return ""
}

// ShellScript runs a multi-line shell script through the device's sh.
func (a *ADB) ShellScript(ctx context.Context, script string) (string, error) {
	if a.verbose {
		fmt.Printf("    $ adb shell sh -s <%d bytes>\n", len(script))
	}
	cmd := exec.CommandContext(ctx, a.bin, "shell", "sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("adb shell script: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}
