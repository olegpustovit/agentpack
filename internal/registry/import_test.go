package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func legacyCopy(t *testing.T, from *Store, home string) []string {
	t.Helper()
	var files []string
	for table, dir := range map[string]string{"registry_objects": objectsDir, "registry_names": objectsDir, "registry_tombstones": tombstonesDir, "registry_heads": metadataDir, "registry_bindings": bindingsDir, "registry_drafts": draftsDir} {
		key := "digest"
		switch table {
		case "registry_heads":
			key = "agentpack_id"
		case "registry_bindings":
			key = "revision_id"
		case "registry_drafts":
			key = "id"
		}
		rows, err := from.sql.Reader.Query("SELECT " + key + ",body FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, body string
			if err := rows.Scan(&id, &body); err != nil {
				t.Fatal(err)
			}
			if table == "registry_drafts" {
				draft, err := from.GetDraft(id)
				if err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(draft)
				if err != nil {
					t.Fatal(err)
				}
				body = string(data)
			}
			path := filepath.Join(home, "registry", dir, id+".json")
			if err := writeAtomic(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			files = append(files, path)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return files
}

func TestLegacyRegistryImportVerifiesBeforeRemovingFiles(t *testing.T) {
	source := mustStore(t)
	pack, revision, err := source.Capture(CaptureInput{Name: "Legacy", Project: "/legacy/project", Manifest: []byte(manifestTemplate), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := source.SaveDraft(DraftInput{Item: "tool", Node: "local", Directory: "/offline", Reason: "offline", FolderIdentity: "folder", ExpectedPins: map[string]PackagePin{}})
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	files := legacyCopy(t, source, home)
	corrupt := filepath.Join(home, "registry", objectsDir, revision.ID+".json")
	original, err := os.ReadFile(corrupt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrupt, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if store, err := New(home); err == nil {
		store.Close()
		t.Fatal("corrupt legacy tree accepted")
	}
	for _, file := range files {
		if _, err := os.Stat(file); err != nil {
			t.Fatal("failed import removed a source", err)
		}
	}
	if err := os.WriteFile(corrupt, original, 0600); err != nil {
		t.Fatal(err)
	}
	imported, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := imported.Close(); err != nil {
			t.Error(err)
		}
	})
	got, revisions, err := imported.Get(pack.ID)
	if err != nil || got.Name != "Legacy" || len(revisions) != 1 || revisions[0].ID != revision.ID {
		t.Fatalf("import: %+v %+v %v", got, revisions, err)
	}
	if got, err := imported.GetDraft(draft.ID); err != nil || got.Node != "local" {
		t.Fatal("draft lost its local target", got.Node, err)
	}
	if _, _, err := imported.restoreBytes(revisions[0]); err != nil {
		t.Fatal("local bindings lost", err)
	}
	for _, file := range files {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("imported source retained: %s %v", file, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "registry", objectsDir)); !os.IsNotExist(err) {
		t.Fatalf("legacy object directory retained: %v", err)
	}
	changes, _, err := imported.sql.Changes(t.Context(), 0, nil, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(changes)
	if strings.Contains(string(body), "env-secret") {
		t.Fatal("local binding became replicated")
	}
}

func TestLegacyPeerApprovalsStayLocalAndRemovedMachinesStayRemoved(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "registry", peersDir, "state.json")
	if err := writeAtomic(path, []byte(`{"peers":["legacy","trusted"],"mesh":["trusted"],"left":["removed"],"receipts":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	peers, err := store.ListPeers()
	if err != nil || len(peers) != 2 || peers[0].Mesh || !peers[1].Mesh {
		t.Fatalf("legacy approvals changed: %+v %v", peers, err)
	}
	if err := store.EnrollOwnMachine("removed"); err != nil {
		t.Fatal(err)
	}
	if peers, err := store.ListPeers(); err != nil || len(peers) != 2 {
		t.Fatalf("removed peer returned: %+v %v", peers, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("legacy peer file remains: %v", err)
	}
	other := mustStore(t)
	copyRegistry(t, store, other)
	if peers, err := other.ListPeers(); err != nil || len(peers) != 0 {
		t.Fatalf("approval replicated: %+v %v", peers, err)
	}
}
