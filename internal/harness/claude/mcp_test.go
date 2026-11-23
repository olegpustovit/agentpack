package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
)

func TestWriteMCPFillsRemoteTypeAndDropsDisabled(t *testing.T) {
	url, off := "https://example.test/mcp", true
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	ctx := base.StageContext{ProjectRoot: t.TempDir(), Mode: mode.ImplicitEffective()}
	projection, err := New().PlanMCP(mcp.Entries{"remote": {Server: mcp.Server{URL: &url}}, "off": {Server: mcp.Server{Disabled: &off}}}, ctx)
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
	data, err := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config mcp.Config
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if *config.Servers["remote"].Type != "http" {
		t.Fatalf("remote = %#v", config.Servers["remote"])
	}
	if _, exists := config.Servers["off"]; exists {
		t.Fatal("disabled server was written")
	}
}
