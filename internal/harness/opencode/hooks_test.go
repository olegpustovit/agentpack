package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/hooks"
)

func TestRendererGeneratesBridgeAndMergesConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(`{"plugin":["existing.js"],"theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := hooks.Origin{Module: "module", PackageKey: "pkg", SourceFile: "hooks.json"}
	bundle := hooks.Bundle{Hooks: []hooks.Hook{
		{Event: hooks.PreToolUse, Matcher: "Bash", Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "check"}, Origin: origin},
		{Event: hooks.UserPromptSubmit, Handler: hooks.Handler{Kind: hooks.PromptHandler, Prompt: "guide"}, Origin: origin},
		{Event: hooks.Stop, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "stop"}, Origin: origin},
	}}
	output, err := (HookRenderer{}).Render(bundle, hooks.RenderContext{TargetRoot: root, StagedPackages: map[string]string{"pkg": t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Files) != 5 || output.Summary.Native != 1 || output.Summary.Degraded != 1 || output.Summary.Omitted != 1 {
		t.Fatalf("output = %#v", output)
	}
	for _, file := range output.Files {
		if strings.HasPrefix(file.Path, filepath.Join(root, "plugins")+string(filepath.Separator)) {
			t.Fatalf("generated hook would write into the user's plugin directory: %s", file.Path)
		}
	}
	config := output.Files[len(output.Files)-1].JSON.(map[string]any)
	plugins := config["plugin"].([]any)
	if len(plugins) != 2 || config["theme"] != "dark" || !strings.Contains(pluginSource, "tool.execute.before") {
		t.Fatalf("config = %#v", config)
	}
}

func TestMergedConfigPreservesPluginOptionsAndJSONC(t *testing.T) {
	root := t.TempDir()
	input := `{"plugin": [["local-plugin", {"enabled": true}], "./agentpack-hooks/index.js"], // keep options
	"provider": {"example": {"options": {"baseURL": "http://localhost"}}},}`
	if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := mergedConfig(root, "./agentpack-hooks/index.js")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(config["plugin"].([]any)) != 2 || !strings.Contains(string(data), `"enabled":true`) || !strings.Contains(string(data), `"baseURL":"http://localhost"`) {
		t.Fatalf("config = %s", data)
	}
}

func TestUnsupportedStrictEventFails(t *testing.T) {
	t.Parallel()
	hook := hooks.Hook{Event: hooks.PreToolUse, Handler: hooks.Handler{Kind: hooks.CommandHandler}, Origin: hooks.Origin{SourceFile: "hook"}}
	// Exercise the common strict failure directly with the OpenCode target semantics.
	if _, err := hooks.CheckSupport((HookRenderer{}).Target(), hook, hooks.Support{Kind: hooks.Unsupported, Reason: "missing"}, &hooks.RenderOutput{}, "", ""); err == nil {
		t.Fatal("expected strict error")
	}
}
