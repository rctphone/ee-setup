# ee-setup — ADB, SSH and root on Alcatel EE71 / EE72

One Go binary that turns a stock Alcatel EE71 or EE72 router into a device you
can actually administer: working ADB that survives reboots, a dropbear SSH
server, and a known root password.

No Windows-only TCL tool, no driver installation, no Python.

## Usage

```sh
ee-setup
```

That is the whole interface. It finds the router, asks for the admin password,
applies the changes with a backup first, prints every step and offers to reboot
so ADB comes up. The root/SSH password defaults to `oemlinux1`.

Flags, for non-interactive use:

```sh
--host IP            skip auto-detection
--password PW        admin password instead of the prompt
--root-password PW   root/SSH password (default: oemlinux1)
```

## What it does

### EE72

The EE72 has no reachable ADB in its normal composition, but its web API accepts
authenticated commands. `ee-setup` logs in, then uses that channel to:

1. back up `/systemrw/boot_hsusb_comp`, `/etc/shadow` and the adbd lock under
   `/data/jrd/usr/eesetup-<stamp>/`;
2. remount `/` read-write with the documented modprobe helper, and back to `ro`
   on exit (including on failure);
3. remove `/jrd-resource/resource/lock_flag` so adbd accepts `shell:`, `exec:`
   and `sync:`;
4. set the root password with `chpasswd` (falling back to `passwd`);
5. write `0196` to `/systemrw/boot_hsusb_comp`, so the debug composition — with
   ADB — is used from the next boot on.

Nothing touches the boot chain, MTD partitions, recovery or efs2.

### EE71

The EE71 needs the USB mass-storage switch first. `ee-setup` sends the vendor
SCSI command itself, waits for ADB, then installs from embedded assets:

- dropbear (`dropbearmulti`) into `/system/usr/sbin` with init scripts and
  rc links;
- the `patch_usb_kernel` init script, which keeps ADB in the USB composition
  after reboot;
- the root password.

If the router is in mass-storage mode: power it off, hold RESET, connect USB,
release when the LEDs blink, then run `ee-setup` again.

## Requirements

- **adb** on `PATH` (Android Platform Tools) for the EE71 path. The EE72 path
  does not need adb until you reboot.
- **Administrator rights**: port 80 is used to serve the payload script to the
  device, and the EE71 USB switch needs raw USB access.
- **EE72 dropbear** is optional and not bundled. Put an ARMv7 **hard-float**
  `dropbear` binary next to the `ee-setup` executable and it is installed and
  started automatically. EE71 soft-float binaries will not run on the EE72 —
  the firmware is `armv7l` hard-float.

## Safety

- Every run creates a backup directory before the first write.
- The script carries a trap that returns `/` to read-only even when it fails.
- The tool refuses to touch a device in Qualcomm EDL (`05c6:9008`).
- Nothing is written until the admin login succeeds.

## Revert

On the device (over ADB or SSH):

```sh
# EE72
cat /data/jrd/usr/eesetup-<stamp>/boot_hsusb_comp > /systemrw/boot_hsusb_comp
cat /data/jrd/usr/eesetup-<stamp>/shadow > /etc/shadow
cp /data/jrd/usr/eesetup-<stamp>/lock_flag /jrd-resource/resource/lock_flag
```

For the EE71, remove `/system/etc/rcS.d/S01patch_usb_kernel` and restore
`/system/sbin/usb/boot_hsusb_composition` from your backup.

## Build

```sh
./build.sh assets
./build.sh build            # host
./build.sh build windows    # CGO disabled, no libusb needed
VERSION=v0.1.0 ./build.sh release   # packaged matrix in dist/
```

The release matrix follows Go's cross-compilation rules:

| Target | CGO | EE71 USB switch |
|---|---|---|
| darwin/arm64 | on (libusb) | yes |
| darwin/amd64 | on (libusb) | yes |
| windows/amd64 | off | yes (Win32 SCSI, no libusb) |
| linux/amd64 | off | no — the EE72 web path works |

A cgo-free build on macOS/Linux still handles EE72 completely; only the EE71
mass-storage switch needs libusb. The USB backend is selected by build tags
(`switch_unix.go` vs `switch_nocgo.go`), so no runtime probing is involved.

## Status

The EE72 procedure follows `docs/ee72_access_method.md` (device firmware
`EE72E_E1_01.00_04`). The EE71 path reuses the procedures from
`github/ee71-update` and `github/dropbear`. End-to-end runs on hardware are the
next step.
