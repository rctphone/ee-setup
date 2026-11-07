//go:build (darwin || linux) && !cgo

package usbswitch

// Without cgo there is no libusb, so the mass-storage switch is unavailable.
// The EE72 web-API path does not need it; EE71 users need a cgo build.

func enumerate() ([]USBDevice, error) { return nil, nil }

func sendSwitch(cdb []byte, verbose bool) error { return ErrUnsupported }
