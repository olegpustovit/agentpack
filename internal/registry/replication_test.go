package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/olegpustovit/agentpack/internal/state"
	"strings"
	"sync"
	"testing"
)

func registryChanges(t *testing.T, s *Store) []state.Change {
	t.Helper()
	changes, _, err := s.sql.Changes(t.Context(), 0, nil, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

func applyRegistryChanges(t *testing.T, to *Store, changes []state.Change) {
	t.Helper()
	if err := ValidateChanges(to.sql, changes); err != nil {
		t.Fatal(err)
	}
	if err := to.sql.Apply(t.Context(), changes); err != nil {
		t.Fatal(err)
	}
}

func copyRegistry(t *testing.T, from, to *Store) {
	t.Helper()
	applyRegistryChanges(t, to, registryChanges(t, from))
}

func publicBodies(t *testing.T, s *Store) [][]byte {
	t.Helper()
	ids, err := s.objectIDs()
	if err != nil {
		t.Fatal(err)
	}
	var bodies [][]byte
	for _, id := range ids {
		body, err := s.objectData(id)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
	}
	return bodies
}

func TestStateReplicationAndRedaction(t *testing.T) {
	source, target := mustStore(t), mustStore(t)
	pack, revision, err := source.Capture(CaptureInput{Name: "Shared", Project: "/workspace/shared", Manifest: []byte(manifestTemplate), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	copyRegistry(t, source, target)
	got, revisions, err := target.Get(pack.ID)
	if err != nil || got.ID != pack.ID || len(revisions) != 1 || revisions[0].ID != revision.ID {
		t.Fatalf("replica: %+v %+v %v", got, revisions, err)
	}
	if _, _, err := target.restoreBytes(revisions[0]); err == nil {
		t.Fatal("raw local bindings replicated")
	}
	body, _ := json.Marshal(registryChanges(t, source))
	for _, secret := range []string{"env-secret", "arg-secret", "url-secret", "header-secret", "user:pass", "ghp_123"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("replicated rows contain %q", secret)
		}
	}
}

func TestAgentpackNameReplicatesAndRenameConverges(t *testing.T) {
	source, target := mustStore(t), mustStore(t)
	pack, _, err := source.Capture(CaptureInput{Name: "Shared profile", Project: "/workspace/demo", SourceNode: "source-node", Manifest: []byte("name='demo'\n"), Lock: []byte("lockfile_version=2\n")})
	if err != nil {
		t.Fatal(err)
	}
	copyRegistry(t, source, target)
	got, revisions, err := target.Get(pack.ID)
	if err != nil || got.Name != "Shared profile" || len(revisions) != 1 {
		t.Fatalf("name: %+v %v", got, err)
	}
	if _, err := source.Rename(pack.ID, "Renamed profile"); err != nil {
		t.Fatal(err)
	}
	copyRegistry(t, source, target)
	got, revisions, err = target.Get(pack.ID)
	if err != nil || got.Name != "Renamed profile" || len(revisions) != 1 || len(got.Heads) != 1 {
		t.Fatalf("renamed: %+v %v", got, err)
	}
	if _, err := source.Rename(pack.ID, "Bearer should-not-publish"); err == nil {
		t.Fatal("credential-shaped name accepted")
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
		t.Fatalf("credentials changed public digest: %s %s", first.ID, second.ID)
	}
	for _, body := range publicBodies(t, store) {
		for _, secret := range []string{"url-secret", "other-query-secret", "user:pass", "other-user:other-pass"} {
			if strings.Contains(string(body), secret) {
				t.Fatalf("public object contains %q", secret)
			}
		}
	}
}

func TestCaptureRedactsCredentialShapedSourceMetadataBeforeHashing(t *testing.T) {
	store := mustStore(t)
	pack, revision, err := store.Capture(CaptureInput{Name: "Metadata", Label: "access_token=label-secret", SourceNode: "Bearer node-secret", Project: "/workspace/token=project-secret", Manifest: []byte("name='metadata'\n"), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	if revision.Project != "<local>" || revision.SourceNode != "<local>" || revision.Label != "<local>" {
		t.Fatalf("metadata was not redacted: %+v", revision)
	}
	for _, body := range publicBodies(t, store) {
		for _, secret := range []string{"label-secret", "node-secret", "project-secret"} {
			if strings.Contains(string(body), secret) {
				t.Fatalf("public object contains %q", secret)
			}
		}
	}
	if _, _, err := store.Get(pack.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryChangesRejectChangedDigestAndMalformedObjects(t *testing.T) {
	source, target := mustStore(t), mustStore(t)
	for _, body := range []string{`{"schema":1,"module":"github.com/acme/tool"}`, "not json"} {
		sum := sha256.Sum256([]byte(body))
		id := hex.EncodeToString(sum[:])
		if err := source.storeObject("registry_objects", id, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateChanges(target.sql, registryChanges(t, source)); err == nil {
		t.Fatal("malformed object accepted")
	}
	source = mustStore(t)
	if _, _, err := source.Capture(CaptureInput{Name: "Digest", Project: "/workspace/digest", Manifest: []byte("name='digest'\n"), Lock: lockWithCommit("abc123")}); err != nil {
		t.Fatal(err)
	}
	changes := registryChanges(t, source)
	for i := range changes {
		if changes[i].Table == "registry_objects" && changes[i].Column == "body" {
			changes[i].Value.Data = json.RawMessage(`"{}"`)
			break
		}
	}
	if err := ValidateChanges(target.sql, changes); err == nil || !strings.Contains(err.Error(), "object") {
		t.Fatalf("changed digest accepted: %v", err)
	}
	if ids, err := target.objectIDs(); err != nil || len(ids) != 0 {
		t.Fatalf("rejected page changed objects: %v %v", ids, err)
	}
}

func TestTombstoneAndCollectionPropagateToOfflineAndNewPeers(t *testing.T) {
	source, target, fresh := mustStore(t), mustStore(t), mustStore(t)
	pack, _, err := source.Capture(CaptureInput{Name: "Delete me", Project: "/workspace/deleted", Manifest: []byte("name='deleted'\n"), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	copyRegistry(t, source, target)
	stale := registryChanges(t, target)
	if err := source.EnrollPeer("offline-peer"); err != nil {
		t.Fatal(err)
	}
	tombstone, err := source.DeleteAgentpack(pack.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Collection is database-only work: it runs while another writer holds the registry file lock.
	var removed []string
	if err := source.withLock(func() (err error) {
		removed, err = source.CollectGarbage()
		return err
	}); err != nil || len(removed) == 0 {
		t.Fatalf("collection: %v %v", removed, err)
	}
	for _, to := range []*Store{target, fresh} {
		copyRegistry(t, source, to)
		if _, _, err := to.Get(pack.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("deleted pack returned", err)
		}
	}
	applyRegistryChanges(t, source, stale)
	if _, _, err := source.Get(pack.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("stale peer resurrected a pack", err)
	}
	if _, err := source.readTombstone(tombstone.ID); err != nil {
		t.Fatal("tombstone lost", err)
	}
}

func TestConcurrentReplicasRetainTwoHeads(t *testing.T) {
	base, left, right, merged := mustStore(t), mustStore(t), mustStore(t), mustStore(t)
	pack, _, err := base.Capture(CaptureInput{Name: "Branches", Project: "/workspace/branches", Manifest: []byte("name='branches'\n"), Lock: lockWithCommit("base123")})
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []*Store{left, right} {
		copyRegistry(t, base, to)
	}
	for commit, store := range map[string]*Store{"left123": left, "right123": right} {
		if _, _, err := store.Capture(CaptureInput{ID: pack.ID, Project: "/workspace/branches", Manifest: []byte("name='branches'\n"), Lock: lockWithCommit(commit)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, from := range []*Store{left, right} {
		copyRegistry(t, from, merged)
	}
	if heads, err := merged.Heads(pack.ID); err != nil || len(heads) != 2 {
		t.Fatalf("concurrent heads: %v %v", heads, err)
	}
}

func TestRevisionLoadsAcrossEncoderLayoutsButNotUnredacted(t *testing.T) {
	store := mustStore(t)
	pack, revision, err := store.Capture(CaptureInput{Name: "Shared", Project: "/workspace/shared", Manifest: []byte(manifestTemplate), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	older := revision
	older.Manifest = strings.ReplaceAll(revision.Manifest, "\n[", "\n\n[")
	if older.Manifest == revision.Manifest {
		t.Fatal("fixture did not change layout")
	}
	data, err := marshalObject(older)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeImported(data); err != nil {
		t.Fatal("older layout rejected", err)
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	if err := store.storeObject("registry_objects", id, data); err != nil {
		t.Fatal(err)
	}
	if _, err := store.sql.DB.Exec("DELETE FROM registry_objects WHERE digest=?", revision.ID); err != nil {
		t.Fatal(err)
	}
	if read, err := store.readRevision(id); err != nil || read.Manifest != older.Manifest {
		t.Fatalf("older layout: %+v %v", read, err)
	}
	if _, again, err := store.Capture(CaptureInput{ID: pack.ID, Project: "/workspace/shared", Manifest: []byte(older.Manifest), Lock: []byte(older.Lock)}); err != nil || again.ID != id {
		t.Fatalf("layout difference made a revision: %+v %v", again, err)
	}
	leaked := revision
	leaked.Manifest += "\n[mcp.servers.leak.env]\nKEY='plain'\n"
	data, err = marshalObject(leaked)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeImported(data); err == nil || !strings.Contains(err.Error(), "unredacted") {
		t.Fatalf("unredacted data accepted: %v", err)
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

func TestReplicatedDraftKeepsItsTargetAcrossLocalAliases(t *testing.T) {
	a, b := mustStore(t), mustStore(t)
	draft, err := a.SaveDraft(DraftInput{Item: "tool", Node: "local", Directory: "/only-on-a", Reason: "offline", FolderIdentity: "folder", ExpectedPins: map[string]PackagePin{}})
	if err != nil || draft.Node != "local" {
		t.Fatalf("local save: %+v %v", draft, err)
	}
	if err := a.sql.BindNode(t.Context(), "node-a"); err != nil {
		t.Fatal(err)
	}
	copyRegistry(t, a, b)
	remote, err := b.GetDraft(draft.ID)
	if err != nil || remote.Node != "node-a" {
		t.Fatalf("draft retargeted to receiver: %+v %v", remote, err)
	}
	local, err := a.GetDraft(draft.ID)
	if err != nil || local.Node != "local" {
		t.Fatalf("local alias lost: %+v %v", local, err)
	}
}

func TestDraftTargetBindingAndSavesAreAtomic(t *testing.T) {
	store := mustStore(t)
	var writers sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		writers.Go(func() {
			_, err := store.SaveDraft(DraftInput{Item: "tool", Node: "local", Directory: "/local", Reason: "offline", FolderIdentity: "folder"})
			failures <- err
		})
	}
	if err := store.sql.BindNode(t.Context(), "stable-node"); err != nil {
		t.Fatal(err)
	}
	writers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count, wrong int
	if err := store.sql.Reader.QueryRow("SELECT count(*),coalesce(sum(json_extract(body,'$.node')<>'stable-node'),0) FROM registry_drafts").Scan(&count, &wrong); err != nil || count != 8 || wrong != 0 {
		t.Fatalf("draft target raced identity binding: count=%d wrong=%d err=%v", count, wrong, err)
	}
}

func mustStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

// Stored revisions outlive the encoder that wrote them: go-toml 2.4 lays out empty sub-tables without
// the blank lines 2.3 wrote, and that made every older revision unreadable.
