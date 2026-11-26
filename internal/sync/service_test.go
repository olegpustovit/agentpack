package sync

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/agy"
	"github.com/olegpustovit/agentpack/internal/harness/cursor"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func TestPlainFolderLaunchLeavesProjectFilesAbsent(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())

	service := NewService()
	if _, _, err := service.SyncForLaunchOptions(t.Context(), project, SyncOptions{Target: new(base.Claude)}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.LockPath(project), paths.ManifestPath(project)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to stay absent, stat returned %v", path, err)
		}
	}
}

func TestServiceAddsPathDependencyAndStagesIt(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	source := filepath.Join(project, "demo")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: demo\ndescription: Demo\n---\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(home, ".codex", "auth.json"), filepath.Join(home, ".grok", "auth.json")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := NewService().Add(context.Background(), project, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewService().Sync(context.Background(), project, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if pkg.Module != "demo" {
		t.Fatalf("package=%#v", pkg)
	}
	projectManifest, err := manifest.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := projectManifest.Dependencies["demo"]; !exists {
		t.Fatal("dependency not recorded")
	}
	plugins, _ := paths.StagingPluginsDirForMode(project, "default")
	if _, err := os.Stat(filepath.Join(plugins, paths.StagedAgentpackBundleName, "skills", "demo", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestAgyOverlayIsARealCopyThatOtherLaunchesKeep(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	source := filepath.Join(project, "demo", "SKILL.md")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(source, "---\nname: demo\ndescription: Demo\n---\nFirst\n")
	service := NewService()
	if _, err := service.Add(t.Context(), project, filepath.Dir(source)); err != nil {
		t.Fatal(err)
	}
	launch := func(target base.Target) bool {
		t.Helper()
		_, skipped, err := service.SyncForLaunchOptions(t.Context(), project, SyncOptions{Target: &target})
		if err != nil {
			t.Fatal(err)
		}
		return skipped
	}
	overlay := filepath.Join(project, ".agents", "plugins", paths.StagedAgentpackBundleName)
	skill := func() string {
		t.Helper()
		if err := filepath.WalkDir(overlay, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.Type()&os.ModeSymlink != 0 {
				t.Fatalf("symlink in the overlay: %s", path)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(overlay, "skills", "demo", "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	launch(base.Agy)
	skill()
	launch(base.Claude)
	if got := skill(); !strings.Contains(got, "First") {
		t.Fatalf("overlay after a claude launch: %q", got)
	}
	// A directory left by an interrupted copy is not a project input.
	launch(base.Agy)
	write(filepath.Join(project, ".agents", agy.OverlayTempPrefix+"123", "SKILL.md"), "leftover")
	if !launch(base.Agy) {
		t.Fatal("a leftover temporary directory forced a rebuild")
	}
	write(source, "---\nname: demo\ndescription: Demo\n---\nSecond\n")
	// Launches do not read a path dependency's source; a sync does.
	if _, err := service.Sync(t.Context(), project, SyncOptions{Target: new(base.Agy)}); err != nil {
		t.Fatal(err)
	}
	if got := skill(); !strings.Contains(got, "Second") {
		t.Fatalf("overlay was not updated: %q", got)
	}
	launch(base.Agy)
	if !launch(base.Agy) {
		t.Fatal("unchanged launch rebuilt")
	}
}

func TestSyncForLaunchUsesVerifiedFastPath(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	if err := manifest.WriteStub(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(home, ".codex", "auth.json"), filepath.Join(home, ".grok", "auth.json")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService()
	if _, skipped, err := service.SyncForLaunchOptions(context.Background(), project, SyncOptions{Target: new(base.Claude)}); err != nil || skipped {
		t.Fatalf("first skipped=%v err=%v", skipped, err)
	}
	database := filepath.Join(os.Getenv("AGENTPACK_HOME"), "state.db")
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatalf("cold launch setup created state.db: %v", err)
	}
	marker := []byte("runtime database must not be opened by launch setup")
	if err := os.WriteFile(database, marker, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, skipped, err := service.SyncForLaunchOptions(context.Background(), project, SyncOptions{Target: new(base.Claude)}); err != nil || !skipped {
		t.Fatalf("second skipped=%v err=%v", skipped, err)
	}
	after, err := os.Stat(database)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(database)
	if err != nil || string(contents) != string(marker) || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
		t.Fatalf("warm launch setup changed state.db: %v", err)
	}
	// A one-harness launch records its own materialization, never all harnesses'.
	claude := base.Claude
	if _, found, err := readMaterializationDigest(project, mode.DefaultName, nil); err != nil || found {
		t.Fatalf("launch wrote the all-harness materialization record: found=%v err=%v", found, err)
	}
	if _, found, err := readMaterializationDigest(project, mode.DefaultName, &claude); err != nil || !found {
		t.Fatalf("launch did not record its own materialization: found=%v err=%v", found, err)
	}
	// An upgraded agentpack binary invalidates the launch digest.
	upgraded := filepath.Join(t.TempDir(), "agentpack")
	if err := os.WriteFile(upgraded, []byte("new build"), 0o755); err != nil {
		t.Fatal(err)
	}
	executable = func() (string, error) { return upgraded, nil }
	t.Cleanup(func() { executable = os.Executable })
	if _, skipped, err := service.SyncForLaunchOptions(context.Background(), project, SyncOptions{Target: new(base.Claude)}); err != nil || skipped {
		t.Fatalf("after upgrade skipped=%v err=%v", skipped, err)
	}
}

// A launch of another agent must not need the staging a running session holds.
func TestSyncForLaunchOfAnotherAgentWhileOneIsRunning(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	if err := manifest.WriteStub(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(home, ".codex", "auth.json"), filepath.Join(home, ".grok", "auth.json")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService()
	if _, _, err := service.SyncForLaunchOptions(context.Background(), project, SyncOptions{Target: new(base.Claude)}); err != nil {
		t.Fatal(err)
	}
	// twice round, so that a launch after Cursor's or Antigravity's overlay is covered too
	for _, target := range []base.Target{base.Claude, base.Codex, base.OpenCode, base.Grok, base.Cursor, base.Agy,
		base.Claude, base.Codex, base.Agy, base.Cursor, base.Claude} {
		if _, _, err := service.SyncForLaunchOptions(context.Background(), project, SyncOptions{Target: &target}); err != nil {
			t.Fatalf("%s while claude runs: %v", target, err)
		}
		if _, skipped, err := service.SyncForLaunchOptions(context.Background(), project, SyncOptions{Target: &target}); err != nil || !skipped {
			t.Fatalf("%s repeat: skipped=%v err=%v", target, skipped, err)
		}
	}
}

func TestChangedInputsRebuildAlongsideRunningSessionWithoutChangingItsFiles(t *testing.T) {
	fixture := newIntegrityFixture(t)
	fixture.manifest(demoKey, commitOne)
	fixture.lock()
	fixture.sync(SyncOptions{})
	previous, err := paths.StagingRootForMode(fixture.project, "default")
	if err != nil {
		t.Fatal(err)
	}
	oldSkill := fixture.stagedPath()
	fixture.manifest(demoKey, commitTwo)
	for _, target := range []base.Target{base.Codex, base.Claude, base.Grok, base.OpenCode, base.Cursor, base.Agy} {
		if _, _, err := fixture.service.SyncForLaunchOptions(context.Background(), fixture.project, SyncOptions{Target: &target}); err != nil {
			t.Fatalf("%s launch beside a running session: %v", target, err)
		}
	}
	current, err := paths.StagingRootForMode(fixture.project, "default")
	if err != nil || current == previous {
		t.Fatalf("staging did not advance: %s, %v", current, err)
	}
	if data, err := os.ReadFile(oldSkill); err != nil || string(data) != skillOne {
		t.Fatalf("running session's skill changed: %q, %v", data, err)
	}
	if _, _, err := fixture.service.SyncForLaunchOptions(context.Background(), fixture.project, SyncOptions{Target: new(base.Claude)}); err != nil {
		t.Fatal(err)
	}
	if fixture.staged() != skillTwo {
		t.Fatalf("new launch did not receive updated content: %q", fixture.staged())
	}
}

func TestServiceDryRunDoesNotBuildStaging(t *testing.T) {
	project := t.TempDir()
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	if err := manifest.WriteStub(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	result, err := NewService().Sync(context.Background(), project, SyncOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skills != 0 || result.Plugins != 0 {
		t.Fatalf("result=%#v", result)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("AGENTPACK_STAGING_ROOT"), "modes")); !os.IsNotExist(err) {
		t.Fatal("dry run created staging")
	}
}

func TestServiceRejectsModeSelectorsMissingFromCapabilityCatalog(t *testing.T) {
	project := t.TempDir()
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	contents := "name = \"demo\"\nversion = \"0.0.1\"\n\n[dependencies]\n\n[modes.default]\nbase = \"all\"\ndisable = [\"package:github.com/example/missing\"]\n"
	if err := os.WriteFile(filepath.Join(project, "agentpack.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	_, err := NewService().Sync(context.Background(), project, SyncOptions{DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "unknown package selector target") {
		t.Fatalf("Sync() error = %v", err)
	}
}

// Staging reads .agents from the workspace, so mode selectors resolve there too.
func TestModeSelectorsResolveAgainstTheWorkspace(t *testing.T) {
	definition, workspace := t.TempDir(), t.TempDir()
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	contents := "name = \"demo\"\nversion = \"0.0.1\"\n\n[dependencies]\n\n[modes.default]\nbase = \"all\"\ndisable = [\".agents:rules/a.mdc\"]\n"
	if err := os.WriteFile(filepath.Join(definition, "agentpack.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".agents", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".agents", "rules", "a.mdc"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService().Sync(context.Background(), definition, SyncOptions{DryRun: true, WorkspaceRoot: workspace}); err != nil {
		t.Fatalf("Sync(workspace) = %v", err)
	}
	if _, err := NewService().Sync(context.Background(), definition, SyncOptions{DryRun: true}); err == nil || !strings.Contains(err.Error(), "unknown .agents selector target") {
		t.Fatalf("Sync(definition) = %v", err)
	}
}

func TestCursorUserSkillsRefreshOnLaunch(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	if err := manifest.WriteStub(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(home, ".cursor", "skills", "personal", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	service := NewService()
	for _, body := range []string{"first", "edited", ""} {
		if body == "" {
			if err := os.RemoveAll(filepath.Dir(skill)); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(skill, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, skipped, err := service.SyncForLaunchOptions(context.Background(), project, SyncOptions{Target: new(base.Cursor)}); err != nil || skipped {
			t.Fatalf("changed user skill %q: skipped=%v err=%v", body, skipped, err)
		}
		fake, err := cursor.StagingHome(project, "default")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(fake, ".cursor", "skills", "personal", "SKILL.md"))
		if body == "" {
			if !os.IsNotExist(err) {
				t.Fatalf("removed user skill still staged: %q, %v", data, err)
			}
		} else if err != nil || string(data) != body {
			t.Fatalf("staged user skill = %q, %v", data, err)
		}
		if _, skipped, err := service.SyncForLaunchOptions(context.Background(), project, SyncOptions{Target: new(base.Cursor)}); err != nil || !skipped {
			t.Fatalf("unchanged user skill: skipped=%v err=%v", skipped, err)
		}
	}
}

// Tidying scales with the temp directory, so a launch must not wait for it.
func TestLaunchDoesNotWaitForOrphanRemoval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	project := t.TempDir()
	if err := manifest.WriteStub(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	finished := make(chan struct{})
	previous := removeOrphans
	removeOrphans = func(string) { <-release; close(finished) }
	t.Cleanup(func() { close(release); <-finished; removeOrphans = previous })
	var steps []string
	target := base.Claude
	_, skipped, err := NewService().SyncForLaunchOptions(context.Background(), project, SyncOptions{Target: &target, Progress: func(step string) { steps = append(steps, step) }})
	if err != nil {
		t.Fatal(err)
	}
	if skipped {
		t.Fatal("expected the first launch to rebuild")
	}
	if !slices.Contains(steps, "staging skills, commands and MCP config") {
		t.Fatalf("rebuild did not report staging: %v", steps)
	}
}

// A launch stages only its own harness, so switching agents must not rebuild.
func TestSwitchingAgentsKeepsEachGeneration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	project := t.TempDir()
	if err := manifest.WriteStub(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(project, "demo", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	for index, step := range []struct {
		target  base.Target
		skipped bool
	}{{base.Claude, false}, {base.OMP, false}, {base.Grok, false}, {base.Claude, true}, {base.OMP, true}, {base.Grok, true}} {
		if _, skipped, err := NewService().SyncForLaunchOptions(context.Background(), project, SyncOptions{Target: &step.target}); err != nil || skipped != step.skipped {
			t.Fatalf("launch %d of %s: skipped=%v, want %v: %v", index, step.target, skipped, step.skipped, err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, "agentpack.toml"), []byte("name = \"demo\"\nversion = \"0.0.2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, skipped, err := NewService().SyncForLaunchOptions(context.Background(), project, SyncOptions{Target: new(base.Claude)}); err != nil || skipped {
		t.Fatalf("changed inputs must rebuild: skipped=%v: %v", skipped, err)
	}
}
