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
	flag.StringVar(&o.rootPassword, "root-password", "", "new root/SSH password (default: ask)")
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
		step("No web interface found; trying the EE71 USB path")
		return runEE71(ctx, o)
	}
	ok("found %s at %s", d.Model, d.Host)
	if v := d.Version(); v != "" {
		detail("firmware %s", v)
	}

	if d.Model == discover.ModelEE72 {
		return runEE72(ctx, o, d)
	}
	return runEE71(ctx, o)
}

// ---------------------------------------------------------------- EE72

func runEE72(ctx context.Context, o options, d *discover.Device) error {
	if err := ensureRootPassword(&o); err != nil {
		return err
	}
	step("Logging in to the web interface")
	c := tclapi.New(d.Host, o.password)
	c.VHost = d.VHost
	if err := login(ctx, c, &o); err != nil {
		return err
	}
	ok("logged in as admin")

	step("Starting the local payload server")
	s, err := channel.NewSession(&channel.Runner{Client: c})
	if err != nil {
		return err
	}
	defer s.Close()
	detail("http://%s/", s.HostIP)

	db := adjacentDropbear()
	if db != nil {
		detail("installing the dropbear binary found next to ee-setup")
		s.SetFile("dropbear", db.Binary)
	}
	script, err := ee72.Script(ee72.Options{
		RootPassword: o.rootPassword,
		RemoveLock:   true,
		Dropbear:     db,
	}, s.HostIP)
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

	runner := &channel.Runner{Client: c}
	_ = runner.Reset(ctx)

	if !confirm("Reboot the router now to start it in debug USB mode?") {
		step("Reboot skipped. ADB will appear after the next power-up.")
		printEE72Result(o)
		done()
		return nil
	}

	step("Rebooting")
	// The connection dies with the device; that is expected.
	_ = runner.Run(ctx, "reboot")
	waitForADB(ctx)
	printEE72Result(o)
	done()
	return nil
}

func printEE72Result(o options) {
	fmt.Println("\n[+] Connect with:")
	fmt.Println("      adb shell")
	fmt.Println("      ssh root@4g.wifi        (the root password you entered)")
	fmt.Println("\n    Revert instructions are in the backup directory on the device:")
	fmt.Println("      /data/jrd/usr/eesetup-<stamp>/")
}

// ---------------------------------------------------------------- EE71

func runEE71(ctx context.Context, o options) error {
	if err := ensureRootPassword(&o); err != nil {
		return err
	}
	a, err := adbwrap.New(true)
	if err != nil {
		return fmt.Errorf("%w\n  The EE71 path needs adb; the EE72 path does not", err)
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
	if !a.IsEE71(ctx) {
		return fmt.Errorf("the connected ADB device does not look like an EE71")
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

// ensureRootPassword asks for the new root password when it was not supplied.
// The password is never stored anywhere but in memory for this run.
func ensureRootPassword(o *options) error {
	if o.rootPassword != "" {
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("pass --root-password in non-interactive mode")
	}
	for {
		pw, err := promptPassword("New root/SSH password: ")
		if err != nil {
			return err
		}
		if pw == "" {
			fmt.Println("    password cannot be empty")
			continue
		}
		again, err := promptPassword("Repeat: ")
		if err != nil {
			return err
		}
		if pw != again {
			fmt.Println("    passwords do not match, try again")
			continue
		}
		o.rootPassword = pw
		return nil
	}
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
