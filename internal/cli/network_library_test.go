package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/olegpustovit/agentpack/internal/cache"
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

func TestGatewayLibraryBootstrapsRecentProjectsAndCachedPackages(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("HOME", t.TempDir())
	firstRoot, firstWorkspace := t.TempDir(), t.TempDir()
	if err := manifest.WriteStub(firstRoot, "first", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(firstRoot, "first", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	writeRecent := func(entries ...network.SessionInfo) {
		t.Helper()
		data, err := json.Marshal(entries)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(home, "network"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "network", "recent.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	recent := func(root, workspace string) network.SessionInfo {
		t.Helper()
		identity, err := network.FolderIdentity(workspace)
		if err != nil {
			t.Fatal(err)
		}
		return network.SessionInfo{Directory: workspace, DefinitionRoot: root, FolderID: identity, Harness: "claude", Mode: "default"}
	}
	first := recent(firstRoot, firstWorkspace)
	manualWorkspace := t.TempDir()
	if err := manifest.WriteStub(manualWorkspace, "manual", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(manualWorkspace, "manual", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	manual := recent(manualWorkspace, manualWorkspace)
	writeRecent(first, manual)

	key := cache.ComputeKey("github:acme/tool@" + strings.Repeat("a", 40))
	cacheRoot, err := cache.EntryDir(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheRoot, "SKILL.md"), []byte("---\nname: Tool\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	if err := cache.UpsertEntry(key, cache.EntryRecord{Kind: lockfile.PackageSkill, SourceURL: "https://github.com/acme/tool", Owner: "acme", Repo: "tool", Commit: commit}, nil); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner()
	library, err := runner.networkLibrary(home)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := library.Save(context.Background(), network.SaveAgentpackInput{Directory: manualWorkspace, Name: "Existing manual pack"})
	if err != nil {
		t.Fatal(err)
	}
	if err := library.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	packs, err := library.Store.ListAgentpacks()
	if err != nil || len(packs) != 2 {
		t.Fatalf("bootstrapped agentpacks: %+v %v", packs, err)
	}
	seededID := ""
	for _, pack := range packs {
		if pack.Name == filepath.Base(firstWorkspace) {
			seededID = pack.ID
		}
	}
	if seededID == "" {
		t.Fatalf("recent project was not bootstrapped: %+v", packs)
	}
	revisions := 1
	var wait sync.WaitGroup
	bootstrapErrors := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			bootstrapErrors <- library.Bootstrap()
		}()
	}
	wait.Wait()
	close(bootstrapErrors)
	for err := range bootstrapErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	packs, err = library.Store.ListAgentpacks()
	if err != nil || len(packs) != 2 {
		t.Fatalf("repeated bootstrap created duplicate project revisions: %+v %v", packs, err)
	}
	seeded, _, err := library.Store.Get(seededID)
	if err != nil || len(seeded.Revisions) != revisions {
		t.Fatalf("repeated bootstrap created duplicate project revisions: %+v %v", seeded, err)
	}
	if _, err := library.Store.Rename(seededID, "Manually named"); err != nil {
		t.Fatal(err)
	}
	secondRoot, secondWorkspace := t.TempDir(), t.TempDir()
	if err := manifest.WriteStub(secondRoot, "second", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(secondRoot, "second", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	writeRecent(recent(secondRoot, secondWorkspace), first)
	if err := library.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	packs, err = library.Store.ListAgentpacks()
	if err != nil || len(packs) != 3 {
		t.Fatalf("newly recent project was not imported: %+v %v", packs, err)
	}
	foundRename := false
	for _, pack := range packs {
		foundRename = foundRename || pack.Name == "Manually named"
	}
	if !foundRename {
		t.Fatalf("bootstrap overwrote a user rename: %+v", packs)
	}
	var renamedID string
	for _, pack := range packs {
		if pack.Name == "Manually named" {
			renamedID = pack.ID
		}
	}
	if renamedID == "" {
		t.Fatal("renamed pack disappeared")
	}
	if _, err := library.Store.DeleteAgentpack(renamedID); err != nil {
		t.Fatal(err)
	}
	if err := library.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	packs, err = library.Store.ListAgentpacks()
	if err != nil || len(packs) != 2 || slices.ContainsFunc(packs, func(pack registry.Agentpack) bool { return pack.ID == renamedID }) {
		t.Fatalf("bootstrap recreated a deleted project: %+v %v", packs, err)
	}
	if !slices.ContainsFunc(packs, func(pack registry.Agentpack) bool { return pack.ID == saved.Agentpack.ID }) {
		t.Fatalf("bootstrap duplicated an existing manually saved pack: %+v", packs)
	}
	packages, err := library.Store.ListPackages()
	if err != nil || len(packages) != 1 || packages[0].Module != "github.com/acme/tool" || packages[0].Commit != commit || packages[0].ContentHash != "" {
		t.Fatalf("bootstrapped cached packages: %+v %v", packages, err)
	}
	if err := os.Remove(filepath.Join(home, "registry", "index.json")); err != nil {
		t.Fatal(err)
	}
	packages, err = library.Store.ListPackages()
	if err != nil || len(packages) != 1 || packages[0].Module != "github.com/acme/tool" {
		t.Fatalf("cache package was not recovered from public objects: %+v %v", packages, err)
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
