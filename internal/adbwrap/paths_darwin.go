//go:build darwin

package adbwrap

import "path/filepath"

func platformCandidates(home string) []string {
	return []string{
		filepath.Join(home, "Library/Android/sdk/platform-tools/adb"),
		"/opt/homebrew/bin/adb",
		"/usr/local/bin/adb",
	}
}
