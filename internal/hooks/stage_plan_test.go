package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mode"
)

type planTestRenderer struct{ target harness.Target }

func (renderer planTestRenderer) Target() harness.Target { return renderer.target }
func (planTestRenderer) Output(Event, Result) any        { return nil }
func (renderer planTestRenderer) Render(bundle Bundle, context RenderContext) (RenderOutput, error) {
	output := RenderOutput{}
	for _, hook := range bundle.Hooks {
		if _, err := BuildExecutionSpec(renderer.target, hook, hook.Event, context, &output); err != nil {
			return RenderOutput{}, err
		}
	}
	return output, nil
}

func TestPlanTargetCapturesSupportAndRenderedBytes(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	writeHookFile(t, filepath.Join(source, "hooks", "hooks.json"), nestedCommand("run"))
	writeHookFile(t, filepath.Join(source, "scripts", "run.sh"), "#!/bin/sh\necho run\n")
	if err := os.Chmod(filepath.Join(source, "scripts", "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	origin := Origin{Layer: PackPlugin, Module: "github.com/acme/repo", PackageKey: "pkg", SourceRoot: source, SourceRelative: "hooks/hooks.json", SourceFile: filepath.Join(source, "hooks", "hooks.json")}
	bundle := Bundle{Hooks: []Hook{{Event: Stop, Handler: Handler{Kind: CommandHandler, Command: "run"}, Origin: origin}}}
	plan, err := PlanTarget(bundle, harness.Cursor, planTestRenderer{harness.Cursor}, RenderContext{ProjectRoot: t.TempDir(), TargetRoot: target}, mode.ImplicitEffective())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 3 || plan.Files[0].Source != origin.SourceFile || plan.Files[0].Module != origin.Module {
		t.Fatalf("planned files = %#v", plan.Files)
	}
	if err := ApplyTargetPlan(plan); err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.Files {
		actual, err := os.ReadFile(file.Path)
		if err != nil || string(actual) != string(file.Contents) {
			t.Errorf("%s differs from plan: err=%v", file.Path, err)
		}
		// A hook command names its script by path, so it must stay executable.
		if info, err := os.Stat(file.Path); runtime.GOOS != "windows" && (err != nil || (info.Mode().Perm()&0o111 != 0) != (filepath.Base(file.Path) == "run.sh")) {
			t.Errorf("%s mode = %v, %v", file.Path, info.Mode(), err)
		}
	}
}

func TestApplyTargetPlanRejectsChangedHookOrigin(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	originFile := filepath.Join(source, "hooks", "hooks.json")
	writeHookFile(t, originFile, nestedCommand("run"))
	origin := Origin{Layer: PackPlugin, Module: "github.com/acme/repo", PackageKey: "pkg", SourceRoot: source, SourceRelative: "hooks/hooks.json", SourceFile: originFile}
	bundle := Bundle{Hooks: []Hook{{Event: Stop, Handler: Handler{Kind: CommandHandler, Command: "run"}, Origin: origin}}}
	plan, err := PlanTarget(bundle, harness.Cursor, planTestRenderer{harness.Cursor}, RenderContext{ProjectRoot: t.TempDir(), TargetRoot: target}, mode.ImplicitEffective())
	if err != nil {
		t.Fatal(err)
	}
	writeHookFile(t, originFile, nestedCommand("changed"))
	if err := ApplyTargetPlan(plan); err == nil {
		t.Fatal("changed hook origin was applied")
	}
	if _, err := os.Stat(filepath.Join(target, "hooks", "pkg", "package", "hooks", "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("partial output exists: %v", err)
	}
}
