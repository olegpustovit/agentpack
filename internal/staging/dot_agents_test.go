package staging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func TestStageDotAgentsLeavesNativeCodexSkillsInProject(t *testing.T) {
	project, stage := t.TempDir(), t.TempDir()
	t.Setenv("AGENTPACK_STAGING_ROOT", stage)
	dot := filepath.Join(project, ".agents")
	files := map[string]string{"skills/local/SKILL.md": "---\nname: local\ndescription: Local skill\n---\n", "commands/review.md": "---\ndescription: Review code\n---\nReview it.\n", "rules/team.mdc": "---\nalwaysApply: true\n---\nTeam rule.\n", "claude/settings.json": "{}", "codex/theme.toml": "x = 1", "AGENTS.md": "project agents", "CLAUDE.md": "project claude"}
	for relative, contents := range files {
		path := filepath.Join(dot, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	codex, _ := paths.StagingCodexHomeDirForMode(project, "default")
	if err := StageDotAgents(project, project, "default", mode.ImplicitEffective(), codex, nil); err != nil {
		t.Fatal(err)
	}
	plugins, _ := paths.StagingPluginsDirForMode(project, "default")
	bundle := filepath.Join(plugins, paths.StagedAgentpackBundleName)
	for _, path := range []string{filepath.Join(bundle, "settings.json"), filepath.Join(bundle, "commands/review.md"), filepath.Join(bundle, "skills/local/SKILL.md"), filepath.Join(bundle, "rules/dot-agents--team.mdc"), filepath.Join(bundle, "CLAUDE.md"), filepath.Join(codex, "theme.toml"), filepath.Join(codex, "skills/review/SKILL.md"), filepath.Join(codex, "AGENTS.md")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(codex, "skills/local/SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("project-local skill copied into Codex home: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(codex, "skills/review/SKILL.md"))
	if !strings.Contains(string(data), "disable-model-invocation: true") {
		t.Fatalf("codex command fallback = %s", data)
	}
}

func TestDotAgentsPlanMatchesMaterializedWritesAndNativeCodexSkills(t *testing.T) {
	dot, claude, codex := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, dot, map[string]string{
		"claude/commands/from-claude.md": "Claude subtree command\n",
		"codex/theme.toml":               "theme = 'night'\n",
		"skills/local/SKILL.md":          "---\nname: local\n---\nNative skill\n",
		"rules/team/guard.mdc":           "---\nalwaysApply: true\n---\nRule\n",
		"agents/review.md":               "---\ndescription: Review agent\n---\nAgent\n",
		"commands/review.md":             "---\ndescription: Review command\n---\nCommand\n",
		"commands/disabled.md":           "---\ndescription: Disabled\n---\nDisabled\n",
		"AGENTS.md":                      "Guidance\n",
	})
	effective, err := mode.NewEffective("dot", mode.Definition{Base: mode.BaseAll, Disable: []string{".agents:commands/disabled.md"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanDotAgents(dot, effective, nil)
	if err != nil {
		t.Fatal(err)
	}
	last := map[harness.Target]map[string]DotAgentsFile{}
	for _, file := range plan.Files {
		if last[file.Target] == nil {
			last[file.Target] = map[string]DotAgentsFile{}
		}
		last[file.Target][file.RelativePath] = file
	}
	if _, exists := last[harness.Codex]["skills/local/SKILL.md"]; exists {
		t.Fatal("native Codex skill was planned as a staging write")
	}
	for _, expected := range []struct {
		target harness.Target
		path   string
	}{
		{harness.Claude, "commands/from-claude.md"},
		{harness.Codex, "theme.toml"},
		{harness.Claude, "skills/local/SKILL.md"},
		{harness.Claude, "rules/dot-agents--team--guard.mdc"},
		{harness.Claude, "commands/review.md"},
		{harness.Codex, "skills/review/SKILL.md"},
		{harness.Codex, "AGENTS.md"},
	} {
		if _, found := last[expected.target][expected.path]; !found {
			t.Errorf("plan omitted %s %s", expected.target, expected.path)
		}
	}
	for path := range last[harness.Claude] {
		if strings.Contains(path, "disabled") {
			t.Errorf("mode-excluded file planned: %s", path)
		}
	}
	var native, omitted bool
	for _, artifact := range plan.Artifacts {
		if artifact.SourceRelative == "skills/local/SKILL.md" && artifact.Target == harness.Codex {
			native = artifact.NativeSource == "skills/local/SKILL.md" && artifact.OutputPath == ""
		}
		if artifact.SourceRelative == "commands/disabled.md" {
			omitted = artifact.Omitted && artifact.OutputPath == ""
		}
	}
	if !native || !omitted {
		t.Fatalf("native/omission plan mismatch: %#v", plan.Artifacts)
	}
	if err := ApplyDotAgentsPlan(plan, claude, codex); err != nil {
		t.Fatal(err)
	}
	for _, root := range []struct {
		target harness.Target
		path   string
	}{{harness.Claude, claude}, {harness.Codex, codex}} {
		for relative, planned := range last[root.target] {
			actual, err := os.ReadFile(filepath.Join(root.path, filepath.FromSlash(relative)))
			if err != nil || string(actual) != string(planned.Contents) {
				t.Errorf("%s %s differs from plan: err=%v", root.target, relative, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(codex, "skills", "local", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("native skill copied into Codex home: %v", err)
	}
}

func TestDotAgentsDisableSkipsOverlayButKeepsNativeCodexSkills(t *testing.T) {
	dot := t.TempDir()
	writeTree(t, dot, map[string]string{
		"skills/native/SKILL.md": "---\nname: native\n---\nSkill\n",
		"commands/cmd.md":        "Command\n",
	})
	t.Setenv("AGENTPACK_DOT_AGENTS", "0")
	target := harness.Codex
	plan, err := PlanDotAgents(dot, mode.ImplicitEffective(), &target)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 0 {
		t.Fatalf("disabled overlay still plans writes: %#v", plan.Files)
	}
	var native bool
	for _, artifact := range plan.Artifacts {
		native = native || artifact.NativeSource == "skills/native/SKILL.md"
	}
	if !native {
		t.Fatalf("native Codex discovery lost when overlay disabled: %#v", plan.Artifacts)
	}
}

func TestDotAgentsPlanRejectsSourceChangesAndAdditions(t *testing.T) {
	dot := t.TempDir()
	writeTree(t, dot, map[string]string{"AGENTS.md": "initial\n"})
	plan, err := PlanDotAgents(dot, mode.ImplicitEffective(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("unchanged plan failed validation: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dot, "AGENTS.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err == nil {
		t.Fatal("changed source accepted")
	}
	plan, err = PlanDotAgents(dot, mode.ImplicitEffective(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dot, "new.md"), []byte("new input\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err == nil {
		t.Fatal("new source accepted")
	}
}
