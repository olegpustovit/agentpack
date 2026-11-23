package state

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMigrationsFreshAndRepeated(t *testing.T) {
	home := t.TempDir()
	store, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := store.Reader.QueryRow("SELECT version FROM schema_meta").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion() {
		t.Fatalf("schema %d, want %d", version, SchemaVersion())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	store, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("reopening changed the migrated database")
	}
}

func TestSessionsMigrationDefaultsAndEventIdentity(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	columns := map[string]bool{
		"id": false, "last_used": false, "folder_id": false, "name": false,
		"directory": false, "definition_root": false, "harness": false,
		"mode": false, "transport": false, "agent_session_id": false,
		"agent_capabilities": false, "conversation_state": false, "state": false,
		"started_at": false, "started_by": false, "node_id": false,
		"owner_email": false, "ended_at": false, "hidden": false,
		"last_seq": false, "last_event_at": false,
		"conversation_truncated": false, "archived_at": false, "archived_seq": false, "title": false, "goal": false, "subagents": false, "waiting": false, "parent_id": false, "tools": false, "worktree_of": false,
		"model": false, "turns": false, "output_tokens": false, "input_tokens": false,
		"cache_read_tokens": false, "cache_write_tokens": false, "cost_micros": false,
		"wanted_config": false,
	}
	rows, err := store.Reader.Query("PRAGMA table_info(sessions)")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&index, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if _, known := columns[name]; !known || notNull != 1 || (name != "id" && !defaultValue.Valid) {
			t.Fatalf("invalid session column: name=%s not_null=%d default=%v", name, notNull, defaultValue)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for name, found := range columns {
		if !found {
			t.Errorf("missing session column %s", name)
		}
	}
	if _, err := store.DB.Exec(`INSERT INTO sessions(id) VALUES ('one'), ('two');
		INSERT INTO conversation_events(session_id, seq) VALUES ('one', 1), ('two', 1), ('one', 2)`); err != nil {
		t.Fatal(err)
	}
	var hidden, endedAt int
	var owner, body string
	if err := store.Reader.QueryRow("SELECT hidden, ended_at, owner_email FROM sessions WHERE id='one'").Scan(&hidden, &endedAt, &owner); err != nil || hidden != 0 || endedAt != 0 || owner != "" {
		t.Fatalf("session defaults: hidden=%d ended_at=%d owner=%q err=%v", hidden, endedAt, owner, err)
	}
	if err := store.Reader.QueryRow("SELECT body FROM conversation_events WHERE session_id='one' AND seq=1").Scan(&body); err != nil || body != "" {
		t.Fatalf("event default: body=%q err=%v", body, err)
	}
	if _, err := store.DB.Exec("INSERT INTO conversation_events(session_id, seq) VALUES ('one', 1)"); err == nil {
		t.Fatal("duplicate event identity accepted")
	}
	if _, err := store.DB.Exec("INSERT INTO sessions(id) VALUES (NULL)"); err == nil {
		t.Fatal("NULL session identity accepted")
	}
}

func TestSessionsMigrationPreservesAccounts(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open(driverName, "file:"+filepath.ToSlash(filepath.Join(home, "state.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	previous := fstest.MapFS{}
	names, err := fs.Glob(migrationFiles, "migrations/000[1-7]_*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		previous[name] = &fstest.MapFile{Data: body}
	}
	if err := migrate(context.Background(), db, previous); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO users(email, password_hash, role) VALUES ('owner@example.com', 'unchanged-hash', 'owner')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var hash, role string
	if err := store.Reader.QueryRow("SELECT password_hash, role FROM users WHERE email='owner@example.com'").Scan(&hash, &role); err != nil || hash != "unchanged-hash" || role != "owner" {
		t.Fatalf("account changed during migration: hash=%q role=%q err=%v", hash, role, err)
	}
	if _, err := store.DB.Exec("INSERT INTO sessions(id) VALUES ('upgraded'); INSERT INTO conversation_events(session_id, seq) VALUES ('upgraded', 1)"); err != nil {
		t.Fatal(err)
	}
}

func TestConversationProgressMigrationPreservesSession(t *testing.T) {
	db, err := sql.Open(driverName, "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "state.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	previous := fstest.MapFS{}
	names, err := fs.Glob(migrationFiles, "migrations/000[1-8]_*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		previous[name] = &fstest.MapFile{Data: body}
	}
	if err := migrate(context.Background(), db, previous); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO sessions(id,name) VALUES ('existing','Keep this label')"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(context.Background(), db, migrationFiles); err != nil {
		t.Fatal(err)
	}
	var label string
	var lastSeq, lastEvent, truncated int64
	if err := db.QueryRow("SELECT name,last_seq,last_event_at,conversation_truncated FROM sessions WHERE id='existing'").Scan(&label, &lastSeq, &lastEvent, &truncated); err != nil || label != "Keep this label" || lastSeq != 0 || lastEvent != 0 || truncated != 0 {
		t.Fatalf("progress upgrade changed session: name=%q seq=%d last_event=%d truncated=%d err=%v", label, lastSeq, lastEvent, truncated, err)
	}
}

func TestPushMigrationKeepsSubscriptionsWithTheirKey(t *testing.T) {
	db, err := sql.Open(driverName, "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "state.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	previous := fstest.MapFS{}
	names, err := fs.Glob(migrationFiles, "migrations/00[0-2][0-9]_*.sql")
	if err != nil {
		t.Fatal(err)
	}
	names = append(names, "migrations/0030_push.sql")
	for _, name := range names {
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		previous[name] = &fstest.MapFile{Data: body}
	}
	if err := migrate(context.Background(), db, previous); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO push_keys(id,public_key,private_key) VALUES (1,'public','private');
 INSERT INTO push_subscriptions(endpoint,email,p256dh,auth,created_at) VALUES ('https://push.example/1','owner@example.com','key','secret',5)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(context.Background(), db, migrationFiles); err != nil {
		t.Fatal(err)
	}
	var key, node, private string
	var created int64
	if err := db.QueryRow(`SELECT s.public_key,s.created_node,s.created_at,k.private_key FROM push_subscriptions s JOIN push_keys k USING(public_key)
 WHERE s.endpoint='https://push.example/1' AND s.created_node=(SELECT node_id FROM local_node WHERE id=1)`).Scan(&key, &node, &created, &private); err != nil || key != "public" || private != "private" || created != 5 {
		t.Fatalf("subscription lost its key: %q %q %d %v", key, node, created, err)
	}
}

func TestBrokenMigrationRollsBack(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := fstest.MapFS{}
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		data, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source[name] = &fstest.MapFile{Data: data}
	}
	broken := fmt.Sprintf("migrations/%04d_broken.sql", SchemaVersion()+1)
	source[broken] = &fstest.MapFile{Data: []byte("CREATE TABLE partial(body TEXT); INSERT INTO missing VALUES ('fail');")}
	if err := migrate(context.Background(), store.DB, source); err == nil {
		t.Fatal("broken migration succeeded")
	}
	var count int
	if err := store.Reader.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='partial'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial migration survived: %d %v", count, err)
	}
	var version int
	if err := store.Reader.QueryRow("SELECT version FROM schema_meta").Scan(&version); err != nil || version != SchemaVersion() {
		t.Fatalf("failed migration advanced schema: %d %v", version, err)
	}
	source[broken] = &fstest.MapFile{Data: []byte("CREATE TABLE recovered(body TEXT);")}
	if err := migrate(context.Background(), store.DB, source); err != nil {
		t.Fatal(err)
	}
	if err := store.Reader.QueryRow("SELECT version FROM schema_meta").Scan(&version); err != nil || version != SchemaVersion()+1 {
		t.Fatalf("retry failed: %d %v", version, err)
	}
}

func TestConcurrentMigrationOpen(t *testing.T) {
	home := t.TempDir()
	results := make(chan error, 2)
	for range 2 {
		go func() {
			s, err := Open(home)
			if err == nil {
				err = s.Close()
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestAheadSchemaIsNotChanged(t *testing.T) {
	home := t.TempDir()
	store, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec("UPDATE schema_meta SET version = ?", SchemaVersion()+1); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if next, err := Open(home); err == nil {
		next.Close()
		t.Fatal("accepted a newer schema")
	}
	db, err := sql.Open(driverName, "file:"+filepath.ToSlash(filepath.Join(home, "state.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("SELECT version FROM schema_meta").Scan(&version); err != nil || version != SchemaVersion()+1 {
		t.Fatalf("newer schema was changed: %d %v", version, err)
	}
}

func TestReplicatedAlterMigrationMarkers(t *testing.T) {
	files, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	enroll := regexp.MustCompile(`(?i)\bcrsql_as_crr\s*\(\s*'([a-z0-9_]+)'\s*\)`)
	alter := regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+["\x60\[]?([a-z0-9_]+)`)
	replicated := map[string]bool{}
	for _, file := range files {
		body, err := migrationFiles.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		first, _, _ := strings.Cut(string(body), "\n")
		sqlText := regexp.MustCompile(`(?m)--[^\n]*`).ReplaceAllString(string(body), "")
		for _, statement := range strings.Split(sqlText, ";") {
			for _, match := range alter.FindAllStringSubmatch(statement, -1) {
				table := strings.ToLower(match[1])
				if replicated[table] && first != "-- crsql-alter: "+table {
					t.Errorf("%s alters replicated table %s without its first-line marker", file, table)
				}
			}
			for _, match := range enroll.FindAllStringSubmatch(statement, -1) {
				replicated[strings.ToLower(match[1])] = true
			}
		}
	}
}

// Readers filter events by kind through the derived column and its index, never by parsing every body.
func TestConversationEventKindIsIndexed(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec(`INSERT INTO conversation_events(session_id,seq,body) VALUES
		('s',1,'{"seq":1,"kind":"prompt","payload":{}}'),('s',2,'not json'),('s',3,'')`); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	rows, err := store.Reader.Query("SELECT coalesce(kind,'-') FROM conversation_events WHERE session_id='s' ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
	}
	if strings.Join(kinds, ",") != "prompt,-,-" {
		t.Fatalf("kinds = %v", kinds)
	}
	var plan strings.Builder
	rows, err = store.Reader.Query("EXPLAIN QUERY PLAN SELECT seq FROM conversation_events WHERE session_id='s' AND kind='prompt' ORDER BY seq DESC LIMIT 1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if !strings.Contains(plan.String(), "conversation_events_kind") {
		t.Fatalf("plan does not use the kind index:\n%s", plan.String())
	}
}
