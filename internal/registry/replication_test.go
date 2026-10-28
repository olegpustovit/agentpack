package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exportInventory(t *testing.T, store *Store, inventory Inventory) map[string][]byte {
	t.Helper()
	documents := make(map[string][]byte, len(inventory.Objects)+len(inventory.Tombstones))
	for _, id := range append(append([]string{}, inventory.Objects...), inventory.Tombstones...) {
		data, err := store.ExportObject(id)
		if err != nil {
			t.Fatal(err)
		}
		documents[id] = data
	}
	return documents
}

func TestInventoryExchangeAndRedaction(t *testing.T) {
	source, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pack, _, err := source.Capture(CaptureInput{Name: "Shared", Project: "/workspace/shared", Manifest: []byte(manifestTemplate), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := source.Inventory()
	if err != nil || inventory.Schema != objectSchema || len(inventory.Objects) == 0 {
		t.Fatalf("source inventory: %#v %v", inventory, err)
	}
	destination, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	delta, err := destination.MissingFrom(inventory)
	if err != nil || len(delta.Objects) != len(inventory.Objects) {
		t.Fatalf("missing inventory: %#v %v", delta, err)
	}
	if _, err := destination.ImportObjects(exportInventory(t, source, inventory)); err != nil {
		t.Fatal(err)
	}
	got, revisions, err := destination.Get(pack.ID)
	if err != nil || got.ID != pack.ID || len(revisions) != 1 {
		t.Fatalf("imported pack: %#v %#v %v", got, revisions, err)
	}
	for _, id := range inventory.Objects {
		data, err := source.ExportObject(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"env-secret", "arg-secret", "url-secret", "header-secret", "user:pass", "ghp_123", "url-secret"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("replication object contains %q", secret)
			}
		}
	}
}

func TestAgentpackNameReplicatesAndRenameConverges(t *testing.T) {
	source, target := mustStore(t), mustStore(t)
	pack, _, err := source.Capture(CaptureInput{Name: "Shared profile", Project: "/workspace/demo", SourceNode: "source-node", Manifest: []byte("name = \"demo\"\n"), Lock: []byte("lockfile_version = 2\n")})
	if err != nil {
		t.Fatal(err)
	}
	transfer := func(inventory Inventory) {
		t.Helper()
		missing, err := target.MissingFrom(inventory)
		if err != nil {
			t.Fatal(err)
		}
		objects := map[string][]byte{}
		for _, id := range append(missing.Objects, missing.Tombstones...) {
			objects[id], err = source.ExportObject(id)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := target.ImportObjects(objects); err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := source.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	transfer(inventory)
	got, revisions, err := target.Get(pack.ID)
	if err != nil || got.Name != "Shared profile" || len(revisions) != 1 {
		t.Fatalf("custom name did not replicate: %#v %#v %v", got, revisions, err)
	}
	if _, err := source.Rename(pack.ID, "Renamed profile"); err != nil {
		t.Fatal(err)
	}
	inventory, err = source.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	transfer(inventory)
	got, revisions, err = target.Get(pack.ID)
	if err != nil || got.Name != "Renamed profile" || len(revisions) != 1 || len(got.Heads) != 1 {
		t.Fatalf("rename did not converge independently of revision history: %#v %#v %v", got, revisions, err)
	}
	if _, err := source.Rename(pack.ID, "Bearer should-not-publish"); err == nil {
		t.Fatal("credential-shaped name was accepted")
	}
}

func TestCaptureURLCredentialsNeverAffectPublicDigest(t *testing.T) {
	store := mustStore(t)
	input := CaptureInput{Name: "URL credentials", Project: "/workspace/url-credentials", Manifest: []byte(manifestTemplate), Lock: lockWithCommit("abc123")}
	pack, first, err := store.Capture(input)
	if err != nil {
		t.Fatal(err)
	}
	input.ID = pack.ID
	input.Manifest = []byte(strings.ReplaceAll(strings.ReplaceAll(manifestTemplate, "url-secret", "other-query-secret"), "user:pass", "other-user:other-pass"))
	_, second, err := store.Capture(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("source URL credentials changed public revision digest: %s != %s", first.ID, second.ID)
	}
	for _, id := range []string{first.ID, first.PackageIDs[0]} {
		data, err := store.ExportObject(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"url-secret", "other-query-secret", "user:pass", "other-user:other-pass"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("public object %s contains URL credential %q", id, secret)
			}
		}
	}
}

func TestImportRejectsCredentialBearingURLMetadata(t *testing.T) {
	for _, value := range []string{
		"https://example.test/pkg?key=query-secret",
		"https://user:pass@example.test/pkg",
	} {
		pkg := Package{Schema: objectSchema, Module: "github.com/acme/tool", Commit: "abc123", Kind: "skill", URL: value, CacheKey: "cache-key"}
		data, err := marshalObject(pkg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeImported(data); err == nil {
			t.Fatalf("import accepted credential-bearing URL %q", value)
		}
	}
}

func TestCaptureRedactsCredentialShapedSourceMetadataBeforeHashing(t *testing.T) {
	store := mustStore(t)
	pack, revision, err := store.Capture(CaptureInput{Name: "Metadata", Label: "access_token=label-secret", SourceNode: "Bearer node-secret", Project: "/workspace/token=project-secret", Manifest: []byte("name = \"metadata\"\n"), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	if revision.Project != "<local>" || revision.SourceNode != "<local>" || revision.Label != "<local>" {
		t.Fatalf("metadata was not redacted: %#v", revision)
	}
	inventory, err := store.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range inventory.Objects {
		data, err := store.ExportObject(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"label-secret", "node-secret", "project-secret"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("object %s contains metadata secret %q", id, secret)
			}
		}
	}
	if _, _, err := store.Get(pack.ID); err != nil {
		t.Fatal(err)
	}
}

func TestImportRejectsChangedDigestAndMalformedObjects(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportObjects(map[string][]byte{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": []byte(`{"schema":1,"module":"github.com/acme/tool"}`)}); err == nil {
		t.Fatal("import accepted malformed or unsupported object")
	}
	if _, err := store.ImportObjects(map[string][]byte{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": []byte("not json")}); err == nil {
		t.Fatal("import accepted invalid object bytes")
	}
	source := mustStore(t)
	if _, _, err := source.Capture(CaptureInput{Name: "Digest", Project: "/workspace/digest", Manifest: []byte("name = \"digest\"\n"), Lock: lockWithCommit("abc123")}); err != nil {
		t.Fatal(err)
	}
	inventory, err := source.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	data, err := source.ExportObject(inventory.Objects[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportObjects(map[string][]byte{strings.Repeat("f", 64): data}); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("import accepted digest mismatch: %v", err)
	}
	if after, err := store.Inventory(); err != nil || len(after.Objects) != 0 {
		t.Fatalf("digest mismatch published an object: %#v %v", after, err)
	}
}

func TestTombstonePropagationWaitsForEnrolledPeerAndGarbageCollection(t *testing.T) {
	source, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pack, _, err := source.Capture(CaptureInput{Name: "Delete me", Project: "/workspace/deleted", Manifest: []byte("name = \"deleted\"\n"), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.EnrollPeer("stable-node"); err != nil {
		t.Fatal(err)
	}
	tombstone, err := source.DeleteAgentpack(pack.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := source.CollectGarbage()
	if err != nil || len(before) != 0 {
		t.Fatalf("garbage collected before acknowledgement: %v %v", before, err)
	}
	inventory, err := source.Inventory()
	if err != nil || len(inventory.Objects) != 0 || len(inventory.Tombstones) != 1 {
		t.Fatalf("deleted inventory still advertises objects: %#v %v", inventory, err)
	}
	destination, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	active, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = active.Capture(CaptureInput{ID: pack.ID, Name: pack.Name, Project: "/workspace/deleted", Manifest: []byte("name = \"deleted\"\n"), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	activeInventory, err := active.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := destination.ImportObjects(exportInventory(t, active, activeInventory)); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.ImportObjects(exportInventory(t, source, inventory)); err != nil {
		t.Fatal(err)
	}
	if packs, err := destination.ListAgentpacks(); err != nil || len(packs) != 0 {
		t.Fatalf("tombstone did not hide imported pack: %#v %v", packs, err)
	}
	if err := source.Acknowledge("stable-node", []string{tombstone.ID}); err != nil {
		t.Fatal(err)
	}
	removed, err := source.CollectGarbage()
	if err != nil || len(removed) == 0 {
		t.Fatalf("garbage collection after acknowledgement: %v %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(source.root, tombstonesDir, tombstone.ID+".json")); err != nil {
		t.Fatalf("tombstone removed before newer peers can learn it: %v", err)
	}
}

func TestConcurrentReplicasRetainTwoHeads(t *testing.T) {
	base, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pack, _, err := base.Capture(CaptureInput{Name: "Branches", Project: "/workspace/branches", Manifest: []byte("name = \"branches\"\n"), Lock: lockWithCommit("base123")})
	if err != nil {
		t.Fatal(err)
	}
	baseInventory, err := base.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	left, right := mustStore(t), mustStore(t)
	for _, target := range []*Store{left, right} {
		if _, err := target.ImportObjects(exportInventory(t, base, baseInventory)); err != nil {
			t.Fatal(err)
		}
	}
	manifest := []byte("name = \"branches\"\n")
	if _, _, err := left.Capture(CaptureInput{ID: pack.ID, Project: "/workspace/branches", Manifest: manifest, Lock: lockWithCommit("left123")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := right.Capture(CaptureInput{ID: pack.ID, Project: "/workspace/branches", Manifest: manifest, Lock: lockWithCommit("right123")}); err != nil {
		t.Fatal(err)
	}
	merge := mustStore(t)
	for _, source := range []*Store{left, right} {
		inventory, err := source.Inventory()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := merge.ImportObjects(exportInventory(t, source, inventory)); err != nil {
			t.Fatal(err)
		}
	}
	heads, err := merge.Heads(pack.ID)
	if err != nil || len(heads) != 2 {
		t.Fatalf("concurrent revisions collapsed: %#v %v", heads, err)
	}
}

func TestDraftRemoteRevalidationAndDiscard(t *testing.T) {
	store := mustStore(t)
	pin := PackagePin{Commit: "abc123", CacheKey: "key", ContentHash: "sha256-tree-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	draft, err := store.SaveDraft(DraftInput{Item: "tool", Revision: strings.Repeat("a", 64), Node: "tailnet-node", Directory: "/offline/machine/project", Reason: "offline", PackID: strings.Repeat("b", 32), FolderIdentity: "folder-identity", ExpectedPins: map[string]PackagePin{"github.com/acme/tool": pin}, NeededHolder: "build-node"})
	if err != nil {
		t.Fatal(err)
	}
	diff := &DiffResult{Add: []string{"github.com/acme/tool"}, Update: []string{}, Remove: []string{}}
	state := DraftState{Reachable: false, NeededHolder: "build-node"}
	review, err := store.RevalidateDraft(draft.ID, state)
	if err != nil || review.Ready || !strings.Contains(review.Reason, "build-node") {
		t.Fatalf("offline draft state: %#v %v", review, err)
	}
	state = DraftState{Reachable: true, FolderIdentity: "folder-identity", Pins: map[string]PackagePin{"github.com/acme/tool": pin}, CurrentDiff: diff}
	review, err = store.RevalidateDraft(draft.ID, state)
	if err != nil || !review.Ready || review.Diff == nil {
		t.Fatalf("ready draft state: %#v %v", review, err)
	}
	state.Pins["github.com/acme/tool"] = PackagePin{Commit: "different", CacheKey: "key"}
	review, err = store.RevalidateDraft(draft.ID, state)
	if err != nil || !review.Ready || review.PinsMatch || !strings.Contains(review.Reason, "refreshed diff") {
		t.Fatalf("changed pins draft state: %#v %v", review, err)
	}
	if _, err := store.GetDraft(draft.ID); err != nil {
		t.Fatalf("draft not available for review: %v", err)
	}
	if err := store.DeleteDraft(draft.ID); err != nil {
		t.Fatal(err)
	}
	if drafts, err := store.ListDrafts(); err != nil || len(drafts) != 0 {
		t.Fatalf("draft discard failed: %#v %v", drafts, err)
	}
}

func mustStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}
