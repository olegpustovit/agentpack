package codex

import (
	"os"
	"path/filepath"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func TestWindowsGenerationSurvivesTempDirectoryChange(t *testing.T) {
	project := t.TempDir()
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("TMP", t.TempDir())
	t.Setenv("TEMP", os.Getenv("TMP"))
	firstSet, err := generations(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	first, err := firstSet.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Abort()
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TMP", t.TempDir())
	t.Setenv("TEMP", os.Getenv("TMP"))
	if directory, err := shortStagingCodexHome(project, "default"); err != nil || directory == firstSet.Directory {
		t.Fatalf("temp directory did not change: %q, %v", directory, err)
	}
	ctx := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	if err := preReset(ctx); err != nil {
		t.Fatal(err)
	}
	home, release, err := LeaseHome(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if home != first.Root() {
		t.Fatalf("leased %q, want %q", home, first.Root())
	}
	secondSet, err := generations(project, "default")
	if err != nil || secondSet.Directory != firstSet.Directory {
		t.Fatalf("rebuild directory: %q, %v", secondSet.Directory, err)
	}

	wrongMode := filepath.Join(filepath.Dir(firstSet.Directory), "other", filepath.Base(first.Root()))
	if err := paths.WriteAtomic(firstSet.Pointer, []byte(wrongMode+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CurrentHome(project, "default"); err == nil {
		t.Fatal("accepted a pointer outside the mode's directory")
	}
}
