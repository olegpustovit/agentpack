package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestChangesConvergeThroughIntermediateNode(t *testing.T) {
	ctx := context.Background()
	stores := make([]*Store, 3)
	for i := range stores {
		s, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		stores[i] = s
	}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 300; i++ {
		s := stores[rng.Intn(3)]
		id := rng.Intn(30)
		var err error
		if rng.Intn(4) == 0 {
			_, err = s.DB.Exec("DELETE FROM sessions WHERE id=?", id)
		} else {
			_, err = s.DB.Exec("INSERT INTO sessions(id,name) VALUES (?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name", id, i)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	sync := func(from, to *Store) {
		t.Helper()
		site, err := from.SiteID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		since, err := to.Replicator().Cursor(ctx, site)
		if err != nil {
			t.Fatal(err)
		}
		exclude, err := to.SiteID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for {
			changes, next, err := from.Replicator().Changes(ctx, since, exclude, 4<<20)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(changes)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(body, &changes); err != nil {
				t.Fatal(err)
			}
			if _, err = to.Replicator().ApplyPage(ctx, changes, site, next); err != nil {
				t.Fatal(err)
			}
			if _, err = to.Replicator().ApplyPage(ctx, changes, site, next); err != nil {
				t.Fatal(err)
			}
			if next == since {
				break
			}
			since = next
		}
	}
	// A and C have no direct link. Repeated exchange must converge through B.
	for i := 0; i < 6; i++ {
		sync(stores[0], stores[1])
		sync(stores[1], stores[2])
		sync(stores[2], stores[1])
		sync(stores[1], stores[0])
	}
	var expected map[string]string
	for _, s := range stores {
		rows, err := s.Reader.Query("SELECT id,name FROM sessions")
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				t.Fatal(err)
			}
			got[id] = name
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if expected == nil {
			expected = got
		} else if !reflect.DeepEqual(expected, got) {
			t.Fatalf("divergent sessions: %v != %v", expected, got)
		}
	}
}

func TestPeerCursorSeesLowVersionRelaysAndFollowingLocalWrites(t *testing.T) {
	ctx := t.Context()
	stores := make([]*Store, 3)
	var receiverHome string
	for i := range stores {
		home := t.TempDir()
		if i == 1 {
			receiverHome = home
		}
		s, err := Open(home)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		stores[i] = s
	}
	a, b, c := stores[0], stores[1], stores[2]
	transfer := func(from, to *Store) {
		t.Helper()
		site, err := from.SiteID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		exclude, err := to.SiteID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		since, err := to.Replicator().Cursor(ctx, site)
		if err != nil {
			t.Fatal(err)
		}
		changes, next, err := from.Replicator().Changes(ctx, since, exclude, 4<<20)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := to.Replicator().ApplyPage(ctx, changes, site, next); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 20 {
		if _, err := b.DB.Exec("INSERT INTO sessions(id,name) VALUES ('old',?) ON CONFLICT(id) DO UPDATE SET name=excluded.name", i); err != nil {
			t.Fatal(err)
		}
	}
	worker, err := Open(receiverHome)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if _, err := worker.DB.Exec("UPDATE sessions SET name='worker warmed' WHERE id='old'"); err != nil {
		t.Fatal(err)
	}
	transfer(b, c)
	if _, err := a.DB.Exec("INSERT INTO sessions(id,name) VALUES ('relayed','from A')"); err != nil {
		t.Fatal(err)
	}
	transfer(a, b)
	transfer(b, c)
	var name string
	if err := c.Reader.QueryRow("SELECT name FROM sessions WHERE id='relayed'").Scan(&name); err != nil || name != "from A" {
		t.Fatalf("low-version relay skipped: %q %v", name, err)
	}
	// Reuse the same writer pool that applied the page; its next native write must advance the transport clock.
	if _, err := b.DB.Exec("UPDATE sessions SET name='edited on B' WHERE id='relayed'"); err != nil {
		t.Fatal(err)
	}
	transfer(b, c)
	if err := c.Reader.QueryRow("SELECT name FROM sessions WHERE id='relayed'").Scan(&name); err != nil || name != "edited on B" {
		t.Fatalf("native edit skipped: %q %v", name, err)
	}
	if _, err := worker.DB.Exec("DELETE FROM sessions WHERE id='relayed'"); err != nil {
		t.Fatal(err)
	}
	transfer(b, c)
	var count int
	if err := c.Reader.QueryRow("SELECT count(*) FROM sessions WHERE id='relayed'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("delete skipped: %d %v", count, err)
	}
}

func TestSQLiteValuesRoundTrip(t *testing.T) {
	for _, value := range []any{nil, int64(9223372036854775807), float64(1.25), "hello", []byte{}, []byte{0, 255}} {
		encoded, err := sqliteValue(value)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(encoded)
		if err != nil {
			t.Fatal(err)
		}
		var decoded SQLiteValue
		if err = json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		got, err := decoded.value()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(value, got) {
			t.Fatalf("%#v became %#v", value, got)
		}
	}
}

func TestChangesPreserveSQLiteStorageTypesAndQuarantineInvalidRows(t *testing.T) {
	ctx := t.Context()
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, s := range []*Store{a, b} {
		if _, err := s.DB.Exec("CREATE TABLE typed_values(id INTEGER NOT NULL PRIMARY KEY, value DEFAULT NULL); SELECT crsql_as_crr('typed_values')"); err != nil {
			t.Fatal(err)
		}
	}
	for i, value := range []any{nil, int64(9223372036854775807), float64(1.25), "hello", []byte{}, []byte{0, 255}} {
		if _, err := a.DB.Exec("INSERT INTO typed_values(id,value) VALUES (?,?)", i, value); err != nil {
			t.Fatal(err)
		}
	}
	changes, next, err := a.Replicator().Changes(ctx, 0, nil, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(changes)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &changes); err != nil {
		t.Fatal(err)
	}
	site, err := a.SiteID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A malformed final row must be quarantined without pinning the peer's cursor.
	bad := append(append([]Change{}, changes...), Change{Table: "invalid", DBVersion: next})
	rejected, err := b.Replicator().ApplyPage(ctx, bad, site, next)
	if err != nil || len(rejected) != 1 {
		t.Fatalf("quarantine: %v %v", rejected, err)
	}
	if cursor, err := b.Replicator().Cursor(ctx, site); err != nil || cursor != next {
		t.Fatalf("cursor %d, want %d: %v", cursor, next, err)
	}
	var count int
	if err := b.Reader.QueryRow("SELECT count(*) FROM sync_quarantine WHERE peer=? AND version=?", site, next).Scan(&count); err != nil || count != 1 {
		t.Fatalf("quarantined rows: %d %v", count, err)
	}
	for i, value := range []any{nil, int64(9223372036854775807), float64(1.25), "hello", []byte{}, []byte{0, 255}} {
		var got any
		if err := b.Reader.QueryRow("SELECT value FROM typed_values WHERE id=?", i).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(value, got) {
			t.Fatalf("value %d: %#v != %#v", i, value, got)
		}
	}
}

func TestChangesDoNotSplitTransactionAndAdvanceExcludedVersions(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := Transaction(t.Context(), s.DB, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO sessions(id,name) VALUES ('one','first'),('two','second')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Replicator().Changes(t.Context(), 0, nil, 32); !errors.Is(err, ErrChangeVersionTooLarge) {
		t.Fatalf("split transaction: %v", err)
	}
	site, err := s.SiteID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	changes, next, err := s.Replicator().Changes(t.Context(), 0, site, 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 || next == 0 {
		t.Fatalf("excluded version did not advance: %d %+v", next, changes)
	}
}

func TestPreserveArchiveEditsWithoutClocksLeavesWriterFree(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pk := []byte{1}
	if err := store.Reader.QueryRow("SELECT crsql_pack_columns('session',1)").Scan(&pk); err != nil {
		t.Fatal(err)
	}
	changes := make([]Change, 500)
	for i := range changes {
		changes[i] = Change{Table: "conversation_events", Column: "body", PK: pk}
	}
	held, err := store.DB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if err := store.preserveArchiveEdits(ctx, store.DB, changes, make([]any, len(changes))); err != nil {
		t.Fatal(err)
	}
}

func TestChangePagesEndOnWholeVersionsAndCoverEverything(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := range 40 {
		if _, err := s.DB.Exec("INSERT INTO conversation_events(session_id,seq,body) VALUES ('s',?,'x'),('s',?,'y')", 2*i, 2*i+1); err != nil {
			t.Fatal(err)
		}
	}
	all, high, err := s.Replicator().Changes(t.Context(), 0, nil, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var paged []Change
	for since, pages := int64(0), 0; since < high; pages++ {
		changes, next, err := s.Replicator().Changes(t.Context(), since, nil, 2048)
		if err != nil || next <= since || pages > len(all) {
			t.Fatalf("page after %d: next %d, %v", since, next, err)
		}
		if body, _ := json.Marshal(changes); len(body) > 2048 {
			t.Fatalf("page of %d bytes exceeds the budget", len(body))
		}
		if last := changes[len(changes)-1]; last.DBVersion != next && next != high {
			t.Fatalf("page ends inside version %d, cursor %d", last.DBVersion, next)
		}
		paged = append(paged, changes...)
		since = next
	}
	if !reflect.DeepEqual(paged, all) {
		t.Fatalf("paged %d changes, want %d", len(paged), len(all))
	}
}

func TestRestoredSourceKeepsPeerCursorsValid(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	source, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	site, _ := source.SiteID(ctx)
	pull := func() {
		t.Helper()
		since, _ := peer.Replicator().Cursor(ctx, site)
		changes, next, err := source.Replicator().Changes(ctx, since, nil, 4<<20)
		if err != nil {
			t.Fatal(err)
		}
		if err := source.Replicator().NoteServed(next); err != nil {
			t.Fatal(err)
		}
		if _, err := peer.Replicator().ApplyPage(ctx, changes, site, next); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(id string) {
		t.Helper()
		if _, err := source.DB.Exec("INSERT INTO sessions(id,name) VALUES (?,?)", id, id); err != nil {
			t.Fatal(err)
		}
	}
	insert("one")
	pull()
	snapshot := filepath.Join(t.TempDir(), "copy.db")
	if _, err := source.DB.Exec("VACUUM INTO ?", snapshot); err != nil {
		t.Fatal(err)
	}
	insert("two")
	insert("three")
	pull()
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(filepath.Join(home, "state.db"+suffix))
	}
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state.db"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if source, err = Open(home); err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	insert("after-restore")
	pull()
	var name string
	if err := peer.Reader.QueryRow("SELECT name FROM sessions WHERE id='after-restore'").Scan(&name); err != nil {
		t.Fatalf("peer missed a write made after the restore: %v", err)
	}
	// Without the watermark the cursor is ahead and the source says so instead of skipping versions.
	if _, _, err := source.Replicator().Changes(ctx, 1<<40, nil, 4<<20); !errors.Is(err, ErrCursorAhead) {
		t.Fatalf("ahead cursor: %v", err)
	}
}
