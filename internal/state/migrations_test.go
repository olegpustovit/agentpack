package state

import (
	"context"
	"database/sql"
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

func TestBrokenMigrationRollsBack(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	initial, err := migrationFiles.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{
		"migrations/0001_init.sql":   &fstest.MapFile{Data: initial},
		"migrations/0002_broken.sql": &fstest.MapFile{Data: []byte("CREATE TABLE partial(body TEXT); INSERT INTO missing VALUES ('fail');")},
	}
	if err := migrate(context.Background(), store.DB, source); err == nil {
		t.Fatal("broken migration succeeded")
	}
	var count int
	if err := store.Reader.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='partial'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial migration survived: %d %v", count, err)
	}
	var version int
	if err := store.Reader.QueryRow("SELECT version FROM schema_meta").Scan(&version); err != nil || version != 1 {
		t.Fatalf("failed migration advanced schema: %d %v", version, err)
	}
	source["migrations/0002_broken.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE recovered(body TEXT);")}
	if err := migrate(context.Background(), store.DB, source); err != nil {
		t.Fatal(err)
	}
	if err := store.Reader.QueryRow("SELECT version FROM schema_meta").Scan(&version); err != nil || version != 2 {
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
