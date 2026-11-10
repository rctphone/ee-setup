package ee72

import (
	"os"
	"strings"
	"testing"

	"github.com/rctphone/ee-setup/internal/channel"
)

// TestWriteContainerHarness emits the packed commands and the container-safe
// variant of the script. Container-safe means the kernel-specific remount
// helper is stubbed out; everything else is byte-for-byte the real script.
func TestWriteContainerHarness(t *testing.T) {
	if os.Getenv("EE72_HARNESS") == "" {
		t.Skip("set EE72_HARNESS=1")
	}
	s, err := Script(Options{RootPassword: "oemlinux1", RemoveLock: true})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "/proc/self/attr/fscreate") ||
			strings.Contains(line, "grep ' / ' /proc/mounts") ||
			strings.Contains(line, "grep -q ' / ubifs rw,'") ||
			strings.Contains(line, "/sbin/modprobe") {
			lines = append(lines, ":")
			continue
		}
		lines = append(lines, line)
	}
	variant := strings.Join(lines, "\n")
	cmds := channel.ChunkCommands(variant)

	if err := os.WriteFile("/tmp/ee72test/chunks.sh", []byte(strings.Join(cmds, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/tmp/ee72test/expected.sh", []byte(variant), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("script %d bytes, variant %d bytes, commands %d", len(s), len(variant), len(cmds))
}
