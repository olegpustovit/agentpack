package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"testing"
)

func TestReplicatedTablesTrackChangesAndLocalTablesStayLocal(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for table, statement := range map[string]string{
		"users":                "INSERT INTO users(email,password_hash) VALUES ('member@example.com','hash')",
		"node_settings":        "INSERT INTO node_settings(node_id,key,value) VALUES ('node','sharing','{}')",
		"sessions":             "INSERT INTO sessions(id,name,keep_native_subagents) VALUES ('session','Chat',1)",
		"conversation_events":  "INSERT INTO conversation_events(session_id,seq,body) VALUES ('session',1,'{}')",
		"limits":               "INSERT INTO limits(node_id,harness,snapshot) VALUES ('node','codex','{}')",
		"mesh_settings":        "INSERT INTO mesh_settings(key,value) VALUES ('main_node','node')",
		"mesh_nodes":           "INSERT INTO mesh_nodes(node_id,name) VALUES ('node','Machine')",
		"issues":               "INSERT INTO issues(fingerprint,title,sample) VALUES ('error','Failed','{}')",
		"issue_counts":         "INSERT INTO issue_counts(fingerprint,node_id,count) VALUES ('error','node',2)",
		"archive_event_clocks": `INSERT INTO archive_event_clocks(pk,clock) VALUES (x'010901','{"cl":1,"version":1,"site":"AAAAAAAAAAAAAAAAAAAAAA=="}')`,
		// Web Push keys and subscriptions replicate on purpose, secrets included: Main sends to every device.
		"push_keys":          "INSERT INTO push_keys(public_key,private_key,created_at) VALUES ('public','private',1)",
		"push_subscriptions": "INSERT INTO push_subscriptions(endpoint,email,p256dh,auth,public_key,created_node) VALUES ('https://push.example/1','member@example.com','key','secret','public','node')",
		"agent_defaults":     `INSERT INTO agent_defaults(key,value) VALUES ('replace_subagents',0)`,
		"omp_settings":       `INSERT INTO omp_settings(key,value) VALUES ('edit.mode','"patch"')`,
		// Agent sign-ins replicate on purpose, secrets included: one sign-in serves every node.
		"agent_credentials": `INSERT INTO agent_credentials(key,value) VALUES ('agy/token','{}')`,
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

func TestReplicationVersionIsPinned(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	current := func() int {
		t.Helper()
		version, err := replicationVersion(t.Context(), store.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return version
	}
	base := current()
	if base != ReplicationVersion {
		t.Fatalf("replicated columns changed: set ReplicationVersion = %d", base)
	}
	if _, err := store.DB.Exec("CREATE TABLE local_only(id INTEGER PRIMARY KEY, note TEXT)"); err != nil {
		t.Fatal(err)
	}
	if current() != base {
		t.Fatal("a local-only table changed the replication version")
	}
	if _, err := store.DB.Exec("SELECT crsql_begin_alter('users'); ALTER TABLE users ADD COLUMN nickname TEXT NOT NULL DEFAULT ''; SELECT crsql_commit_alter('users')"); err != nil {
		t.Fatal(err)
	}
	if current() == base {
		t.Fatal("a replicated column did not change the replication version")
	}
}

func replicationVersion(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT t.name,c.name,c.type,c."notnull",coalesce(c.dflt_value,''),c.pk
 FROM sqlite_schema t JOIN pragma_table_info(t.name) c
 WHERE t.type='table' AND EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name=t.name||'__crsql_clock')
 ORDER BY t.name,c.name`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	digest := sha256.New()
	for rows.Next() {
		var table, column, kind, defaultValue string
		var notNull, pk int
		if err := rows.Scan(&table, &column, &kind, &notNull, &defaultValue, &pk); err != nil {
			return 0, err
		}
		fmt.Fprintf(digest, "%s\x00%s\x00%s\x00%d\x00%s\x00%d\n", table, column, kind, notNull, defaultValue, pk)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Bit 30 keeps the value apart from the migration counts older peers send, which they read as Needs update.
	return int(binary.BigEndian.Uint32(digest.Sum(nil))&0x3fffffff | 0x40000000), nil
}
