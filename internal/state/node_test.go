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
	if _, err := a.DB.Exec("CREATE TABLE future_state (other_node TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"INSERT INTO sessions(id,node_id,name) VALUES ('session'," + LocalNodeSQL + ",'Keep label')",
		"INSERT INTO node_settings(node_id,key,value) VALUES (" + LocalNodeSQL + ",'sharing','Keep setting')",
		"INSERT INTO limits(node_id,harness,snapshot) VALUES (" + LocalNodeSQL + ",'codex','Keep limits')",
		"INSERT INTO issue_counts(fingerprint,node_id,count) VALUES ('error'," + LocalNodeSQL + ",7)",
		"INSERT INTO agent_snapshots VALUES (" + LocalNodeSQL + ",'codex','/project','default','v1','{}',0)",
		"INSERT INTO mcp_credentials(id,created_node) VALUES ('credential'," + LocalNodeSQL + ")",
		"INSERT INTO push_subscriptions(endpoint,created_node) VALUES ('endpoint'," + LocalNodeSQL + ")",
		"INSERT INTO agent_tasks(id,role,from_session,from_node,to_session,to_node,state,created_at,updated_at) VALUES ('task','out','one'," + LocalNodeSQL + ",'two'," + LocalNodeSQL + ",'open',0,0)",
		"INSERT INTO issue_log_cursors VALUES ('day','error'," + LocalNodeSQL + ",'version',7)",
		"INSERT INTO mesh_nodes(node_id) VALUES (" + LocalNodeSQL + ")",
		"INSERT INTO registry_peers(node_id) VALUES (" + LocalNodeSQL + ")",
		"INSERT INTO cache_locations(cache_key,node_id) VALUES ('cache'," + LocalNodeSQL + ")",
		"INSERT INTO blob_locations(sha,node_id) VALUES ('blob'," + LocalNodeSQL + ")",
		"INSERT INTO usage_hourly(hour,node_id,harness) VALUES (0," + LocalNodeSQL + ",'codex')",
		"INSERT INTO limit_samples(hour,node_id,harness,window_id) VALUES (0," + LocalNodeSQL + ",'codex','window')",
		"INSERT INTO registry_drafts(id,body) VALUES ('draft',json_object('node'," + LocalNodeSQL + "))",
		"INSERT INTO future_state VALUES (" + LocalNodeSQL + ")",
		"INSERT INTO agent_credentials(key,value) VALUES ('credential',json_object('node'," + LocalNodeSQL + "))",
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
	for table, column := range map[string]string{
		"sessions": "node_id", "node_settings": "node_id", "limits": "node_id", "issue_counts": "node_id",
		"agent_snapshots": "node_id", "mcp_credentials": "created_node", "push_subscriptions": "created_node",
		"agent_tasks": "CASE WHEN from_node=to_node THEN from_node ELSE '' END", "issue_log_cursors": "node_id",
		"mesh_nodes": "node_id", "registry_peers": "node_id", "cache_locations": "node_id", "blob_locations": "node_id",
		"usage_hourly": "node_id", "limit_samples": "node_id", "local_node": "node_id",
		"future_state":    "other_node",
		"registry_drafts": "json_extract(body,'$.node')", "agent_credentials": "json_extract(value,'$.node')",
	} {
		var node string
		if err := a.Reader.QueryRow("SELECT " + column + " FROM " + table).Scan(&node); err != nil || node != "stable-node" {
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
