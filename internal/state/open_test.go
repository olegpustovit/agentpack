package state

import (
	"path/filepath"
	"testing"
)

func TestExtensionLoaded(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
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
