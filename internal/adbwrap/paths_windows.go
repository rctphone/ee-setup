//go:build windows

package adbwrap

import (
	"os"
	"path/filepath"
)

func platformCandidates(home string) []string {
	var out []string
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		out = append(out, filepath.Join(la, "Android/Sdk/platform-tools/adb.exe"))
	}
	return append(out, filepath.Join(home, "AppData/Local/Android/Sdk/platform-tools/adb.exe"))
}
