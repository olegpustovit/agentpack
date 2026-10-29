package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func TestPrepareFinalizeBuildsFakeHomeAndMergesUserMCP(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	real := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "mcp.json"), []byte(`{"mcpServers":{"same":{"command":"user"},"user":{"command":"user"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	h := New()
	if err := h.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	packCommand := "pack"
	if err := h.WriteMCP(mcp.Entries{"same": {Server: mcp.Server{Command: &packCommand}}, "pack": {Server: mcp.Server{Command: &packCommand}}}, ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.Finalize(nil, ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	fake, _ := paths.StagingCursorHomeDirForMode(project, "default")
	data, err := os.ReadFile(filepath.Join(fake, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config mcp.Config
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if *config.Servers["same"].Command != "user" || *config.Servers["pack"].Command != "pack" {
		t.Fatalf("mcp=%#v", config.Servers)
	}
	cli, err := os.ReadFile(filepath.Join(fake, ".cursor", "cli-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(cli, &value); err != nil {
		t.Fatal(err)
	}
	if value["attribution"].(map[string]any)["attributeCommitsToAgent"] != false {
		t.Fatalf("cli=%#v", value)
	}
}

func TestFakeHomeSharesArtLoginAcrossRebuilds(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	ctx := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	h := New()
	if err := h.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	fake, err := paths.StagingCursorHomeDirForMode(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Finalize(nil, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(fake, ".artifacts")); !os.IsNotExist(err) {
		t.Fatalf("missing login: %v", err)
	}
	art := filepath.Join(home, ".artifacts")
	if err := os.Mkdir(art, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(art, "config")
	if err := os.WriteFile(config, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A login created after staging must be visible on the next fast launch.
	t.Setenv("CURSOR_AGENT_PATH", os.Args[0])
	if _, err := launch(base.LaunchContext{ProjectRoot: project, Mode: ctx.Mode}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(fake, ".artifacts", "config")); err != nil || string(data) != "fixture" {
		t.Fatalf("late login = %q, %v", data, err)
	}
	for range 2 {
		if err := h.Finalize(nil, ctx); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fake, ".artifacts", "config"), []byte("updated"), 0o600); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(config)
		if err != nil || string(data) != "updated" {
			t.Fatalf("shared config = %q, %v", data, err)
		}
	}
	info, err := os.Stat(config)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %v, %v", info, err)
	}
}

func TestMergeHooksUserBeforePack(t *testing.T) {
	root := t.TempDir()
	user, pack, dest := filepath.Join(root, "user.json"), filepath.Join(root, "pack.json"), filepath.Join(root, "dest.json")
	if err := os.WriteFile(user, []byte("{/* user hooks */\"hooks\":{\"beforeSubmitPrompt\":[{\"command\":\"user\"},],},}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pack, []byte(`{"hooks":{"beforeSubmitPrompt":[{"command":"pack"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeHookFiles(pack, user, dest); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(dest)
	var rootValue struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &rootValue); err != nil {
		t.Fatal(err)
	}
	entries := rootValue.Hooks["beforeSubmitPrompt"]
	if entries[0]["command"] != "user" || entries[1]["command"] != "pack" {
		t.Fatalf("hooks=%#v", entries)
	}
}

func TestUserSkillsSeedFakeHomeBeforePackOverlay(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	user := filepath.Join(home, ".cursor", "skills")
	for name, body := range map[string]string{"personal/SKILL.md": "personal skill", "personal/scripts/run.sh": "#!/bin/sh\n", "shared/SKILL.md": "user version"} {
		path := filepath.Join(user, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	h := New()
	if err := h.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	pack, err := h.StagedRoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pack, "skills", "shared", "SKILL.md"), []byte("pack version"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.Finalize(nil, ctx); err != nil {
		t.Fatal(err)
	}
	fake, err := paths.StagingCursorHomeDirForMode(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	for name, expected := range map[string]string{"personal/SKILL.md": "personal skill", "personal/scripts/run.sh": "#!/bin/sh\n", "shared/SKILL.md": "pack version"} {
		data, err := os.ReadFile(filepath.Join(fake, ".cursor", "skills", name))
		if err != nil || string(data) != expected {
			t.Fatalf("fake skill %s = %q, %v", name, data, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(user, "shared", "SKILL.md"))
	if err != nil || string(data) != "user version" {
		t.Fatalf("user skill modified: %q, %v", data, err)
	}
}

func TestShareArtLoginDoesNotOverwriteExistingDestination(t *testing.T) {
	home, fake := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(fake, ".artifacts")
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := shareArtLogin(home, fake); err == nil {
		t.Fatal("expected conflict")
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "keep" {
		t.Fatalf("destination changed: %q, %v", data, err)
	}
}
