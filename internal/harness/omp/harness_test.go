package omp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
)

func TestBundleAndLaunch(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("OMP_PATH", os.Args[0])
	ctx := base.StageContext{ProjectRoot: t.TempDir()}
	h := New()
	if err := h.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	root, err := h.StagedRoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := h.LaunchCommand(base.LaunchContext{ProjectRoot: ctx.ProjectRoot, Yolo: true, Arguments: []string{"--profile", "work", "--", "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{os.Args[0], "--plugin-dir", root, "--yolo", "--profile", "work", "--", "hello"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("args = %q, want %q", cmd.Args, want)
	}
	if cmd.Env != nil {
		t.Fatalf("native profile environment overridden: %q", cmd.Env)
	}
	if _, err := os.Stat(filepath.Join(root, "rules", "agentpack-guidance.md")); err != nil {
		t.Fatal(err)
	}
}

func TestMCPPreservesCwdDisabledAndOAuth(t *testing.T) {
	command, cwd, url := "server", "/workspace", "https://example.com/mcp"
	disabled := true
	data, warnings, err := RenderMCP(mcp.Entries{
		"local":  {Server: mcp.Server{Command: &command, Cwd: &cwd, Disabled: &disabled}},
		"remote": {Server: mcp.Server{URL: &url, OAuth: &mcp.OAuth{ClientID: "id", ClientSecret: "secret", Scopes: []string{"read", "write"}, CallbackPort: 1234}}},
	})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("render: %v, %v", err, warnings)
	}
	var config struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	local := config.Servers["local"]
	if local["cwd"] != cwd || local["enabled"] != false || local["disabled"] != nil {
		t.Fatalf("local: %v", local)
	}
	remote := config.Servers["remote"]
	oauth := remote["oauth"].(map[string]any)
	if remote["type"] != "http" || oauth["scope"] != "read write" || oauth["clientSecret"] != "secret" {
		t.Fatalf("remote: %v", remote)
	}
}
