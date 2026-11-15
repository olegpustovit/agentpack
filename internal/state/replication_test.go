package state

import "testing"

func TestReplicatedTablesTrackChangesAndLocalTablesStayLocal(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for table, statement := range map[string]string{
		"users":                "INSERT INTO users(email,password_hash) VALUES ('member@example.com','hash')",
		"node_settings":        "INSERT INTO node_settings(node_id,key,value) VALUES ('node','sharing','{}')",
		"sessions":             "INSERT INTO sessions(id,name) VALUES ('session','Chat')",
		"conversation_events":  "INSERT INTO conversation_events(session_id,seq,body) VALUES ('session',1,'{}')",
		"limits":               "INSERT INTO limits(node_id,harness,snapshot) VALUES ('node','codex','{}')",
		"mesh_settings":        "INSERT INTO mesh_settings(key,value) VALUES ('main_node','node')",
		"mesh_nodes":           "INSERT INTO mesh_nodes(node_id,name) VALUES ('node','Machine')",
		"issues":               "INSERT INTO issues(fingerprint,title,sample) VALUES ('error','Failed','{}')",
		"issue_counts":         "INSERT INTO issue_counts(fingerprint,node_id,count) VALUES ('error','node',2)",
		"archive_event_clocks": `INSERT INTO archive_event_clocks(pk,clock) VALUES (x'010901','{"cl":1,"version":1,"site":"AAAAAAAAAAAAAAAAAAAAAA=="}')`,
	} {
		if _, err := store.DB.Exec(statement); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		var count int
		if err := store.Reader.QueryRow(`SELECT count(*) FROM crsql_changes WHERE "table"=?`, table).Scan(&count); err != nil || count == 0 {
			t.Fatalf("%s has no tracked changes: %d %v", table, count, err)
		}
	}
	var local int
	if err := store.Reader.QueryRow(`SELECT count(*) FROM crsql_changes WHERE "table" IN ('schema_meta','auth_sessions','oauth_clients','oauth_tokens','local_node','local_node_aliases','issue_log_cursors')`).Scan(&local); err != nil || local != 0 {
		t.Fatalf("private state became replicated: %d %v", local, err)
	}
	if _, err := store.DB.Exec("UPDATE sessions SET archived_at=123 WHERE id='session'"); err != nil {
		t.Fatal(err)
	}
	var archived int
	if err := store.Reader.QueryRow(`SELECT val FROM crsql_changes WHERE "table"='sessions' AND cid='archived_at'`).Scan(&archived); err != nil || archived != 123 {
		t.Fatalf("altered column was not tracked: %d %v", archived, err)
	}
}
