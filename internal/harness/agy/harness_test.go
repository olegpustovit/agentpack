package agy

import (
	"os"
	"path/filepath"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mode"
)

func TestPrepareWritesManifestAndAttributionRule(t *testing.T) {
	project := t.TempDir()
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "")
	ctx := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	harness := New()
	if err := harness.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := harness.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	root, _ := harness.StagedRoot(ctx)
	for _, relative := range []string{"plugin.json", "rules/agentpack-no-attribution.md"} {
		if _, err := os.Stat(filepath.Join(root, relative)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestACPLaunchConfiguresClientBypassAndUnpromptedSettle(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "agy")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	project := t.TempDir()
	got, err := acpLaunch(base.LaunchContext{ProjectRoot: project, WorkspaceRoot: project, Mode: mode.ImplicitEffective()})
	if err != nil {
		t.Fatal(err)
	}
	if !got.ClientPermissionBypass {
		t.Fatal("ClientPermissionBypass = false, want true")
	}
	if got.UnpromptedUsageSettle <= 0 {
		t.Fatalf("UnpromptedUsageSettle = %v, want > 0", got.UnpromptedUsageSettle)
	}
}
