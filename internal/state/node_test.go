package state

import (
	"strings"
	"testing"
)

func TestLocalNodeIdentityIsDurableDistinctAndBindsOnce(t *testing.T) {
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	old, err := a.NodeID(t.Context())
	if err != nil || !strings.HasPrefix(old, "site:") {
		t.Fatalf("offline identity: %q %v", old, err)
	}
	other, err := b.NodeID(t.Context())
	if err != nil || old == other {
		t.Fatalf("machines share an identity: %q %q %v", old, other, err)
	}
	for _, query := range []string{
		"INSERT INTO sessions(id,node_id,name) VALUES ('session'," + LocalNodeSQL + ",'Keep label')",
		"INSERT INTO node_settings(node_id,key,value) VALUES (" + LocalNodeSQL + ",'sharing','Keep setting')",
		"INSERT INTO limits(node_id,harness,snapshot) VALUES (" + LocalNodeSQL + ",'codex','Keep limits')",
		"INSERT INTO issue_counts(fingerprint,node_id,count) VALUES ('error'," + LocalNodeSQL + ",7)",
	} {
		if _, err := a.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.BindNode(t.Context(), "stable-node"); err != nil {
		t.Fatal(err)
	}
	if err := a.BindNode(t.Context(), "stable-node"); err != nil {
		t.Fatal(err)
	}
	if err := a.BindNode(t.Context(), "different-node"); err == nil {
		t.Fatal("bound state was moved to a different node")
	}
	for _, table := range []string{"sessions", "node_settings", "limits", "issue_counts"} {
		var node string
		if err := a.Reader.QueryRow("SELECT node_id FROM " + table).Scan(&node); err != nil || node != "stable-node" {
			t.Fatalf("%s lost ownership: %q %v", table, node, err)
		}
	}
	var count, aliases int
	if err := a.Reader.QueryRow("SELECT count FROM issue_counts").Scan(&count); err != nil || count != 7 {
		t.Fatalf("binding changed issue count: %d %v", count, err)
	}
	if err := a.Reader.QueryRow("SELECT count(*) FROM local_node_aliases WHERE node_id IN ('local',?)", old).Scan(&aliases); err != nil || aliases != 2 {
		t.Fatalf("old diagnostic identities lost: %d %v", aliases, err)
	}
	if err := migrate(t.Context(), a.DB, migrationFiles); err != nil {
		t.Fatal(err)
	}
	if id, err := a.NodeID(t.Context()); err != nil || id != "stable-node" {
		t.Fatalf("repeat migration changed identity: %q %v", id, err)
	}
}
