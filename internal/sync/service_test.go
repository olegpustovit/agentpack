package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/staging"
)

func TestServiceAddsPathDependencyAndStagesIt(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
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
	pkg, err := NewService().Add(context.Background(), project, source, false)
	if err != nil {
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

func TestSyncForLaunchUsesVerifiedFastPath(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
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
	if _, skipped, err := service.SyncForLaunch(context.Background(), project, "", base.Claude); err != nil || skipped {
		t.Fatalf("first skipped=%v err=%v", skipped, err)
	}
	if _, skipped, err := service.SyncForLaunch(context.Background(), project, "", base.Claude); err != nil || !skipped {
		t.Fatalf("second skipped=%v err=%v", skipped, err)
	}
}

// A launch of another agent must not need the staging a running session holds.
func TestSyncForLaunchOfAnotherAgentWhileOneIsRunning(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
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
	effective, _, err := service.SyncForLaunch(context.Background(), project, "", base.Claude)
	if err != nil {
		t.Fatal(err)
	}
	running, err := staging.AcquireLaunchShared(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	defer running.Unlock()
	// twice round, so that a launch after Cursor's or Antigravity's overlay is covered too
	for _, target := range []base.Target{base.Claude, base.Codex, base.OpenCode, base.Grok, base.Cursor, base.Agy,
		base.Claude, base.Codex, base.Agy, base.Cursor, base.Claude} {
		if _, _, err := service.SyncForLaunch(context.Background(), project, "", target); err != nil {
			t.Fatalf("%s while claude runs: %v", target, err)
		}
		if _, skipped, err := service.SyncForLaunch(context.Background(), project, "", target); err != nil || !skipped {
			t.Fatalf("%s repeat: skipped=%v err=%v", target, skipped, err)
		}
		if err := service.ValidateLaunchInputs(project, project, effective, target); err != nil {
			t.Fatalf("%s: %v", target, err)
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
	running, err := staging.AcquireLaunchShared(fixture.project, "default")
	if err != nil {
		t.Fatal(err)
	}
	defer running.Unlock()
	if _, err := staging.TryAcquireRebuildExclusive(fixture.project, "default"); err == nil {
		t.Fatal("fixture did not hold the running session's lock")
	}
	fixture.manifest(demoKey, commitTwo)
	for _, target := range []base.Target{base.Codex, base.Claude, base.Grok, base.OpenCode, base.Cursor, base.Agy} {
		if _, _, err := fixture.service.SyncForLaunch(context.Background(), fixture.project, "", target); err != nil {
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
	if _, _, err := fixture.service.SyncForLaunch(context.Background(), fixture.project, "", base.Claude); err != nil {
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

func TestCursorUserSkillsRefreshOnLaunch(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
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
		if _, skipped, err := service.SyncForLaunch(context.Background(), project, "", base.Cursor); err != nil || skipped {
			t.Fatalf("changed user skill %q: skipped=%v err=%v", body, skipped, err)
		}
		fake, err := paths.StagingCursorHomeDirForMode(project, "default")
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
		if _, skipped, err := service.SyncForLaunch(context.Background(), project, "", base.Cursor); err != nil || !skipped {
			t.Fatalf("unchanged user skill: skipped=%v err=%v", skipped, err)
		}
	}
}
