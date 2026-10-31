package registry

import (
	"strings"
	"testing"
	"time"
)

func snapshot(deps map[string]string) ([]byte, []byte) {
	manifest := "name = \"demo\"\n[dependencies]\n"
	lock := "lockfile_version = 3\n[meta]\nname = \"demo\"\nversion = \"0.0.1\"\n"
	for _, module := range []string{"github.com/acme/tool", "github.com/acme/left", "github.com/acme/right"} {
		commit, ok := deps[module]
		if !ok {
			continue
		}
		manifest += "\"" + module + "\" = { commit = \"" + commit + "\" }\n"
		lock += "[[packages]]\nmodule = \"" + module + "\"\nkind = \"skill\"\ncommit = \"" + commit + "\"\ncache_key = \"" + commit + "\"\ncontent_hash = \"sha256-tree-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"\n"
	}
	return []byte(manifest), []byte(lock)
}

// twoReplicas returns machines that share one revision and have since diverged.
func twoReplicas(t *testing.T, left, right map[string]string) (*Store, *Store, string) {
	t.Helper()
	a, b := newStore(t), newStore(t)
	manifest, lock := snapshot(map[string]string{"github.com/acme/tool": "base"})
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pack, _, err := a.Capture(CaptureInput{Name: "Demo", Project: "/a", SourceNode: "a", Manifest: manifest, Lock: lock, CreatedAt: start})
	if err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	manifest, lock = snapshot(left)
	if _, _, err := a.Capture(CaptureInput{ID: pack.ID, Project: "/a", SourceNode: "a", Manifest: manifest, Lock: lock, CreatedAt: start.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	manifest, lock = snapshot(right)
	if _, _, err := b.Capture(CaptureInput{ID: pack.ID, Project: "/b", SourceNode: "b", Manifest: manifest, Lock: lock, CreatedAt: start.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return a, b, pack.ID
}

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// exchange copies objects one way at a time, each side merging when it has them, as the gateway does.
func exchange(t *testing.T, stores ...*Store) {
	t.Helper()
	for _, from := range stores {
		inventory, err := from.Inventory()
		if err != nil {
			t.Fatal(err)
		}
		for _, to := range stores {
			if to == from {
				continue
			}
			if _, err := to.ImportObjects(exportInventory(t, from, inventory)); err != nil {
				t.Fatal(err)
			}
			if err := to.MergeHeads(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func headsOf(t *testing.T, store *Store, id string) []string {
	t.Helper()
	heads, err := store.Heads(id)
	if err != nil {
		t.Fatal(err)
	}
	return heads
}

func TestIndependentMergesOfCleanEditsConvergeOnOneObject(t *testing.T) {
	a, b, id := twoReplicas(t,
		map[string]string{"github.com/acme/tool": "base", "github.com/acme/left": "l1"},
		map[string]string{"github.com/acme/tool": "base", "github.com/acme/right": "r1"})
	// Both machines merge the same two heads without talking to each other.
	aInventory, _ := a.Inventory()
	bInventory, _ := b.Inventory()
	if _, err := b.ImportObjects(exportInventory(t, a, aInventory)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ImportObjects(exportInventory(t, b, bInventory)); err != nil {
		t.Fatal(err)
	}
	if err := a.MergeHeads(); err != nil {
		t.Fatal(err)
	}
	if err := b.MergeHeads(); err != nil {
		t.Fatal(err)
	}
	left, right := headsOf(t, a, id), headsOf(t, b, id)
	if len(left) != 1 || len(right) != 1 || left[0] != right[0] {
		t.Fatalf("heads did not converge: %v %v", left, right)
	}
	merged, err := a.readRevision(left[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"github.com/acme/left", "github.com/acme/right", "github.com/acme/tool"} {
		if !strings.Contains(merged.Lock, want) || !strings.Contains(merged.Manifest, want) {
			t.Fatalf("merge lost %s:\n%s\n%s", want, merged.Manifest, merged.Lock)
		}
	}
	if len(merged.PackageIDs) != 3 || len(merged.ParentIDs) != 2 {
		t.Fatalf("merge shape: %+v", merged)
	}
}

func TestARemovalMergesWithAnEditElsewhere(t *testing.T) {
	a, b, id := twoReplicas(t,
		map[string]string{},
		map[string]string{"github.com/acme/tool": "base", "github.com/acme/right": "r1"})
	exchange(t, a, b)
	if heads := headsOf(t, a, id); len(heads) != 1 {
		t.Fatalf("a removal and an edit elsewhere must merge: %v", heads)
	}
	merged, err := a.readRevision(headsOf(t, a, id)[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(merged.Manifest, "acme/tool") || !strings.Contains(merged.Manifest, "acme/right") {
		t.Fatalf("merged manifest:\n%s", merged.Manifest)
	}
}

func TestAConflictWaitsForAPersonAndTheirChoiceReplicates(t *testing.T) {
	a, b, id := twoReplicas(t,
		map[string]string{"github.com/acme/tool": "left"},
		map[string]string{"github.com/acme/tool": "right"})
	exchange(t, a, b)
	if heads := headsOf(t, a, id); len(heads) != 2 {
		t.Fatalf("a conflict must keep both heads: %v", heads)
	}
	conflict, err := a.Conflict(id)
	if err != nil || len(conflict.Fields) == 0 {
		t.Fatalf("conflict: %+v %v", conflict, err)
	}
	other, err := b.Conflict(id)
	if err != nil || len(other.Fields) != len(conflict.Fields) || other.Fields[0].Path != conflict.Fields[0].Path {
		t.Fatalf("machines disagree on the conflict: %+v %+v %v", conflict, other, err)
	}
	if _, err := a.Resolve(id, conflict.Heads, nil, "a"); err == nil {
		t.Fatal("resolving without a choice must fail")
	}

	// A launch during the conflict still captures, on its own line of work.
	manifest, lock := snapshot(map[string]string{"github.com/acme/tool": "right", "github.com/acme/right": "r2"})
	if _, _, err := b.Capture(CaptureInput{ID: id, Project: "/b", SourceNode: "b", Manifest: manifest, Lock: lock}); err != nil {
		t.Fatalf("capture during a conflict: %v", err)
	}
	if heads := headsOf(t, b, id); len(heads) != 2 {
		t.Fatalf("capture must continue one side: %v", heads)
	}
	exchange(t, a, b)
	conflict, err = a.Conflict(id)
	if err != nil || len(conflict.Fields) == 0 {
		t.Fatalf("conflict after capture: %+v %v", conflict, err)
	}

	choices := map[string]string{}
	var winner string
	for _, head := range conflict.Heads {
		revision, err := a.readRevision(head)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(revision.Manifest, "left") && !strings.Contains(revision.Manifest, "acme/left") {
			winner = head
		}
	}
	for _, field := range conflict.Fields {
		choices[field.Path] = winner
	}
	resolved, err := a.Resolve(id, conflict.Heads, choices, "a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resolved.Lock, "commit = 'left'") || !strings.Contains(resolved.Lock, "acme/right") {
		t.Fatalf("resolved lock:\n%s", resolved.Lock)
	}
	exchange(t, a, b)
	left, right := headsOf(t, a, id), headsOf(t, b, id)
	if len(left) != 1 || len(right) != 1 || left[0] != right[0] {
		t.Fatalf("resolution did not replicate: %v %v", left, right)
	}
	if _, err := a.Resolve(id, conflict.Heads, choices, "a"); err == nil {
		t.Fatal("resolving superseded heads must fail")
	}
}

func TestRemovedMachineStaysRemoved(t *testing.T) {
	store := newStore(t)
	if err := store.EnrollOwnMachine("node-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemovePeer("node-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnrollOwnMachine("node-1"); err != nil {
		t.Fatal(err)
	}
	if peers, _ := store.ListPeers(); len(peers) != 0 {
		t.Fatalf("automatic enrollment undid a removal: %+v", peers)
	}
	if err := store.EnrollPeer("node-1"); err != nil {
		t.Fatal(err)
	}
	if peers, _ := store.ListPeers(); len(peers) != 1 {
		t.Fatalf("explicit enrollment: %+v", peers)
	}
}
