package sync

import (
	"context"
	"testing"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
)

func TestChangedEnvironmentsSkipsUnchangedDigests(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	root := t.TempDir()
	if err := manifest.WriteStub(root, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(root, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	digest, err := EnvironmentDigest(root, root, "default", harness.Claude)
	if err != nil {
		t.Fatal(err)
	}
	same := RunningEnvironment{SessionID: "same", DefinitionRoot: root, Workspace: root, Mode: "default", Target: harness.Claude, Digest: digest}
	moved := same
	moved.SessionID, moved.Digest = "moved", "stale"
	changed, err := NewService().ChangedEnvironments(context.Background(), []RunningEnvironment{same, moved})
	if err != nil || len(changed) != 1 || changed[0].SessionID != "moved" {
		t.Fatalf("changed=%+v err=%v", changed, err)
	}
}
