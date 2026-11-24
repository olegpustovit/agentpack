package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

func pathDependencyProject(t *testing.T) string {
	t.Helper()
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
	if _, err := NewService().Add(t.Context(), project, source); err != nil {
		t.Fatal(err)
	}
	return project
}

// One read of pack.lock per command: every later check digests the bytes that
// read returned, so a lock replaced mid-command cannot be half-used.
func TestSyncAndLaunchReadPackLockOnce(t *testing.T) {
	project := pathDependencyProject(t)
	reads := 0
	previous := readLockFile
	readLockFile = func(root string) ([]byte, error) { reads++; return previous(root) }
	t.Cleanup(func() { readLockFile = previous })
	service := NewService()
	for _, step := range []struct {
		name string
		run  func() (bool, error)
	}{
		{"sync", func() (bool, error) { _, err := service.Sync(t.Context(), project, SyncOptions{}); return false, err }},
		{"cold launch", func() (bool, error) {
			_, skipped, err := service.SyncForLaunchOptions(t.Context(), project, SyncOptions{Target: new(base.Claude)})
			return !skipped, err
		}},
		{"warm launch", func() (bool, error) {
			_, skipped, err := service.SyncForLaunchOptions(t.Context(), project, SyncOptions{Target: new(base.Claude)})
			return skipped, err
		}},
	} {
		reads = 0
		if ok, err := step.run(); err != nil || step.name != "sync" && !ok {
			t.Fatalf("%s: path=%v err=%v", step.name, ok, err)
		}
		if reads != 1 {
			t.Fatalf("%s read pack.lock %d times, want 1", step.name, reads)
		}
	}
}

// Sync takes the definition mutation lock itself, so gateway restarts, ACP
// workers and env restore are serialized with CLI mutations; a caller that
// already holds it passes the hold through its context instead of waiting on itself.
func TestSyncHoldsDefinitionLockWithoutDeadlockingItsHolder(t *testing.T) {
	project := pathDependencyProject(t)
	service := NewService()
	held, release, err := LockDefinition(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Sync(cancelled, project, SyncOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("sync did not wait for the holder: %v", err)
	}
	if _, _, err := service.SyncForLaunchOptions(cancelled, project, SyncOptions{Target: new(base.Claude)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("launch did not wait for the holder: %v", err)
	}
	if _, err := service.Sync(held, project, SyncOptions{}); err != nil {
		t.Fatalf("holder's own sync: %v", err)
	}
	if _, err := service.Sync(cancelled, project, SyncOptions{DryRun: true}); err != nil {
		t.Fatalf("dry run must not wait: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := service.Sync(context.Background(), project, SyncOptions{}); done <- err }()
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("waiting sync after release: %v", err)
	}
}
