// Command ee-setup prepares an Alcatel EE71 or EE72 router for customization:
// working ADB, a dropbear SSH server and a known root password.
//
// Run it with no arguments. It finds the router, asks for the admin password,
// applies the changes with a backup first, and prints every step.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/rctphone/ee-setup/internal/adbwrap"
	"github.com/rctphone/ee-setup/internal/channel"
	"github.com/rctphone/ee-setup/internal/discover"
	"github.com/rctphone/ee-setup/internal/ee71"
	"github.com/rctphone/ee-setup/internal/ee72"
	"github.com/rctphone/ee-setup/internal/tclapi"
	"github.com/rctphone/ee-setup/internal/usbswitch"
)

// version is injected at build time: -X main.version=v0.1.0
var version = "dev"

type options struct {
	host         string
	password     string
	rootPassword string
}

func main() {
	var o options
	flag.StringVar(&o.host, "host", "", "router address (default: auto-detect)")
	flag.StringVar(&o.password, "password", "", "admin password (default: ask)")
	flag.StringVar(&o.rootPassword, "root-password", "oemlinux1", "root/SSH password")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, o); err != nil {
		fmt.Printf("\n[ERR] %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o options) error {
	fmt.Printf("=== ee-setup %s: ADB + SSH + root for Alcatel EE71/EE72 ===\n", version)

	step("Looking for the router...")
	d, err := discover.Find(ctx, o.host, o.password)
	if err != nil {
		step("No web interface found; checking USB/ADB")
		return runOverADB(ctx, o)
	}
	ok("found %s at %s", d.Model, d.Host)
	if v := d.Version(); v != "" {
		detail("firmware %s", v)
	}

	if d.Model == discover.ModelEE72 {
		return runEE72(ctx, o, d)
	}
	return runEE71(ctx, o, nil)
}

// runOverADB is the path when the web interface is not reachable — typical when
// the router is in debug USB mode or in mass-storage mode. The model is
// identified from the device itself, never guessed from a path that both
// models share.
func runOverADB(ctx context.Context, o options) error {
	switch usbswitch.ModelFromUSB() {
	case "EE72":
		return errEE72NeedsWeb()
	case "EE71":
		return runEE71(ctx, o, nil)
	}

	a, err := adbwrap.New(true)
	if err != nil {
		return fmt.Errorf("no router found over the network and no adb available: %w", err)
	}
	switch a.Model(ctx) {
	case "EE72":
		return errEE72NeedsWeb()
	case "EE71":
		return runEE71(ctx, o, a)
	default:
		// No ADB transport yet: try the EE71 mass-storage switch.
		return runEE71(ctx, o, a)
	}
}

// ---------------------------------------------------------------- EE72

func runEE72(ctx context.Context, o options, d *discover.Device) error {
	step("Logging in to the web interface")
	c := tclapi.New(d.Host, o.password)
	c.VHost = d.VHost
	c.HTTP.Timeout = 30 * time.Second
	if err := login(ctx, c, &o); err != nil {
		return err
	}
	ok("logged in as admin")

	runner := &channel.Runner{Client: c}
	db := adjacentDropbear()
	opts := ee72.Options{
		RootPassword: o.rootPassword,
		RemoveLock:   true,
		Dropbear:     db,
	}

	s, err := channel.NewSession(runner)
	if err != nil {
		if !errors.Is(err, channel.ErrPortUnavailable) {
			return err
		}
		return runEE72Chunked(ctx, c, runner, opts, db)
	}
	defer s.Close()
	detail("payload server: http://%s/", s.HostIP)

	if db != nil {
		detail("installing the dropbear binary found next to ee-setup")
		s.SetFile("dropbear", db.Binary)
	}
	script, err := ee72.Script(opts, s.HostIP)
	if err != nil {
		return err
	}

	step("Applying changes on the device")
	detail("backup → remount → adbd lock → root password → boot composition")
	out, err := s.RunScript(ctx, script)
	printDeviceLog(out)
	if err != nil {
		return fmt.Errorf("device script: %w", err)
	}
	if !strings.Contains(out, "DONE") {
		return fmt.Errorf("the device script did not finish")
	}
	ok("changes applied")
	_ = runner.Reset(ctx)

	if !confirm("Reboot the router now to start it in debug USB mode?") {
		step("Reboot skipped. ADB will appear after the next power-up.")
		printEE72Result(o.rootPassword)
		done()
		return nil
	}
	step("Rebooting")
	_ = runner.Run(ctx, "reboot")
	waitForADB(ctx)
	printEE72Result(o.rootPassword)
	done()
	return nil
}

// runEE72Chunked is the no-privilege path: port 80 could not be bound, so the
// script is sent in 28-byte commands. There is no live log, so the result is
// verified after the reboot.
func runEE72Chunked(ctx context.Context, c *tclapi.Client, runner *channel.Runner,
	opts ee72.Options, db *ee72.Dropbear) error {
	step("Port 80 is not available without administrator rights")
	step("Sending the script in 28-byte commands instead (no sudo, no server)")
	if db != nil {
		step("dropbear is skipped in this mode: no server to fetch the binary from")
		opts.Dropbear = nil
	}
	script, err := ee72.Script(opts, "")
	if err != nil {
		return err
	}

	step("Applying changes on the device")
	detail("backup → remount → adbd lock → root password → boot composition")
	fmt.Printf("    %d commands, base64 over the web API\n", len(channel.ChunkCommands(script)))
	if err := runner.ChunkScan(ctx, script); err != nil {
		return fmt.Errorf("sending the script: %w", err)
	}
	_ = runner.Reset(ctx)
	ok("script delivered and started")
	step("Waiting a few seconds for the device to finish")
	time.Sleep(5 * time.Second)
	step("There is no live log in this mode; the result is checked after reboot")

	if !confirm("Reboot the router now to start it in debug USB mode?") {
		step("Reboot skipped. ADB will appear after the next power-up.")
		printEE72Result(opts.RootPassword)
		done()
		return nil
	}
	step("Rebooting")
	_ = runner.Run(ctx, "reboot")
	waitForADB(ctx)
	printEE72Result(opts.RootPassword)
	done()
	return nil
}

func printEE72Result(rootPassword string) {
	fmt.Println("\n[+] Connect with:")
	fmt.Println("      adb shell      (verifies the adbd lock was removed)")
	fmt.Printf("      ssh root@4g.wifi        (password: %s)\n", rootPassword)
	fmt.Println("\n    Revert instructions are in the backup directory on the device:")
	fmt.Println("      /data/jrd/usr/eesetup-<stamp>/")
}

// ---------------------------------------------------------------- EE71

// errEE72NeedsWeb keeps EE72 provisioning on the documented web path.
func errEE72NeedsWeb() error {
	return fmt.Errorf("this is an EE72 — it is configured through its web interface, not ADB.\n" +
		"  Connect to the router's Wi-Fi (or its USB network interface), then run ee-setup again.\n" +
		"  Nothing was changed")
}

func runEE71(ctx context.Context, o options, a *adbwrap.ADB) error {
	if a == nil {
		var err error
		a, err = adbwrap.New(true)
		if err != nil {
			return fmt.Errorf("%w\n  The EE71 path needs adb; the EE72 path does not", err)
		}
	}
	detail("adb: %s", a.Bin())

	if st := a.State(ctx); st != "device" && st != "recovery" {
		step("No ADB transport; switching USB mass-storage to debug mode")
		if err := usbswitch.Switch(true); err != nil {
			switch {
			case errors.Is(err, usbswitch.ErrEDL):
				return fmt.Errorf("device is in Qualcomm EDL (9008) mode — power-cycle it and retry")
			case errors.Is(err, usbswitch.ErrUnsupported):
				return fmt.Errorf("this build has no USB backend (built without cgo).\n"+
					"  Build with cgo and libusb, or use the EE72 web path: %w", err)
			case errors.Is(err, usbswitch.ErrNotFound):
				return fmt.Errorf("no EE71 mass-storage interface found.\n" +
					"  Power the router off, hold RESET, connect USB, release when the LEDs blink,\n" +
					"  then run ee-setup again")
			default:
				return fmt.Errorf("USB switch: %w", err)
			}
		}
		step("Waiting for ADB")
		if err := a.WaitForDevice(ctx, 60*time.Second); err != nil {
			return fmt.Errorf("device did not appear in ADB mode: %w", err)
		}
	}
	switch a.Model(ctx) {
	case "EE71":
	case "EE72":
		return fmt.Errorf("this is an EE72, not an EE71 — nothing was changed")
	default:
		return fmt.Errorf("the connected ADB device is not an EE71")
	}
	ok("EE71 in ADB mode")

	step("Acquiring root")
	if err := a.Root(ctx); err != nil {
		return fmt.Errorf("adb root: %w", err)
	}

	step("Installing dropbear and the persistent-ADB patch")
	if err := ee71.Setup(ctx, a, ee71.Options{
		RootPassword:         o.rootPassword,
		InstallDropbear:      true,
		InstallPersistentADB: true,
	}); err != nil {
		return err
	}

	fmt.Println("\n[+] Connect with:")
	fmt.Println("      ssh root@4gee.wifi      (the root password you entered)")
	fmt.Println("      adb shell")
	done()
	return nil
}

// ---------------------------------------------------------------- shared

// login authenticates, asking for the admin password when it was not supplied.
func login(ctx context.Context, c *tclapi.Client, o *options) error {
	if o.password != "" {
		return c.Login(ctx)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		pw, err := promptPassword("Admin password: ")
		if err != nil {
			return err
		}
		c.Password = pw
		err = c.Login(ctx)
		if err == nil {
			o.password = pw
			return nil
		}
		if !errors.Is(err, tclapi.ErrAuth) {
			return err
		}
		fmt.Println("[!] Wrong password")
	}
	return tclapi.ErrAuth
}

func promptPassword(label string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("no terminal to ask for the password; pass --password")
	}
	fmt.Print(label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// confirm asks a yes/no question, defaulting to yes.
func confirm(question string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return true
	}
	fmt.Printf("\n[?] %s [Y/n] ", question)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes", "д", "да":
		return true
	default:
		return false
	}
}

// adjacentDropbear returns a dropbear binary shipped next to the executable.
// EE72 firmware is armv7 hard-float, so only such a build is usable. Keeping it
// next to the binary means no flag and no path guessing.
func adjacentDropbear() *ee72.Dropbear {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	bin, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "dropbear"))
	if err != nil || len(bin) == 0 {
		return nil
	}
	return &ee72.Dropbear{Binary: bin}
}

// waitForADB waits up to two minutes for the router to come back with ADB.
func waitForADB(ctx context.Context) {
	a, err := adbwrap.New(false)
	if err != nil {
		step("adb not installed; check manually with 'adb devices' after boot")
		return
	}
	step("Waiting for ADB (up to 2 minutes)")
	if err := a.WaitForDevice(ctx, 2*time.Minute); err != nil {
		fmt.Println("[!] ADB did not come up; check the router LEDs and try 'adb devices'")
		return
	}
	ok("ADB is up")
	if out, err := a.Shell(ctx, "id"); err == nil {
		detail("adb shell: %s", strings.TrimSpace(out))
	}
}

func printDeviceLog(out string) {
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fmt.Printf("    | %s\n", line)
	}
}

func done() {
	fmt.Println("\n=== done ===")
}

func step(format string, args ...any) {
	fmt.Printf("[*] "+format+"\n", args...)
}

func ok(format string, args ...any) {
	fmt.Printf("[+] "+format+"\n", args...)
}

func detail(format string, args ...any) {
	fmt.Printf("    %s\n", fmt.Sprintf(format, args...))
}
