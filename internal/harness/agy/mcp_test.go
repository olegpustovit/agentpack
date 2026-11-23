package agy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
)

func TestWriteMCPUsesServerURL(t *testing.T) {
	url := "https://example.test/mcp"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	ctx := base.StageContext{ProjectRoot: t.TempDir(), Mode: mode.ImplicitEffective()}
	projection, err := New().PlanMCP(mcp.Entries{"remote": {Server: mcp.Server{URL: &url}}}, ctx)
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
	data, err := os.ReadFile(filepath.Join(root, "mcp_config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"serverUrl": "https://example.test/mcp"`) {
		t.Fatalf("output = %s", data)
	}
}
