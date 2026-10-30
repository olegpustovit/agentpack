package omp

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/hooks"
)

func TestHookRendererMapsLifecycleAndReportsPermissionDegradation(t *testing.T) {
	hook := hooks.Hook{Event: hooks.PreToolUse, Matcher: "Bash", Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "check"}, Origin: hooks.Origin{PackageKey: "pkg", SourceFile: "hooks.json"}}
	output, err := (HookRenderer{}).Render(hooks.Bundle{Hooks: []hooks.Hook{hook}}, hooks.RenderContext{TargetRoot: t.TempDir(), StagedPackages: map[string]string{"pkg": t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if output.Summary.Emulated != 1 || len(output.Files) != 3 {
		t.Fatalf("output: %#v", output)
	}
	hook.Event = hooks.PermissionRequest
	output, err = (HookRenderer{}).Render(hooks.Bundle{Hooks: []hooks.Hook{hook}}, hooks.RenderContext{TargetRoot: t.TempDir(), StagedPackages: map[string]string{"pkg": t.TempDir()}})
	if err != nil || output.Summary.Degraded != 1 {
		t.Fatalf("permission hook: %#v %v", output, err)
	}
	hook.Event, hook.Matcher = hooks.PreCompact, "auto"
	output, err = (HookRenderer{}).Render(hooks.Bundle{Hooks: []hooks.Hook{hook}}, hooks.RenderContext{})
	if err != nil || output.Summary.Omitted != 1 || len(output.Files) != 0 {
		t.Fatalf("unavailable lifecycle matcher must be omitted: %#v %v", output, err)
	}

}

func TestHookBridgeExecutesResults(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not installed")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hooks.js"), []byte(hookSource), 0o600); err != nil {
		t.Fatal(err)
	}
	config := `{"agentpack":"unused","hooks":[{"event":"tool_call","canonicalEvent":"PreToolUse","kind":"command","specPath":"tool","matcher":"Bash|Read.*"},{"event":"input","canonicalEvent":"UserPromptSubmit","kind":"command","specPath":"input","matcher":""},{"event":"tool_result","canonicalEvent":"PostToolUse","kind":"command","specPath":"post","matcher":""},{"event":"session_stop","canonicalEvent":"Stop","kind":"command","specPath":"stop","matcher":""},{"event":"session_start","canonicalEvent":"SessionStart","kind":"command","specPath":"start","matcher":""}]}`
	if err := os.WriteFile(filepath.Join(root, "agentpack.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	check, err := os.ReadFile("hooks_check.js")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "check.js")
	if err := os.WriteFile(script, check, 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(bun, script).CombinedOutput(); err != nil {
		t.Fatalf("bridge: %v\n%s", err, output)
	}
}

func TestLSPProjectionUsesPluginFile(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	ctx := base.StageContext{ProjectRoot: t.TempDir()}
	projection, err := planLSP(nil, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Files) != 1 || filepath.Base(projection.Files[0].Path) != ".lsp.json" {
		t.Fatalf("projection: %#v", projection)
	}
}
