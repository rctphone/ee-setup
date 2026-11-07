//go:build darwin

package adbwrap

import (
	"os"
	"path/filepath"
)

func platformCandidates(home string) []string {
	// sudo sets HOME=/var/root; the SDK belongs to the invoking user.
	if u := os.Getenv("SUDO_USER"); u != "" && u != "root" {
		home = filepath.Join("/Users", u)
	}
	return []string{
		filepath.Join(home, "Library/Android/sdk/platform-tools/adb"),
		"/opt/homebrew/bin/adb",
		"/usr/local/bin/adb",
	}
}
