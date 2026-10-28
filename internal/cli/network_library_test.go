package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/registry"
)

func TestGatewayLibrarySaveCaptureRestore(t *testing.T) {
	home, root, workspace := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	if err := manifest.WriteStub(root, "review", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(root, "review", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner()
	runner.Stdout, runner.Stderr = io.Discard, io.Discard
	ctx := context.Background()
	if code, err := runner.Run(ctx, []string{"env", "bind", root, "--project", workspace}); err != nil || code != 0 {
		t.Fatalf("bind: %d %v", code, err)
	}
	library, err := runner.networkLibrary(home)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := library.Save(ctx, network.SaveAgentpackInput{Directory: workspace, Name: "Team review", AutoCapture: true})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Agentpack.Name != "Team review" || saved.Revision.Project != workspace {
		t.Fatalf("saved metadata: %+v", saved)
	}
	draftState, err := library.DraftState(ctx, registry.Draft{Directory: workspace, PackID: saved.Agentpack.ID, Revision: saved.Revision.ID})
	if err != nil || draftState.CurrentDiff == nil || len(draftState.Pins) != 0 || len(draftState.CurrentDiff.Add) != 0 {
		t.Fatalf("external draft preview: %+v %v", draftState, err)
	}
	missing, err := library.DraftState(ctx, registry.Draft{Directory: workspace, PackID: saved.Agentpack.ID, Revision: strings.Repeat("a", 64), NeededHolder: "build-machine"})
	if err != nil || len(missing.MissingObjects) != 1 || missing.NeededHolder != "build-machine" {
		t.Fatalf("missing revision lost exact holder: %+v %v", missing, err)
	}
	// A real CLI mutation appends only when the semantic content changes.
	if err := manifest.ReplaceModes(root, map[string]mode.Definition{"quiet": {Base: mode.BaseNone}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if code, err := runner.Run(ctx, []string{"--env", root, "--workspace", workspace, "sync"}); err != nil || code != 0 {
			t.Fatalf("capture sync: %d %v", code, err)
		}
	}
	_, revisions, err := library.Store.Get(saved.Agentpack.ID)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("capture revisions: %d %v", len(revisions), err)
	}
	if _, err := library.Preview(ctx, saved.Agentpack.ID, saved.Revision.ID, workspace); err != nil {
		t.Fatal(err)
	}
	if err := library.Restore(ctx, saved.Agentpack.ID, saved.Revision.ID, workspace, false); err != nil {
		t.Fatal(err)
	}
	project, err := manifest.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := project.ModeDefinition("quiet"); exists {
		t.Fatal("restore retained the later mode")
	}
	_, revisions, err = library.Store.Get(saved.Agentpack.ID)
	if err != nil || len(revisions) != 3 || len(revisions[2].ParentIDs) != 1 || revisions[2].ParentIDs[0] != saved.Revision.ID {
		t.Fatalf("restore parentage: %+v %v", revisions, err)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("external restore wrote checkout: %v %v", entries, err)
	}
}

func TestCaptureDisabledDoesNotCreateRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	if err := captureNetworkEnvironment(t.TempDir(), t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "registry")); !os.IsNotExist(err) {
		t.Fatalf("disabled capture created registry: %v", err)
	}
}
