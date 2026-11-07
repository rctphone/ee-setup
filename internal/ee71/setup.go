// Package ee71 provisions an EE71 over ADB: it installs a dropbear SSH server
// into /system, sets the root password, and makes ADB survive reboots with the
// kernel USB-composition patch.
package ee71

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rctphone/ee-setup/internal/adbwrap"
)

//go:embed assets/dropbearmulti-2014.66
var dropbearBinary []byte

//go:embed assets/dropbear.init
var dropbearInit []byte

//go:embed assets/sftp-server
var sftpServer []byte

//go:embed assets/patch_usb_kernel
var patchUSBKernel []byte

// Options controls the EE71 procedure.
type Options struct {
	RootPassword         string
	InstallDropbear      bool
	InstallPersistentADB bool
	SSHPort              int
}

func (o *Options) Defaults() {
	if o.SSHPort == 0 {
		o.SSHPort = 22
	}
}

// remote file destinations under /system (the operational root filesystem).
const (
	remoteDropbearBin = "/system/usr/sbin/dropbearmulti"
	remoteInitScript  = "/system/etc/init.d/dropbear"
	remoteSFTP        = "/system/usr/libexec/sftp-server"
	remoteUSBPatcher  = "/system/etc/init.d/patch_usb_kernel"
)

// Setup installs the requested components. ADB must be up and rooted.
func Setup(ctx context.Context, a *adbwrap.ADB, o Options) error {
	o.Defaults()

	tmp, err := os.MkdirTemp("", "ee-setup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if _, err := a.Shell(ctx, "mkdir -p /system/usr/sbin /system/usr/bin /system/etc/init.d "+
		"/system/etc/rc5.d /system/etc/rcS.d /system/usr/libexec"); err != nil {
		return fmt.Errorf("creating directories: %w", err)
	}

	if o.InstallDropbear {
		fmt.Println("[*] Installing dropbear...")
		if err := push(ctx, a, tmp, "dropbearmulti", dropbearBinary, remoteDropbearBin); err != nil {
			return err
		}
		if err := push(ctx, a, tmp, "dropbear.init", dropbearInit, remoteInitScript); err != nil {
			return err
		}
		if err := push(ctx, a, tmp, "sftp-server", sftpServer, remoteSFTP); err != nil {
			fmt.Printf("    [WARN] sftp-server: %v\n", err)
		}
		if _, err := a.Shell(ctx, dropbearLinks(o.SSHPort)); err != nil {
			return fmt.Errorf("configuring dropbear: %w", err)
		}
	}

	if o.InstallPersistentADB {
		fmt.Println("[*] Installing persistent-ADB kernel patch...")
		if err := push(ctx, a, tmp, "patch_usb_kernel", patchUSBKernel, remoteUSBPatcher); err != nil {
			return err
		}
		if _, err := a.Shell(ctx, persistentADBScript()); err != nil {
			return fmt.Errorf("installing USB patch: %w", err)
		}
	}

	fmt.Println("[*] Setting root password...")
	if _, err := a.Shell(ctx, passwordScript(o.RootPassword)); err != nil {
		return fmt.Errorf("setting password: %w", err)
	}
	if _, err := a.Shell(ctx, "sync"); err != nil {
		return err
	}

	if o.InstallDropbear {
		fmt.Println("[*] Starting dropbear...")
		if out, err := a.Shell(ctx, "/system/etc/init.d/dropbear start 2>&1"); err != nil {
			fmt.Printf("    [WARN] dropbear did not start: %v (%s)\n", err, strings.TrimSpace(out))
			fmt.Println("    It will start on the next boot via /system/etc/rc5.d/S10dropbear")
		} else {
			fmt.Println("    running")
		}
	}
	return nil
}

func push(ctx context.Context, a *adbwrap.ADB, dir, name string, data []byte, remote string) error {
	local := filepath.Join(dir, name)
	if err := os.WriteFile(local, data, 0o755); err != nil {
		return err
	}
	if err := a.Push(ctx, local, remote); err != nil {
		return fmt.Errorf("pushing %s: %w", name, err)
	}
	return nil
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func dropbearLinks(port int) string {
	return `
set -e
cd /system/usr/sbin
ln -sf dropbearmulti dropbear
ln -sf dropbearmulti dropbearkey
ln -sf dropbearmulti dropbearconvert
cd /system/usr/bin
ln -sf /usr/sbin/dropbearmulti ssh
ln -sf /usr/sbin/dropbearmulti scp
cd /system/etc/rc5.d
ln -sf ../init.d/dropbear S10dropbear
chmod 0755 /system/usr/sbin/dropbearmulti /system/etc/init.d/dropbear
chmod 0755 /system/usr/libexec/sftp-server 2>/dev/null || true
mkdir -p /system/etc/default
` + fmt.Sprintf("printf 'DROPBEAR_PORT=%d\\n' > /system/etc/default/dropbear\n", port)
}

func persistentADBScript() string {
	return `
set -e
chmod 0755 /system/etc/init.d/patch_usb_kernel
ln -sf ../init.d/patch_usb_kernel /system/etc/rcS.d/S01patch_usb_kernel
COMP=/system/sbin/usb/boot_hsusb_composition
if [ -f "$COMP" ]; then
    if ! grep -q ffs "$COMP"; then
        sed -i 's/echo rndis_qc,mass_storage/echo rndis_qc,diag,ffs,serial,mass_storage/' "$COMP"
        sed -i 's/echo ecm,mass_storage/echo ecm,diag,ffs,serial,mass_storage/' "$COMP"
        echo "boot_hsusb_composition patched"
    else
        echo "boot_hsusb_composition already has ADB"
    fi
fi
`
}

// passwordScript feeds the credential through stdin. It must not rely on an
// environment variable crossing chroot, which would arrive empty.
func passwordScript(password string) string {
	pw := shQuote(password)
	return `
set -e
PW=` + pw + `
if printf 'root:%s\n' "$PW" | chroot /system chpasswd 2>/dev/null; then
    echo "password set (chpasswd)"
else
    printf '%s\n%s\n' "$PW" "$PW" | chroot /system passwd root
    echo "password set (passwd)"
fi
`
}
