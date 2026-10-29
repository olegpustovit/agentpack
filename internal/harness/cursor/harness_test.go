package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
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
