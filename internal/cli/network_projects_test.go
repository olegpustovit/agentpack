package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/lockfile"
)

func TestProjectContentsOmitsMCPCommandsAndSecrets(t *testing.T) {
	locked := lockfile.PackLock{
		MCPServers: []lockfile.MCPServer{{
			Name:      "filesystem",
			Source:    "manifest",
			Launcher:  "npm",
			Status:    "pinned",
			Package:   "@modelcontextprotocol/server-filesystem",
			Version:   "1.2.3",
			Command:   "npx",
			Requested: "@modelcontextprotocol/server-filesystem --token=s3cret",
		}},
	}
	data, err := json.Marshal(projectContents(locked))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, leaked := range []string{`"npx"`, "s3cret", `"command"`, `"requested"`, `"args"`, `"env"`, `"headers"`} {
		if strings.Contains(text, leaked) {
			t.Fatalf("project contents leaked %s: %s", leaked, text)
		}
	}
	if !strings.Contains(text, `"package":"@modelcontextprotocol/server-filesystem"`) {
		t.Fatalf("package name must stay visible: %s", text)
	}
}
