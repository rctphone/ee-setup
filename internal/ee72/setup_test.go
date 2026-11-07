package ee72

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/GehirnInc/crypt/sha512_crypt"
)

func TestPasswordHashVerifies(t *testing.T) {
	hash, err := PasswordHash("test-password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$6$") {
		t.Fatalf("hash is not SHA-512 crypt: %q", hash)
	}
	if err := sha512_crypt.New().Verify(hash, []byte("test-password")); err != nil {
		t.Fatalf("hash does not verify: %v", err)
	}
	if err := sha512_crypt.New().Verify(hash, []byte("wrong")); err == nil {
		t.Fatal("wrong password verified")
	}
}

func TestScriptContents(t *testing.T) {
	script, err := Script(Options{RootPassword: "test-password", RemoveLock: true}, "192.0.2.19")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"cp -p /systemrw/boot_hsusb_comp",
		"cp -p /etc/shadow",
		"rm -f /jrd-resource/resource/lock_flag",
		"remount rw",
		"remount ro",
		"printf %s 0196 > /systemrw/boot_hsusb_comp",
		"/etc/shadow",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}
}

// TestScriptParses runs the generated script through the host shell in
// syntax-only mode (sh -n). The script targets BusyBox ash, which is close
// enough to dash/bash for syntax checking.
func TestScriptParses(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host")
	}
	script, err := Script(Options{RootPassword: "test-password", RemoveLock: true}, "192.0.2.19")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -n failed: %v\n%s\n---\n%s", err, out, script)
	}
}
