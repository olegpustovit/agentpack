package staging

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mode"
)

func TestPackagePlanPublishesItsArtifactAndSupportBytes(t *testing.T) {
	source, claudeRoot, codexRoot := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, source, map[string]string{
		"skills/review/SKILL.md":            "---\nname: review\ndescription: Review\n---\nSkill body\n",
		"skills/review/references/guide.md": "Reference body\n",
		"skills/review/scripts/check.sh":    "#!/bin/sh\nexit 0\n",
		"commands/review.md":                "---\ndescription: Check\n---\nCommand body\n",
		"rules/guard.mdc":                   "---\ndescription: Guard\nglobs: [\"**/*.go\"]\n---\nRule body\n",
		"agents/team/reviewer.md":           "---\ndescription: Team reviewer\n---\nTeam body\n",
		"agents/ops/reviewer.md":            "---\ndescription: Ops reviewer\n---\nOps body\n",
		"core/support.txt":                  "Plugin support\n",
	})
	if err := os.Chmod(filepath.Join(source, "core", "support.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(source, "skills", "review", "scripts", "check.sh"), 0o744); err != nil {
		t.Fatal(err)
	}
	roots := []HarnessRoot{{Target: harness.Claude, Path: claudeRoot}, {Target: harness.Codex, Path: codexRoot}}
	plan, err := PlanPackageTree(source, "github.com/team/pack", "", mode.ImplicitEffective(), roots, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyPackageTreePlan(plan, roots); err != nil {
		t.Fatal(err)
	}
	lastWrite := map[harness.Target]map[string]PlannedPackageFile{}
	for _, file := range plan.Files {
		if lastWrite[file.Target] == nil {
			lastWrite[file.Target] = map[string]PlannedPackageFile{}
		}
		lastWrite[file.Target][file.RelativePath] = file
	}
	for _, root := range roots {
		for relative, planned := range lastWrite[root.Target] {
			actual, err := os.ReadFile(filepath.Join(root.Path, filepath.FromSlash(relative)))
			if err != nil {
				t.Fatalf("%s %s: %v", root.Target, relative, err)
			}
			if string(actual) != string(planned.Contents) {
				t.Errorf("%s %s differs from plan", root.Target, relative)
			}
		}
	}
	for _, expected := range []struct {
		target harness.Target
		path   string
	}{
		{harness.Claude, "agents/reviewer.md"},
		{harness.Claude, "agents/ops/reviewer.md"},
		{harness.Codex, "agents/reviewer.toml"},
		{harness.Codex, "skills/review-command/SKILL.md"},
		{harness.Codex, "skills/guard/SKILL.md"},
		{harness.Claude, "skills/review/references/guide.md"},
		{harness.Claude, "core/support.txt"},
	} {
		if _, ok := lastWrite[expected.target][expected.path]; !ok {
			t.Errorf("plan missing %s %s", expected.target, expected.path)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(claudeRoot, "core", "support.txt"))
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("support executable mode=%v, err=%v", info, err)
		}
		for _, root := range roots {
			info, err := os.Stat(filepath.Join(root.Path, "skills", "review", "scripts", "check.sh"))
			if err != nil || info.Mode().Perm() != 0o744 {
				t.Fatalf("skill script executable mode=%v, err=%v", info, err)
			}
		}
	}
}

func TestPackagePlanRejectsNewArtifactBeforePublication(t *testing.T) {
	source, destination := t.TempDir(), t.TempDir()
	writeTree(t, source, map[string]string{"commands/first.md": "First\n"})
	roots := []HarnessRoot{{Target: harness.Claude, Path: destination}}
	plan, err := PlanPackageTree(source, "github.com/team/pack", "", mode.ImplicitEffective(), roots, true)
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, source, map[string]string{"commands/new.md": "New after planning\n"})
	if err := ApplyPackageTreePlan(plan, roots); err == nil {
		t.Fatal("published an outdated plan after adding an artifact")
	}
	if entries, err := os.ReadDir(destination); err != nil || len(entries) != 0 {
		t.Fatalf("output changed: %v %v", entries, err)
	}
}

func TestPackagePlanAbortsBeforePublishingWhenInputsChange(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	writeTree(t, source, map[string]string{
		"skills/review/SKILL.md": "---\nname: review\n---\nBefore\n",
		"core/support.txt":       "Support before\n",
	})
	plan, err := PlanPackageTree(source, "github.com/team/pack", "", mode.ImplicitEffective(), []HarnessRoot{{Target: harness.Claude, Path: target}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "core", "support.txt"), []byte("Support after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPackageTreePlan(plan, []HarnessRoot{{Target: harness.Claude, Path: target}}); err == nil || !strings.Contains(err.Error(), "package input changed after planning") {
		t.Fatalf("expected input drift error, got %v", err)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("published output despite input drift: entries=%v err=%v", entries, err)
	}
}

func TestPackagePlanKeepsModesAndMalformedFrontmatterFailSoft(t *testing.T) {
	source := t.TempDir()
	writeTree(t, source, map[string]string{
		"skills/keep/SKILL.md": "---\nname: keep\n---\nKept\n",
		"commands/disabled.md": "---\ndescription: Disabled\n---\nDisabled\n",
		"commands/broken.md":   "---\nname: [\n---\nMalformed\n",
	})
	effective, err := mode.NewEffective("ci", mode.Definition{Base: mode.BaseAll, Disable: []string{"package-path:github.com/team/pack:commands/disabled.md"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanPackageTree(source, "github.com/team/pack", "", effective, []HarnessRoot{{Target: harness.Claude}}, false)
	if err != nil {
		t.Fatal(err)
	}
	var kept, disabled, malformed bool
	for _, artifact := range plan.Artifacts {
		switch artifact.SourceRelative {
		case "skills/keep/SKILL.md":
			kept = !artifact.Omitted && artifact.OutputPath != ""
		case "commands/disabled.md":
			disabled = artifact.Omitted && artifact.OutputPath == ""
		case "commands/broken.md":
			malformed = !artifact.Omitted && artifact.OutputPath != ""
		}
	}
	if !kept || !disabled || !malformed {
		t.Fatalf("mode plan mismatch: %#v", plan.Artifacts)
	}
	for _, file := range plan.Files {
		if strings.Contains(file.RelativePath, "disabled") {
			t.Errorf("mode-excluded item staged: %s", file.RelativePath)
		}
	}
}
