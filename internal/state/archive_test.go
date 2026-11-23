package state

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestArchiveRoundTripIsPrivateDurableAndPlainSQLite(t *testing.T) {
	home := t.TempDir()
	db, err := OpenArchive(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO conversation_events(session_id,seq,body) VALUES ('session',1,?)", `{"text":"保存"}`); err != nil {
		t.Fatal(err)
	}
	var journal string
	var sync, extensions int
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal=%s %v", journal, err)
	}
	if err := db.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil || sync != 2 {
		t.Fatalf("sync=%d %v", sync, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name LIKE 'crsql_%'").Scan(&extensions); err != nil || extensions != 0 {
		t.Fatalf("archive loaded replication: %d %v", extensions, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArchive(home); err != nil {
		t.Fatal(err)
	}
	db, err = OpenArchive(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var body string
	if err := db.QueryRow("SELECT body FROM conversation_events WHERE session_id='session' AND seq=1").Scan(&body); err != nil || body != `{"text":"保存"}` {
		t.Fatalf("archive round trip: %q %v", body, err)
	}
	info, err := os.Stat(filepath.Join(home, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("archive permission: %v", info.Mode())
	}
	reader, err := ReadArchive(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.Exec("DELETE FROM conversation_events"); err == nil {
		t.Fatal("archive reader allowed writes")
	}
}

func TestArchiveRefusesUnrelatedDatabaseAndLinksWithoutChangingThem(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "archive.db")
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?agentpack_plain=1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE unrelated(body TEXT); INSERT INTO unrelated VALUES ('keep me')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if db, err := OpenArchive(home); err == nil {
		db.Close()
		t.Fatal("unrelated database accepted as an archive")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("refusing an invalid archive changed it", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("do not overwrite"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skip("symlinks unavailable", err)
	}
	if db, err := OpenArchive(home); err == nil {
		db.Close()
		t.Fatal("archive symlink accepted")
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "do not overwrite" {
		t.Fatal("symlink target changed", err)
	}
}

func TestArchiveRefusesReplicatedStateCopiedToArchivePath(t *testing.T) {
	source := t.TempDir()
	store, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(source, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(destination, "archive.db"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArchive(destination); err == nil {
		t.Fatal("replicated state database accepted as a plain archive")
	}
}

func TestOpenPlainNeverLoadsCRSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := OpenPlain(path, PlainOptions{Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode, sync string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || sync != "2" {
		t.Fatalf("journal_mode=%s synchronous=%s", mode, sync)
	}
	if _, err := db.Exec("SELECT crsql_site_id()"); err == nil {
		t.Fatal("plain database loaded cr-sqlite")
	}
	ro, err := OpenPlain(path, PlainOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Exec("CREATE TABLE t(x)"); err == nil {
		t.Fatal("read-only plain database accepted a write")
	}
}
