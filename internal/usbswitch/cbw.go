// Package usbswitch sends the vendor SCSI command that flips an Alcatel EE71
// from USB Mass-Storage mode into ADB mode. The framing here is the USB
// Bulk-Only Transport (BOT) Command/Status wrapper, ported byte-for-byte from
// scripts/tcl_switch_usb.py so both tools speak the identical protocol.
package usbswitch

import (
	"encoding/binary"
	"fmt"
)

const (
	cbwSignature = 0x43425355 // 'USBC' little-endian
	cswSignature = 0x53425355 // 'USBS' little-endian

	cbwLen = 31
	cswLen = 13

	flagDataIn = 0x80
)

// SwitchCDB is the TCL/Qualcomm vendor command that triggers the USB
// composition switch on the EE71 (same default as tcl_switch_usb.py).
var SwitchCDB = []byte{0x16, 0xf9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}

// inquiryCDB is a standard SCSI INQUIRY requesting 36 bytes — used to confirm
// the interface is a live MSC target before sending the vendor command.
var inquiryCDB = []byte{0x12, 0, 0, 0, 0x24, 0}

// buildCBW packs a 31-byte Command Block Wrapper. Mirrors the python
// struct.pack("<I I I B B B 16s", ...).
func buildCBW(tag, dataLen uint32, dataIn bool, lun uint8, cdb []byte) ([]byte, error) {
	if len(cdb) < 1 || len(cdb) > 16 {
		return nil, fmt.Errorf("CDB length must be 1..16, got %d", len(cdb))
	}
	b := make([]byte, cbwLen)
	binary.LittleEndian.PutUint32(b[0:4], cbwSignature)
	binary.LittleEndian.PutUint32(b[4:8], tag)
	binary.LittleEndian.PutUint32(b[8:12], dataLen)
	if dataIn {
		b[12] = flagDataIn
	}
	b[13] = lun
	b[14] = byte(len(cdb) & 0x1f)
	copy(b[15:31], cdb) // remaining bytes stay zero-padded
	return b, nil
}

// cswStatus is the SCSI command status from a Command Status Wrapper.
type cswStatus uint8

const (
	cswPassed cswStatus = 0x00
)

// parseCSW validates a 13-byte Command Status Wrapper and returns the tag and
// status. It errors on a bad signature or wrong length.
func parseCSW(data []byte, wantTag uint32) (status cswStatus, residue uint32, err error) {
	if len(data) != cswLen {
		return 0, 0, fmt.Errorf("CSW must be %d bytes, got %d", cswLen, len(data))
	}
	if sig := binary.LittleEndian.Uint32(data[0:4]); sig != cswSignature {
		return 0, 0, fmt.Errorf("invalid CSW signature: 0x%08x", sig)
	}
	tag := binary.LittleEndian.Uint32(data[4:8])
	if tag != wantTag {
		return 0, 0, fmt.Errorf("CSW tag mismatch: 0x%08x != 0x%08x", tag, wantTag)
	}
	residue = binary.LittleEndian.Uint32(data[8:12])
	return cswStatus(data[12]), residue, nil
}
