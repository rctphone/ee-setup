// Package tclapi implements the JSON-RPC protocol used by the TCL/Alcatel
// router web interface (GoAhead "JRD" stack).
//
// EE72 (loginAuthType=5) encrypts every parameter and result envelope with
// AES-256-CBC in OpenSSL "Salted__" format, using a public key before login and
// a per-session key afterwards. Envelope handling is delegated to go-openssl,
// which implements the same EVP_BytesToKey/MD5 scheme as the firmware's
// CryptoJS code.
package tclapi

import (
	"encoding/json"
	"fmt"

	openssl "github.com/Luzifer/go-openssl/v4"
)

// Protocol constants, fixed by the firmware.
const (
	// PublicKey encrypts traffic before authentication.
	PublicKey = "EE5GRouter2020"
	// VerificationKey is sent in the _TclRequestVerificationKey header.
	VerificationKey = "KSDHSDFOGQ5WERYTUIQWERTYUISDFG1HJZXCVCXBN2GDSMNDHKVKFsVBNf"
	// XORKey obfuscates the UserName field (ASCII, 32 bytes).
	XORKey = "e5dl12XYVggihggafXWf0f2YSf2Xngd1"
	// DefaultVHost is the Host header the web server requires. Requests with
	// any other Host are answered with a redirect.
	DefaultVHost = "4g.wifi"
	// DeviceName and CustomID identify the firmware family in headers.
	DeviceName = "EE72E"
	CustomID   = "E1"
)

var crypter = openssl.New()

// Encrypt seals a JSON value for the device with a random salt.
func Encrypt(v any, secret string) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("tclapi: marshal params: %w", err)
	}
	out, err := crypter.EncryptBytes(secret, plain, openssl.BytesToKeyMD5)
	if err != nil {
		return "", fmt.Errorf("tclapi: encrypt: %w", err)
	}
	return string(out), nil
}

// Decrypt opens a "Salted__" envelope and returns the plaintext JSON.
func Decrypt(msg, secret string) ([]byte, error) {
	plain, err := crypter.DecryptBytes(secret, []byte(msg), openssl.BytesToKeyMD5)
	if err != nil {
		return nil, fmt.Errorf("tclapi: decrypt: %w", err)
	}
	return plain, nil
}

// XOREncrypt obfuscates the login UserName. Every output rune is < 256.
func XOREncrypt(text string) string {
	k := []rune(XORKey)
	out := make([]rune, 0, 2*len(text))
	for i, c := range []rune(text) {
		key := k[i%len(k)]
		out = append(out, (0xF0&key)|((0x0F&c)^(0x0F&key)))
		out = append(out, (0xF0&key)|((c>>4)^(0x0F&key)))
	}
	return string(out)
}

// FormatStr swaps the two halves of the password hash, mirroring the web UI's
// formatStr() helper. The result is the post-login parameter key.
func FormatStr(hash string) string {
	h := len(hash) / 2
	return hash[h:] + hash[:h]
}

// XORBytes returns XOREncrypt's output as one byte per character, matching
// Python's .encode("latin-1"). Plain []byte(string) would UTF-8 encode runes
// above 0x7F and produce the wrong length.
func XORBytes(text string) []byte {
	runes := []rune(XOREncrypt(text))
	out := make([]byte, len(runes))
	for i, r := range runes {
		out[i] = byte(r)
	}
	return out
}
