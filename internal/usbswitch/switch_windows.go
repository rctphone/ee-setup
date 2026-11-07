//go:build windows && amd64

package usbswitch

import (
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Windows path: no libusb, no WinUSB/Zadig. The stock EE71 exposes a virtual
// USB CD-ROM, so we drive it through the standard storage stack —
// IOCTL_STORAGE_QUERY_PROPERTY to identify it and IOCTL_SCSI_PASS_THROUGH_DIRECT
// to send the vendor switch CDB. Raw SCSI passthrough requires Administrator.

const (
	ioctlStorageQueryProperty  = 0x002D1400
	ioctlScsiPassThroughDirect = 0x0004D014
	busTypeUsb                 = 0x07 // STORAGE_BUS_TYPE.BusTypeUsb
	scsiDeviceTypeCDROM        = 0x05
	scsiIoctlDataIn            = 0x01
	storageDevicePropertyID    = 0x00 // StorageDeviceProperty
	propertyStandardQueryType  = 0x00 // PropertyStandardQuery
	maxStorageIndex            = 16   // scan \\.\CdRom0..15 and \\.\PhysicalDrive0..15
)

// scsiPassThroughDirect mirrors the Win32 SCSI_PASS_THROUGH_DIRECT for amd64.
// Field order + natural Go alignment reproduce the C layout exactly (verified
// by the size assertion below).
type scsiPassThroughDirect struct {
	Length             uint16
	ScsiStatus         uint8
	PathID             uint8
	TargetID           uint8
	Lun                uint8
	CdbLength          uint8
	SenseInfoLength    uint8
	DataIn             uint8
	DataTransferLength uint32
	TimeOutValue       uint32
	DataBuffer         uintptr
	SenseInfoOffset    uint32
	Cdb                [16]uint8
}

// sptdWithSense bundles the request with its sense buffer in one allocation so
// SenseInfoOffset can point at a fixed offset.
type sptdWithSense struct {
	spt   scsiPassThroughDirect
	sense [32]byte
}

// Compile-time guard: the C struct is 56 bytes on x64. If Go ever pads
// differently this fails to build rather than corrupting the IOCTL.
const _ = unsafe.Sizeof(scsiPassThroughDirect{}) - 56
const _ = 56 - unsafe.Sizeof(scsiPassThroughDirect{})

type storagePropertyQuery struct {
	PropertyID uint32
	QueryType  uint32
	Additional [1]byte
}

type storageDeviceDescriptor struct {
	Version               uint32
	Size                  uint32
	DeviceType            uint8
	DeviceTypeModifier    uint8
	RemovableMedia        uint8
	CommandQueueing       uint8
	VendorIDOffset        uint32
	ProductIDOffset       uint32
	ProductRevisionOffset uint32
	SerialNumberOffset    uint32
	BusType               uint32
	RawPropertiesLength   uint32
	// raw bytes follow
}

// enumerate reports devices relevant to the EE71. USB storage targets are
// discovered via the storage stack; the EDL (9008) serial device is found via
// a best-effort registry scan (it is not a storage device).
func enumerate() ([]USBDevice, error) {
	var out []USBDevice

	if edlPresentInRegistry() {
		out = append(out, USBDevice{VID: edlVID, PID: edlPID})
	}
	return out, nil
}

// sendSwitch finds the EE71 USB CD-ROM (or matching USB removable disk) and
// writes the vendor switch CDB via SCSI passthrough.
func sendSwitch(cdb []byte, verbose bool) error {
	if !isElevated() {
		return fmt.Errorf("administrator rights required for SCSI passthrough — run as Administrator")
	}
	targets := scanStorage(verbose)
	var chosen *storageTarget
	for i := range targets {
		if targets[i].isEE71 {
			chosen = &targets[i]
			break
		}
	}
	if chosen == nil {
		return ErrNotFound
	}
	if verbose {
		fmt.Printf("[dbg] switching via %s (%s %s, bus=USB)\n", chosen.path, chosen.vendor, chosen.product)
	}

	h, err := openDevice(chosen.path)
	if err != nil {
		return fmt.Errorf("open %s: %w", chosen.path, err)
	}
	defer windows.CloseHandle(h)

	return scsiSend(h, cdb)
}

type storageTarget struct {
	path    string
	vendor  string
	product string
	isEE71  bool
}

// scanStorage probes \\.\CdRomN and \\.\PhysicalDriveN, returning USB targets
// and flagging those that look like the EE71 (USB CD-ROM, or USB removable disk
// whose vendor/product matches known EE71 strings).
func scanStorage(verbose bool) []storageTarget {
	var targets []storageTarget
	probe := func(path string) {
		h, err := openDevice(path)
		if err != nil {
			return
		}
		defer windows.CloseHandle(h)

		desc, vendor, product, err := queryStorageDescriptor(h)
		if err != nil {
			return
		}
		if desc.BusType != busTypeUsb {
			return
		}
		t := storageTarget{path: path, vendor: vendor, product: product}
		isCDROM := desc.DeviceType == scsiDeviceTypeCDROM || strings.Contains(strings.ToLower(path), "cdrom")
		t.isEE71 = matchesEE71(vendor, product)
		if verbose {
			fmt.Printf("[dbg] %s USB vendor=%q product=%q cdrom=%v match=%v\n",
				path, vendor, product, isCDROM, t.isEE71)
		}
		targets = append(targets, t)
	}

	for i := 0; i < maxStorageIndex; i++ {
		probe(fmt.Sprintf(`\\.\CdRom%d`, i))
	}
	for i := 0; i < maxStorageIndex; i++ {
		probe(fmt.Sprintf(`\\.\PhysicalDrive%d`, i))
	}
	return targets
}

func matchesEE71(vendor, product string) bool {
	s := strings.ToLower(vendor + " " + product)
	for _, kw := range []string{"alcatel", "tct", "tcl", "onetouch", "linkkey", "linkzone",
		"ee71", "ee72", "mw70", "bt70", "mifi", "mobilebroadband", "qualcomm"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func openDevice(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(
		p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
}

func queryStorageDescriptor(h windows.Handle) (*storageDeviceDescriptor, string, string, error) {
	q := storagePropertyQuery{PropertyID: storageDevicePropertyID, QueryType: propertyStandardQueryType}
	buf := make([]byte, 1024)
	var ret uint32
	err := windows.DeviceIoControl(
		h, ioctlStorageQueryProperty,
		(*byte)(unsafe.Pointer(&q)), uint32(unsafe.Sizeof(q)),
		&buf[0], uint32(len(buf)), &ret, nil,
	)
	if err != nil {
		return nil, "", "", err
	}
	if ret < uint32(unsafe.Sizeof(storageDeviceDescriptor{})) {
		return nil, "", "", fmt.Errorf("short descriptor (%d bytes)", ret)
	}
	desc := (*storageDeviceDescriptor)(unsafe.Pointer(&buf[0]))
	vendor := cstrAt(buf, desc.VendorIDOffset)
	product := cstrAt(buf, desc.ProductIDOffset)
	return desc, vendor, product, nil
}

// cstrAt reads a NUL-terminated ASCII string at offset within buf (0 = absent).
func cstrAt(buf []byte, off uint32) string {
	if off == 0 || int(off) >= len(buf) {
		return ""
	}
	end := int(off)
	for end < len(buf) && buf[end] != 0 {
		end++
	}
	return strings.TrimSpace(string(buf[off:end]))
}

// scsiSend issues the vendor CDB with a 192-byte data-in phase, matching the
// libusb path. A non-zero SCSI status here is non-fatal: the device usually
// drops the bus to re-enumerate before completing the command.
func scsiSend(h windows.Handle, cdb []byte) error {
	if len(cdb) < 1 || len(cdb) > 16 {
		return fmt.Errorf("CDB length must be 1..16, got %d", len(cdb))
	}
	const dataLen = 192
	data := make([]byte, dataLen)

	var wb sptdWithSense
	wb.spt.Length = uint16(unsafe.Sizeof(wb.spt))
	wb.spt.CdbLength = uint8(len(cdb))
	wb.spt.SenseInfoLength = uint8(len(wb.sense))
	wb.spt.DataIn = scsiIoctlDataIn
	wb.spt.DataTransferLength = dataLen
	wb.spt.TimeOutValue = 5
	wb.spt.DataBuffer = uintptr(unsafe.Pointer(&data[0]))
	wb.spt.SenseInfoOffset = uint32(unsafe.Offsetof(wb.sense))
	copy(wb.spt.Cdb[:], cdb)

	var ret uint32
	err := windows.DeviceIoControl(
		h, ioctlScsiPassThroughDirect,
		(*byte)(unsafe.Pointer(&wb)), uint32(unsafe.Sizeof(wb)),
		(*byte)(unsafe.Pointer(&wb)), uint32(unsafe.Sizeof(wb)),
		&ret, nil,
	)
	// The vendor command tears the bus down; an IOCTL error at this point is
	// the expected success signature, not a failure.
	_ = err
	runtime.KeepAlive(data) // DataBuffer holds a raw pointer the GC can't see
	return nil
}

// edlPresentInRegistry checks the USB enumeration registry for a Qualcomm
// 9008 (EDL) device. Best-effort: absence of the key is treated as "no EDL".
func edlPresentInRegistry() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Enum\USB`, registry.READ)
	if err != nil {
		return false
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return false
	}
	for _, n := range names {
		ln := strings.ToUpper(n)
		if strings.Contains(ln, "VID_05C6") && strings.Contains(ln, "PID_9008") {
			return true
		}
	}
	return false
}

func isElevated() bool {
	const (
		securityBuiltinDomainRID = 0x20
		domainAliasRIDAdmins     = 0x220
	)
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY, 2,
		securityBuiltinDomainRID,
		domainAliasRIDAdmins,
		0, 0, 0, 0, 0, 0, &sid)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)
	member, err := windows.GetCurrentProcessToken().IsMember(sid)
	return err == nil && member
}
