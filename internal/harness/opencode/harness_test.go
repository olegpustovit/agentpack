package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mode"
)

func TestMergeV2InlineSkills(t *testing.T) {
	for _, skills := range []any{
		[]any{"user-skill"},
		map[string]any{"paths": []any{"user-skill"}, "urls": []any{"https://example.test/skills"}},
	} {
		pack := map[string]any{"skills": map[string]any{"paths": []any{"pack-skill"}}}
		user := map[string]any{"skills": skills}
		mergeSkills(pack, user)
		mergeInline(pack, user)
		data, err := json.Marshal(pack)
		if err != nil || !strings.Contains(string(data), "pack-skill") || !strings.Contains(string(data), "user-skill") {
			t.Fatalf("skill sources = %s, %v", data, err)
		}
	}
}

func TestPrepareSeedsConfigAndAttributionIdempotently(t *testing.T) {
	project, stagingRoot, home := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("AGENTPACK_STAGING_ROOT", stagingRoot)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("OPENCODE_PATH", filepath.Join(home, "no-opencode"))
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "")
	userRoot := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userRoot, "opencode.json"), []byte(`{"provider":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	harness := New()
	if err := harness.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := harness.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	root, _ := harness.StagedRoot(ctx)
	data, _ := os.ReadFile(filepath.Join(root, "opencode.json"))
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if (nativeMajor() == 1 && config["provider"] != nil || nativeMajor() != 1 && config["provider"] != "test") || len(config["instructions"].([]any)) != 1 {
		t.Fatalf("config = %#v", config)
	}
	if _, err := os.Stat(filepath.Join(root, instructionsFile)); err != nil {
		t.Fatal(err)
	}
}

func TestYoloDoesNotPersistAndPreservesInlineConfig(t *testing.T) {
	project := t.TempDir()
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("OPENCODE_PATH", os.Args[0])
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"username":"fixture", "permission":"deny", "seed":9007199254740993}`)
	ctx := base.LaunchContext{ProjectRoot: project, Mode: mode.ImplicitEffective(), Yolo: true}
	root, err := stagedRoot(base.StageContext{ProjectRoot: project, Mode: ctx.Mode})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "opencode.json")
	const staged = `{"permission":"ask"}`
	if err := os.WriteFile(path, []byte(staged), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd, err := launch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var content string
	for _, entry := range cmd.Environ() {
		if strings.HasPrefix(entry, "OPENCODE_CONFIG_CONTENT=") {
			content = strings.TrimPrefix(entry, "OPENCODE_CONFIG_CONTENT=")
		}
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(content), &config); err != nil || config["username"] != "fixture" || config["permission"] != "allow" {
		t.Fatalf("inline config = %q, %v", content, err)
	}
	if !strings.Contains(content, `"seed":9007199254740993`) {
		t.Fatalf("inline integer lost precision: %s", content)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != staged {
		t.Fatalf("staged config = %q, %v", data, err)
	}
	ctx.Yolo = false
	cmd, err = launch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range cmd.Environ() {
		if strings.HasPrefix(entry, "OPENCODE_CONFIG_CONTENT=") && entry != `OPENCODE_CONFIG_CONTENT={"username":"fixture", "permission":"deny", "seed":9007199254740993}` {
			t.Fatalf("normal launch inherited yolo: %s", entry)
		}
	}
}

func TestYoloOverridesNativePermissionRules(t *testing.T) {
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("OPENCODE_PATH", os.Args[0])
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"permissions":[{"action":"*","resource":"*","effect":"deny"}]}`)
	cmd, err := launch(base.LaunchContext{ProjectRoot: t.TempDir(), Mode: mode.ImplicitEffective(), Yolo: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range cmd.Environ() {
		if content, found := strings.CutPrefix(entry, "OPENCODE_CONFIG_CONTENT="); found {
			var config struct {
				Permissions []struct {
					Effect string `json:"effect"`
				} `json:"permissions"`
			}
			if err := json.Unmarshal([]byte(content), &config); err != nil || len(config.Permissions) != 1 || config.Permissions[0].Effect != "allow" {
				t.Fatalf("inline config = %q, %v", content, err)
			}
			return
		}
	}
	t.Fatal("missing inline config")
}

func TestStagesLSPReadsTheLspMap(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "opencode.json")
	for text, want := range map[string]bool{`{"lsp":{"x":{"command":["x"]}}}`: true, `{"lsp":{}}`: false, `{"mcp":{}}`: false} {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := stagesLSP(path); got != want {
			t.Errorf("stagesLSP(%s) = %v", text, got)
		}
	}
}

func TestNativeVersionFollowsBinaryReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	binary := filepath.Join(t.TempDir(), "opencode")
	t.Setenv("OPENCODE_PATH", binary)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '1.17.11\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if nativeMajor() != 1 {
		t.Fatal("OpenCode 1 was not detected")
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'opencode v2.2.2\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if nativeMajor() != 2 {
		t.Fatal("replaced OpenCode 2 binary still detected as version 1")
	}
}

func TestPrepareV2UsesCallerPluginDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	home, project, custom := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("OPENCODE_CONFIG_DIR", custom)
	binary := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'opencode v2.2.2\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_PATH", binary)
	plugins := filepath.Join(custom, "plugins")
	if err := os.Mkdir(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugins, "user.js"), []byte("user plugin"), 0o644); err != nil {
		t.Fatal(err)
	}
	const userConfig = "{ // caller config\n \"username\": \"custom-user\",\n}\n"
	if err := os.WriteFile(filepath.Join(custom, "opencode.jsonc"), []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	if err := prepare(ctx); err != nil {
		t.Fatal(err)
	}
	command, err := launch(base.LaunchContext{ProjectRoot: project, Mode: ctx.Mode})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(command.Env, "OPENCODE_CONFIG_DIR="+custom) {
		t.Fatal("launch does not preserve the caller's config directory")
	}
	data, err := os.ReadFile(filepath.Join(custom, "opencode.jsonc"))
	if err != nil || string(data) != userConfig {
		t.Fatalf("user JSONC modified: %q, %v", data, err)
	}
	entries, err := os.ReadDir(plugins)
	if err != nil || len(entries) != 1 || entries[0].Name() != "user.js" {
		t.Fatalf("user plugin directory changed: %v, %v", entries, err)
	}
}

func TestPrepareRejectsConfigOverlappingStaging(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	binary := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'opencode v2.2.2\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_PATH", binary)
	ctx := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	root, err := stagedRoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	userPlugin := filepath.Join(root, "plugins", "personal.js")
	if err := os.MkdirAll(filepath.Dir(userPlugin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPlugin, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_CONFIG_DIR", root)
	if err := prepare(ctx); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlapping config accepted: %v", err)
	}
	data, err := os.ReadFile(userPlugin)
	if err != nil || string(data) != "keep" {
		t.Fatalf("user plugin changed: %q, %v", data, err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '1.17.11\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(root))
	if err := prepare(ctx); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlapping global config accepted: %v", err)
	}
	data, err = os.ReadFile(userPlugin)
	if err != nil || string(data) != "keep" {
		t.Fatalf("user plugin changed by v1 preparation: %q, %v", data, err)
	}
}

func TestProtectInlineVariables(t *testing.T) {
	input := `{ "literal": "{env:KEEP}", // {env:COMMENT}
 "enabled": {env:ENABLED}, "file": {file:settings.json}, "marker": "__agentpack_inline_variable_0__" }`
	protected, replacements := protectInlineVariables(input, `{"pack":"__agentpack_inline_variable_1__"}`)
	if len(replacements) != 2 || !strings.Contains(protected, `"{env:KEEP}"`) || !strings.Contains(protected, `// {env:COMMENT}`) {
		t.Fatalf("protected = %q, replacements = %v", protected, replacements)
	}
	if _, collides := replacements["__agentpack_inline_variable_1__"]; collides {
		t.Fatal("placeholder collides with generated pack config")
	}
	for key, value := range replacements {
		quoted, _ := json.Marshal(key)
		protected = strings.ReplaceAll(protected, string(quoted), value)
	}
	if protected != input {
		t.Fatalf("round trip = %q", protected)
	}
}

func TestUnsupportedMajorIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	binary := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '3.0.0\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_PATH", binary)
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	ctx := base.StageContext{ProjectRoot: t.TempDir(), Mode: mode.ImplicitEffective()}
	if err := prepare(ctx); err == nil || !strings.Contains(err.Error(), "unsupported OpenCode major") {
		t.Fatalf("prepare error = %v", err)
	}
	if _, err := launch(base.LaunchContext{ProjectRoot: ctx.ProjectRoot, Mode: ctx.Mode}); err == nil || !strings.Contains(err.Error(), "unsupported OpenCode major") {
		t.Fatalf("launch error = %v", err)
	}
}

func TestLaunchRejectsNullGeneratedConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	binary := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '2.2.2\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_PATH", binary)
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	ctx := base.LaunchContext{ProjectRoot: t.TempDir(), Mode: mode.ImplicitEffective()}
	root, err := stagedRoot(base.StageContext{ProjectRoot: ctx.ProjectRoot, Mode: ctx.Mode})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packConfigPath(root), []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := launch(ctx); err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("launch error = %v", err)
	}
}
