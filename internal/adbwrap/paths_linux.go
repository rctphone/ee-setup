//go:build linux

package adbwrap

import "path/filepath"

func platformCandidates(home string) []string {
	return []string{
		filepath.Join(home, "Android/Sdk/platform-tools/adb"),
		"/usr/bin/adb",
		"/usr/local/bin/adb",
	}
}
