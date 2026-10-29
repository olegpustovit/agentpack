package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
)

func TestNativeServerNamesAndProjection(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"lsp":false}`)
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "1")
	global := filepath.Join(home, "config", "opencode", "opencode.jsonc")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{"mcp":{"global":{"type":"local","command":["user"],"environment":{"TOKEN":"fixture-secret"}}},"lsp":{"user":false},}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "opencode.json"), []byte(`{"mcp":{"project":{"type":"local","command":["project"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := base.StageContext{ProjectRoot: workspace, WorkspaceRoot: workspace, Mode: mode.ImplicitEffective()}
	user, err := nativeServerNames(ctx)
	if err != nil || !user.mcp["global"] || !user.mcp["project"] || !user.lsp["user"] || !user.lspDisabled {
		t.Fatalf("native scopes = %+v, %v", user, err)
	}
	command := "pack"
	entries := mcp.Entries{"global": {Server: mcp.Server{Command: &command}}, "project": {Server: mcp.Server{Command: &command}}, "pack": {Server: mcp.Server{Command: &command}}}
	root := t.TempDir()
	projection, err := planNativeMCP(entries, ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCP map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal(projection.Files[0].Contents, &config); err != nil || len(config.MCP) != 1 || config.MCP["pack"] == nil {
		t.Fatalf("pack MCP config = %s, %v", projection.Files[0].Contents, err)
	}
	lsp, err := planNativeLSP(map[string]json.RawMessage{"pack": json.RawMessage(`{"command":"server"}`)}, ctx, root)
	if err != nil || len(lsp.Files) != 0 || len(lsp.Omitted) != 1 || len(lsp.Warnings) != 1 {
		t.Fatalf("disabled LSP projection = %+v, %v", lsp, err)
	}
}

func TestNativeServerNamesExpandVariablesAndTrackInputs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "")
	t.Setenv("AGENTPACK_TEST_SERVER_NAME", "first")
	t.Setenv("AGENTPACK_TEST_TIMEOUT", "123")
	binary := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '1.17.11\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_PATH", binary)
	configDir := filepath.Join(home, "config", "opencode")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"timeout":{env:AGENTPACK_TEST_TIMEOUT},"mcp":{"{env:AGENTPACK_TEST_SERVER_NAME}":{"type":"local","command":["{file:command.txt}"]}}}`
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(configDir, "command.txt")
	if err := os.WriteFile(file, []byte("fixture-command"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := base.StageContext{ProjectRoot: workspace, WorkspaceRoot: workspace}
	names, err := nativeServerNames(ctx)
	if err != nil || !names.mcp["first"] {
		t.Fatalf("expanded names = %+v, %v", names.mcp, err)
	}
	first, err := NativeConfigFingerprint(workspace, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTPACK_TEST_SERVER_NAME", "second")
	second, err := NativeConfigFingerprint(workspace, workspace)
	if err != nil || first == second {
		t.Fatalf("environment change ignored: %v", err)
	}
	if err := os.WriteFile(file, []byte("changed-command"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := NativeConfigFingerprint(workspace, workspace)
	if err != nil || second == third {
		t.Fatalf("file reference change ignored: %v", err)
	}
}

func TestV2NativeServerNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "")
	binary := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '2.2.2\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_PATH", binary)
	custom := filepath.Join(home, "custom")
	t.Setenv("OPENCODE_CONFIG_DIR", custom)
	roots, err := NativeUserConfigRoots()
	if err != nil || len(roots) != 1 || roots[0] != custom {
		t.Fatalf("v2 config roots = %v, %v", roots, err)
	}
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	root := filepath.Join(home, "config", "opencode")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(`{"mcp":{"servers":{"native":{"type":"local"},"type":{"type":"local"}}},"lsp":{"user":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := nativeServerNames(base.StageContext{ProjectRoot: workspace})
	if err != nil || !result.mcp["native"] || !result.mcp["type"] || !result.lsp["user"] || result.mcp["servers"] {
		t.Fatalf("native v2 names = %+v, %v", result, err)
	}
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(`{"mcp":{"servers":{"type":"local","command":["server"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = nativeServerNames(base.StageContext{ProjectRoot: workspace})
	if err != nil || !result.mcp["servers"] {
		t.Fatalf("legacy server named servers = %+v, %v", result, err)
	}
}
