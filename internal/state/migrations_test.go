package state

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
		"conversation_truncated": false, "archived_at": false, "archived_seq": false, "title": false, "goal": false, "subagents": false,
		"model": false, "turns": false, "output_tokens": false, "input_tokens": false,
		"cache_read_tokens": false, "cache_write_tokens": false, "cost_micros": false,
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
