package usbswitch

import "errors"

// Sentinel errors surfaced to the caller for flow control.
var (
	// ErrEDL means the device enumerated in Qualcomm Emergency Download mode
	// (0x05c6:0x9008). The tool must never touch it — recovery from EDL needs
	// firehose/EDL flashing, not a USB switch.
	ErrEDL = errors.New("device is in Qualcomm EDL (9008) mode")
	// ErrNotFound means no EE71 mass-storage interface was found to switch.
	ErrNotFound = errors.New("no EE71 USB mass-storage device found")
	// ErrUnsupported means this build has no USB backend (built without cgo).
	ErrUnsupported = errors.New("USB switching is not available in this build")
)

const (
	edlVID = 0x05c6 // Qualcomm
	edlPID = 0x9008 // EDL / Firehose
	tctVID = 0x1bbb // TCT Mobile (Alcatel) — stock EE71 vendor ID
)

// USBDevice is a minimal descriptor snapshot used for state detection. The
// per-OS enumerate() fills it without opening/claiming the device.
type USBDevice struct {
	VID, PID uint16
}

// IsEDL reports whether the device is in Qualcomm EDL mode.
func (d USBDevice) IsEDL() bool { return d.VID == edlVID && d.PID == edlPID }

// ModelFromUSB identifies the attached router from its TCT USB PID. The
// mapping comes from usb_config.ini: 0908/025E are EE71, 0196/0191/F000 are
// EE72. PID 0195 is deliberately ambiguous — both models use it — and returns
// an empty string.
func ModelFromUSB() string {
	devs, err := enumerate()
	if err != nil {
		return ""
	}
	model := ""
	for _, d := range devs {
		if d.VID != tctVID {
			continue
		}
		m := modelFromPID(d.PID)
		if m == "" {
			continue
		}
		if model != "" && model != m {
			return "" // contradictory evidence
		}
		model = m
	}
	return model
}

func modelFromPID(pid uint16) string {
	switch pid {
	case 0x0908, 0x025E: // Mac switch / release-init composition
		return "EE71"
	case 0x0196, 0x0191, 0xF000: // debug / Mac switch / service composition
		return "EE72"
	}
	return ""
}

// DetectEDL reports whether any attached device is in EDL mode. Callers use
// this as a hard stop before any USB write.
func DetectEDL() (bool, error) {
	devs, err := enumerate()
	if err != nil {
		return false, err
	}
	for _, d := range devs {
		if d.IsEDL() {
			return true, nil
		}
	}
	return false, nil
}

// Switch finds the EE71 mass-storage interface and sends the vendor command
// that flips it into ADB mode. It aborts with ErrEDL if an EDL device is
// present, and returns ErrNotFound if no MSC target exists. A CSW timeout
// immediately after the vendor command is treated as success, because the
// device re-enumerates before it can answer.
func Switch(verbose bool) error {
	edl, err := DetectEDL()
	if err != nil {
		return err
	}
	if edl {
		return ErrEDL
	}
	return sendSwitch(SwitchCDB, verbose)
}
