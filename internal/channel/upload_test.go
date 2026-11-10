package channel

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestChunkCommands(t *testing.T) {
	script := "#!/bin/sh\nset -e\n" + strings.Repeat("echo line\n", 200)

	cmds := ChunkCommands(script)
	if cmds[0] != "rm -f /tmp/e" {
		t.Fatalf("first command = %q", cmds[0])
	}
	if cmds[len(cmds)-2] != "base64 -d /tmp/e>/tmp/s" || cmds[len(cmds)-1] != "sh /tmp/s" {
		t.Fatalf("unexpected tail: %q", cmds[len(cmds)-2:])
	}

	for _, c := range cmds {
		if len(c) > MaxCommand {
			t.Fatalf("command is %d bytes, limit %d: %q", len(c), MaxCommand, c)
		}
		if strings.ContainsAny(c, blocked) {
			t.Fatalf("command has a blocked character: %q", c)
		}
		if _, err := ParameterValue(c); err != nil {
			t.Fatalf("ParameterValue rejected %q: %v", c, err)
		}
	}

	var b64 strings.Builder
	for _, c := range cmds[1 : len(cmds)-2] {
		b64.WriteString(strings.TrimSuffix(strings.TrimPrefix(c, "echo "), ">>/tmp/e"))
	}
	got, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != script {
		t.Fatalf("reassembled script differs: %d bytes vs %d", len(got), len(script))
	}
}
