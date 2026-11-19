package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestExtensionLoaded(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	var version string
	if err := store.DB.QueryRow("SELECT sqlite_version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "3.54.1" {
		t.Fatalf("SQLite version %s differs from the extension source headers", version)
	}
	var siteID []byte
	if err := store.DB.QueryRow("SELECT crsql_site_id()").Scan(&siteID); err != nil {
		t.Fatal(err)
	}
	if len(siteID) != 16 {
		t.Fatalf("site ID has %d bytes, want 16", len(siteID))
	}
}

// The Go cache does not notice changed external C libraries. A source-pin path
// in the cgo flags changes the package/link action when the pin changes.
func TestLinkPathsTrackSourcePin(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(source), "../..")
	lock, err := os.ReadFile(filepath.Join(root, "third_party/crsqlite.lock"))
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		Commit string `toml:"commit"`
	}
	if err := toml.Unmarshal(lock, &pin); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(source), "crsqlite.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64", "windows_amd64"} {
		platform := strings.Split(target, "_")
		directive := fmt.Sprintf("#cgo %s,%s LDFLAGS: -L${SRCDIR}/../../.build/crsqlite/%s/%s -lcrsqlite", platform[0], platform[1], pin.Commit, target)
		if !strings.Contains(string(contents), directive) {
			t.Errorf("cgo link path must track source pin for %s; update crsqlite.go with the lock", target)
		}
	}
}

func TestOpenReopenAndDurability(t *testing.T) {
	name := "space #& ü"
	if runtime.GOOS != "windows" {
		name += "?"
	}
	home := filepath.Join(t.TempDir(), name)
	store, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store != nil {
			if err := store.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	for _, db := range []*sql.DB{store.DB, store.Reader} {
		for pragma, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "busy_timeout": "5000"} {
			var got string
			if err := db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("%s = %s, want %s", pragma, got, want)
			}
		}
	}
	if _, err := store.DB.Exec("CREATE TABLE marker (body TEXT); INSERT INTO marker VALUES ('persisted')"); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := store.Reader.QueryRow("SELECT body FROM marker").Scan(&body); err != nil || body != "persisted" {
		t.Fatalf("reader: %q %v", body, err)
	}
	tx, err := store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO marker VALUES ('uncommitted')"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var count int
	if err := store.Reader.QueryRowContext(ctx, "SELECT count(*) FROM marker").Scan(&count); err != nil || count != 1 {
		t.Fatalf("reader blocked or saw an uncommitted write: count=%d err=%v", count, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reader.Exec("INSERT INTO marker VALUES ('forbidden')"); err == nil {
		t.Fatal("reader pool allowed a write")
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
			info, err := os.Stat(filepath.Join(home, name))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0600 {
				t.Errorf("%s mode = %o", name, info.Mode().Perm())
			}
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	store, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reader.QueryRow("SELECT body FROM marker").Scan(&body); err != nil || body != "persisted" {
		t.Fatalf("reopen lost data: %q %v", body, err)
	}
	store.Reader.SetMaxIdleConns(0)
	for range 4 {
		var count int
		if err := store.Reader.QueryRow("SELECT count(*) FROM crsql_changes").Scan(&count); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.Reader.Stats().OpenConnections; got != 0 {
		t.Errorf("retired read connections still open: %d", got)
	}
}

func TestTwoStoresWriteWithoutLockErrors(t *testing.T) {
	home := t.TempDir()
	a, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := a.DB.Exec("CREATE TABLE writes (owner INTEGER, seq INTEGER, PRIMARY KEY(owner,seq))"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for owner, store := range []*Store{a, b} {
		go func() {
			for seq := range 30 {
				if _, err := store.DB.Exec("INSERT INTO writes VALUES (?,?)", owner, seq); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := a.Reader.QueryRow("SELECT count(*) FROM writes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 60 {
		t.Fatalf("lost writes: %d", count)
	}
}

func TestOpenRejectsDatabaseSymlink(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "untouched")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, "state.db")); err != nil {
		t.Skip("symlinks unavailable", err)
	}
	if store, err := Open(home); err == nil {
		store.Close()
		t.Fatal("followed state database symlink")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "keep" {
		t.Fatalf("symlink target changed: %q %v", got, err)
	}
}

func TestTransactionCommitsAndRollsBack(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec("CREATE TABLE tx_test (body TEXT)"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("abort transaction")
	for _, fail := range []bool{false, true} {
		err := Transaction(context.Background(), store.DB, func(tx *sql.Tx) error {
			if _, err := tx.Exec("INSERT INTO tx_test VALUES ('value')"); err != nil {
				return err
			}
			if fail {
				return failure
			}
			return nil
		})
		if fail && !errors.Is(err, failure) || !fail && err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := store.Reader.QueryRow("SELECT count(*) FROM tx_test").Scan(&count); err != nil || count != 1 {
		t.Fatalf("transaction count=%d err=%v", count, err)
	}
}

func TestPlainSQLiteSkipsExtensionPerConnection(t *testing.T) {
	home := t.TempDir()
	plain, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(filepath.Join(home, "plain.db")))+"?agentpack_plain=1")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if _, err := plain.Exec("CREATE TABLE local_only (value TEXT)"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := plain.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name LIKE 'crsql_%'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("plain database extension tables=%d err=%v", count, err)
	}
	if err := plain.QueryRow("SELECT length(crsql_site_id())").Scan(&count); err == nil {
		t.Fatal("plain database loaded extension")
	}
	store, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Reader.QueryRow("SELECT length(crsql_site_id())").Scan(&count); err != nil || count != 16 {
		t.Fatalf("plain connection disabled state extension: %d %v", count, err)
	}
}

func TestReopeningACurrentStoreTakesNoWriteLock(t *testing.T) {
	home := t.TempDir()
	first, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	tx, err := first.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE schema_meta SET version=version"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	second, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("reopen waited %s for another writer", elapsed)
	}
}
