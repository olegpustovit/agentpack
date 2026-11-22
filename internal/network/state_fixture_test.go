package network

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/olegpustovit/agentpack/internal/state"
)

var migratedState = sync.OnceValues(func() ([]byte, error) {
	home, err := os.MkdirTemp("", "agentpack-test-state-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	store, err := state.Open(home)
	if err != nil {
		return nil, err
	}
	if err := store.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(home, "state.db"))
})

// seedTestState skips repeating schema migrations while preserving the real SQLite
// connections and isolated replication identities. Tests of migrations open fresh databases.
func seedTestState(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, "state.db")
	if _, err := os.Lstat(path); err == nil {
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	data, err := migratedState()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	// Set the local site before opening the database with the extension, which caches it.
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`BEGIN IMMEDIATE;
		UPDATE crsql_site_id SET site_id=randomblob(16) WHERE ordinal=0;
		UPDATE crsql_db_versions SET site_id=(SELECT site_id FROM crsql_site_id WHERE ordinal=0);
		UPDATE local_node SET node_id='site:' || lower(hex((SELECT site_id FROM crsql_site_id WHERE ordinal=0))) WHERE id=1;
		COMMIT;`)
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
}
