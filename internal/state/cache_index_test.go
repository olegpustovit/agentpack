package state

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/olegpustovit/agentpack/internal/paths"
)

func TestOpenCacheIndexDropsLegacyAndUsesPrivatePlainSQLite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	path, err := paths.CacheDBPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if db, err := OpenCacheIndex(true); err != nil || db != nil {
		t.Fatalf("missing read-only index: %v %v", db, err)
	}
	db, err := OpenCacheIndex(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO aliases(alias,cache_key) VALUES ('a','key')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("SELECT crsql_db_version()"); err == nil {
		t.Fatal("cache index loaded cr-sqlite")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("index permissions: %v %v", info, err)
	}
	db, err = OpenCacheIndex(true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var key string
	if err := db.QueryRow("SELECT cache_key FROM aliases WHERE alias='a'").Scan(&key); err != nil || key != "key" {
		t.Fatalf("persisted alias: %q %v", key, err)
	}
	if _, err := db.Exec("DELETE FROM aliases"); err == nil {
		t.Fatal("read-only index accepted writes")
	}
}

func TestOpenCacheIndexRejectsNonregularDatabase(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	path, err := paths.CacheDBPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCacheIndex(false); err == nil {
		t.Fatal("directory accepted as index")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "index")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skip(err)
	}
	if _, err := OpenCacheIndex(false); err == nil {
		t.Fatal("symlink accepted as index")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "untouched" {
		t.Fatal("symlink target modified", err)
	}
}
