package tclapi

import (
	"crypto/sha512"
	"encoding/hex"

	"golang.org/x/crypto/pbkdf2"
)

// PBKDF2 params are fixed by the firmware web UI (loginAuthType=5).
const (
	pbkdf2Iterations = 1024
	pbkdf2KeyLen     = 64
)

// PBKDF2Password returns the 128-character hex hash the device compares
// against. Salt is the per-device value returned by GetDeviceSt.
func PBKDF2Password(password, salt string) string {
	return hex.EncodeToString(pbkdf2.Key(
		[]byte(password), []byte(salt), pbkdf2Iterations, pbkdf2KeyLen, sha512.New))
}
