package registry

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
)

func TestStagedMCPGolden(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	project := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "1")
	t.Setenv("OPENCODE_PATH", filepath.Join(home, "missing-opencode"))
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "1")

	tomlSeed := "[mcp_servers.shadowed]\ncommand = 'user-server'\n"
	for path, contents := range map[string]string{
		filepath.Join(home, ".codex", "config.toml"):    tomlSeed,
		filepath.Join(home, ".grok", "config.toml"):     tomlSeed,
		filepath.Join(home, ".cursor", "mcp.json"):      `{"mcpServers":{"shadowed":{"command":"user-server"}}}`,
		filepath.Join(xdg, "opencode", "opencode.json"): `{"mcp":{"shadowed":{"type":"local","command":["user-server"]}}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	command, cwd, stdio := "mcp-fixture", "fixture-dir", "stdio"
	remote, oauthURL, sse := "https://example.test/mcp", "https://example.test/oauth", "sse"
	off, on := true, false
	entries := mcp.Entries{
		"local": {Source: mcp.Plugin, Module: "github.com/example/fixture", Server: mcp.Server{
			Command: &command, Type: &stdio, Cwd: &cwd, Args: []string{"--stdio"}, Env: map[string]string{"FIXTURE": "value"}, Disabled: &on,
		}},
		"remote": {Source: mcp.Manifest, Server: mcp.Server{URL: &remote, Headers: map[string]string{"X-Fixture": "value"}}},
		"oauth": {Source: mcp.DotAgents, Server: mcp.Server{URL: &oauthURL, Type: &sse, OAuth: &mcp.OAuth{
			ClientID: "fixture-client", ClientSecret: "fixture-secret", CallbackPort: 8739,
			Scopes: []string{"read", "write"}, MetadataURL: "https://example.test/metadata",
		}}},
		"disabled": {Source: mcp.Manifest, Server: mcp.Server{Command: &command, Disabled: &off}},
		"shadowed": {Source: mcp.Manifest, Server: mcp.Server{Command: &command}},
	}

	check := func(t *testing.T, target base.Target, golden ...string) {
		t.Helper()
		h, err := ByTarget(target)
		if err != nil {
			t.Fatal(err)
		}
		ctx := base.StageContext{ProjectRoot: project, WorkspaceRoot: project, Mode: mode.ImplicitEffective()}
		projection, err := h.PlanMCP(entries, ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(projection.Files) != len(golden) {
			t.Fatalf("got %d projected files, want %d", len(projection.Files), len(golden))
		}
		if err := base.WriteMCPProjection(projection); err != nil {
			t.Fatal(err)
		}
		for i, name := range golden {
			got, err := os.ReadFile(projection.Files[i].Path)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", "mcp", name)
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs\ngot:\n%s\nwant:\n%s", name, got, want)
			}
		}
	}
	for _, test := range []struct {
		target base.Target
		files  []string
	}{
		{base.Claude, []string{"claude.json"}},
		{base.Codex, []string{"codex.toml"}},
		{base.Cursor, []string{"cursor.json", "cursor-home.json"}},
		{base.Grok, []string{"grok.toml"}},
		{base.Agy, []string{"agy.json"}},
		{base.OpenCode, []string{"opencode.json"}},
		{base.OMP, []string{"omp.json"}},
	} {
		t.Run(string(test.target), func(t *testing.T) { check(t, test.target, test.files...) })
	}
	t.Run("opencode-native", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("shell version fixture")
		}
		binary := filepath.Join(t.TempDir(), "opencode")
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '2.0.0\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OPENCODE_PATH", binary)
		check(t, base.OpenCode, "opencode-native.json")
	})
}
