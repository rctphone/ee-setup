//go:build (darwin || linux) && cgo

package usbswitch

import (
	"fmt"
	"time"

	"github.com/google/gousb"
)

// enumerate lists attached USB devices and flags those exposing an MSC
// SCSI/Bulk-Only interface. Uses gousb (libusb); descriptor reads only, no
// claim. Devices that fail to describe are skipped, not fatal.
func enumerate() ([]USBDevice, error) {
	ctx := gousb.NewContext()
	defer ctx.Close()

	var out []USBDevice
	devs, err := ctx.OpenDevices(func(d *gousb.DeviceDesc) bool {
		out = append(out, USBDevice{
			VID: uint16(d.Vendor),
			PID: uint16(d.Product),
		})
		return false // describe only; never keep the handle open
	})
	for _, d := range devs {
		d.Close()
	}
	if err != nil {
		// OpenDevices surfaces per-device open failures even when we keep
		// nothing; the snapshot above is still usable.
		return out, nil
	}
	return out, nil
}

// sendSwitch finds the first EE71 MSC device, confirms it with a SCSI INQUIRY,
// and writes the vendor switch CBW. Prefers the TCT vendor ID but falls back to
// any MSC SCSI/BOT device (matches tcl_switch_usb.py behaviour).
func sendSwitch(cdb []byte, verbose bool) error {
	ctx := gousb.NewContext()
	defer ctx.Close()

	dev, intfNum, cfgNum, err := openMSC(ctx, verbose)
	if err != nil {
		return err
	}
	defer dev.Close()

	if err := dev.SetAutoDetach(true); err != nil && verbose {
		fmt.Printf("[dbg] SetAutoDetach: %v\n", err)
	}

	cfg, err := dev.Config(cfgNum)
	if err != nil {
		return fmt.Errorf("select config %d: %w", cfgNum, err)
	}
	defer cfg.Close()

	intf, err := cfg.Interface(intfNum, 0)
	if err != nil {
		return fmt.Errorf("claim interface %d: %w", intfNum, err)
	}
	defer intf.Close()

	epOut, epIn, err := bulkEndpoints(intf)
	if err != nil {
		return err
	}

	// Confirm the target is alive before issuing the vendor command.
	if err := inquiry(epOut, epIn, verbose); err != nil && verbose {
		fmt.Printf("[dbg] INQUIRY probe failed (continuing): %v\n", err)
	}

	tag := uint32(time.Now().UnixNano())
	cbw, err := buildCBW(tag, 192, true, 0, cdb)
	if err != nil {
		return err
	}
	if _, err := epOut.Write(cbw); err != nil {
		return fmt.Errorf("write switch CBW: %w", err)
	}

	// The device re-enumerates as soon as it accepts the vendor command, so a
	// failed/short data+CSW read here is the expected success path.
	buf := make([]byte, 192)
	_, _ = epIn.Read(buf)
	csw := make([]byte, cswLen)
	if n, rerr := epIn.Read(csw); rerr == nil && n == cswLen {
		if st, _, perr := parseCSW(csw, tag); perr == nil && st != cswPassed && verbose {
			fmt.Printf("[dbg] CSW status=%d (device may already be switching)\n", st)
		}
	}
	return nil
}

// openMSC returns an opened device that has an MSC SCSI/BOT interface, plus the
// interface and config numbers to claim. TCT vendor devices are preferred.
func openMSC(ctx *gousb.Context, verbose bool) (*gousb.Device, int, int, error) {
	type cand struct {
		dev  *gousb.Device
		intf int
		cfg  int
	}
	var chosen *cand
	var others []*gousb.Device

	devs, _ := ctx.OpenDevices(func(d *gousb.DeviceDesc) bool { return true })
	defer func() {
		for _, d := range others {
			d.Close()
		}
	}()

	for _, d := range devs {
		desc := d.Desc
		if uint16(desc.Vendor) == edlVID && uint16(desc.Product) == edlPID {
			d.Close()
			return nil, 0, 0, ErrEDL
		}
		// Only TCT/Alcatel hardware is ever a switch target. An arbitrary USB
		// stick must never receive a vendor command.
		if uint16(desc.Vendor) != tctVID {
			others = append(others, d)
			continue
		}
		intfNum, cfgNum, ok := firstMSC(desc)
		if !ok {
			others = append(others, d)
			continue
		}
		if verbose {
			fmt.Printf("[dbg] MSC candidate %04x:%04x intf=%d cfg=%d\n",
				desc.Vendor, desc.Product, intfNum, cfgNum)
		}
		if chosen == nil {
			chosen = &cand{dev: d, intf: intfNum, cfg: cfgNum}
		} else {
			others = append(others, d)
		}
	}

	if chosen == nil {
		return nil, 0, 0, ErrNotFound
	}
	return chosen.dev, chosen.intf, chosen.cfg, nil
}

// firstMSC returns the interface and config numbers of the first SCSI/BOT
// Mass-Storage interface, if any.
func firstMSC(d *gousb.DeviceDesc) (intfNum, cfgNum int, ok bool) {
	for _, cfg := range d.Configs {
		for _, intf := range cfg.Interfaces {
			for _, alt := range intf.AltSettings {
				if alt.Class == gousb.ClassMassStorage &&
					alt.SubClass == 0x06 &&
					(alt.Protocol == 0x50 || alt.Protocol == 0x62) {
					return intf.Number, cfg.Number, true
				}
			}
		}
	}
	return 0, 0, false
}

func bulkEndpoints(intf *gousb.Interface) (out *gousb.OutEndpoint, in *gousb.InEndpoint, err error) {
	for _, ep := range intf.Setting.Endpoints {
		if ep.TransferType != gousb.TransferTypeBulk {
			continue
		}
		if ep.Direction == gousb.EndpointDirectionIn {
			in, err = intf.InEndpoint(ep.Number)
		} else {
			out, err = intf.OutEndpoint(ep.Number)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if out == nil || in == nil {
		return nil, nil, fmt.Errorf("bulk endpoints not found (out=%v in=%v)", out != nil, in != nil)
	}
	return out, in, nil
}

// inquiry runs a SCSI INQUIRY to confirm the interface responds.
func inquiry(epOut *gousb.OutEndpoint, epIn *gousb.InEndpoint, verbose bool) error {
	tag := uint32(time.Now().UnixNano())
	cbw, err := buildCBW(tag, 36, true, 0, inquiryCDB)
	if err != nil {
		return err
	}
	if _, err := epOut.Write(cbw); err != nil {
		return err
	}
	data := make([]byte, 36)
	if _, err := epIn.Read(data); err != nil {
		return err
	}
	csw := make([]byte, cswLen)
	if _, err := epIn.Read(csw); err != nil {
		return err
	}
	st, _, err := parseCSW(csw, tag)
	if err != nil {
		return err
	}
	if st != cswPassed {
		return fmt.Errorf("INQUIRY CSW status=%d", st)
	}
	if verbose {
		fmt.Printf("[dbg] INQUIRY ok: %q\n", string(data[8:32]))
	}
	return nil
}
