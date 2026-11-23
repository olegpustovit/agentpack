package codex

import (
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/mcp"
)

func TestMergeMCPPreservesUserAndWritesDisabled(t *testing.T) {
	seed := []byte("[mcp_servers.user]\ncommand = \"user\"\n")
	command, off := "pack", true
	data, _, _, err := MergeMCPData(seed, mcp.Entries{"user": {Server: mcp.Server{Command: &command}}, "pack": {Server: mcp.Server{Command: &command, Disabled: &off}}})
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	if !strings.Contains(output, `command = 'user'`) && !strings.Contains(output, `command = "user"`) {
		t.Fatalf("user missing: %s", output)
	}
	if !strings.Contains(output, "enabled = false") {
		t.Fatalf("disabled missing: %s", output)
	}
}
