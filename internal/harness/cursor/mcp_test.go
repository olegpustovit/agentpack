package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
)

func TestWriteMCPPreservesNativeFields(t *testing.T) {
	command, kind, off := "server", "stdio", true
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	ctx := base.StageContext{ProjectRoot: t.TempDir(), Mode: mode.ImplicitEffective()}
	projection, err := New().PlanMCP(mcp.Entries{"server": {Server: mcp.Server{Type: &kind, Command: &command, Disabled: &off}}}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.WriteMCPProjection(projection); err != nil {
		t.Fatal(err)
	}
	root, err := stagedRoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config mcp.Config
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	server := config.Servers["server"]
	if *server.Type != "stdio" || server.Disabled == nil || !*server.Disabled {
		t.Fatalf("server = %#v", server)
	}
}
