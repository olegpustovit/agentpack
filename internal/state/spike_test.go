package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const spikeRows = 10_000

func spikeStore(tb testing.TB) (*Store, string) {
	tb.Helper()
	home := tb.TempDir()
	path := filepath.Join(home, "state.db")
	s, err := Open(home)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := s.Close(); err != nil {
			tb.Error(err)
		}
	})
	if _, err := s.DB.Exec("CREATE TABLE events (id INTEGER NOT NULL PRIMARY KEY, body TEXT NOT NULL DEFAULT ''); SELECT crsql_as_crr('events')"); err != nil {
		tb.Fatal(err)
	}
	return s, path
}

func spikeBody() string { return `{"text":"` + strings.Repeat("x", 489) + `"}` }

func insertSpike(tb testing.TB, s *Store) {
	tb.Helper()
	tx, err := s.DB.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare("INSERT INTO events (id, body) VALUES (?, ?)")
	if err != nil {
		tb.Fatal(err)
	}
	defer stmt.Close()
	body := spikeBody()
	for i := range spikeRows {
		if _, err := stmt.Exec(i, body); err != nil {
			tb.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
}

func syncSpike(tb testing.TB, from, to *Store) {
	tb.Helper()
	rows, err := from.DB.Query(`SELECT "table",pk,cid,val,col_version,db_version,site_id,cl,seq,ts FROM crsql_changes`)
	if err != nil {
		tb.Fatal(err)
	}
	defer rows.Close()
	tx, err := to.DB.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO crsql_changes ("table",pk,cid,val,col_version,db_version,site_id,cl,seq,ts) VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer stmt.Close()
	for rows.Next() {
		var values [10]any
		var targets [10]any
		for i := range targets {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets[:]...); err != nil {
			tb.Fatal(err)
		}
		if _, err := stmt.Exec(values[:]...); err != nil {
			tb.Fatal(err)
		}
	}
	if err := rows.Err(); err != nil {
		tb.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
}

func spikeSize(tb testing.TB, store *Store, path string) int64 {
	tb.Helper()
	// Measure the checkpointed database rather than leaving its pages in the WAL.
	if _, err := store.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		tb.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		tb.Fatal(err)
	}
	return info.Size()
}

func TestSyncTenThousand(t *testing.T) {
	a, pathA := spikeStore(t)
	b, pathB := spikeStore(t)
	t.Run("insert-and-replicate", func(t *testing.T) {
		started := time.Now()
		insertSpike(t, a)
		inserted := time.Since(started)
		started = time.Now()
		syncSpike(t, a, b)
		synced := time.Since(started)
		var count int
		var bytesTotal int
		if err := b.DB.QueryRow("SELECT count(*), sum(length(body)) FROM events").Scan(&count, &bytesTotal); err != nil {
			t.Fatal(err)
		}
		if count != spikeRows || bytesTotal != spikeRows*len(spikeBody()) {
			t.Fatalf("incomplete replica: %d rows, %d bytes", count, bytesTotal)
		}
		var body string
		if err := b.DB.QueryRow("SELECT body FROM events WHERE id = 123").Scan(&body); err != nil {
			t.Fatal(err)
		}
		if body != spikeBody() {
			t.Fatal("replica body differs")
		}
		t.Logf("insert=%s sync=%s raw=%d source=%d replica=%d", inserted, synced, bytesTotal, spikeSize(t, a, pathA), spikeSize(t, b, pathB))
	})
	t.Run("delete-and-replicate", func(t *testing.T) {
		if _, err := a.DB.Exec("DELETE FROM events"); err != nil {
			t.Fatal(err)
		}
		syncSpike(t, a, b)
		var count int
		if err := b.DB.QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("replica retained %d deleted events", count)
		}
		t.Logf("after deletion source=%d replica=%d", spikeSize(t, a, pathA), spikeSize(t, b, pathB))
	})
}

func BenchmarkOpen(b *testing.B) {
	home := b.TempDir()
	for b.Loop() {
		s, err := Open(home)
		if err != nil {
			b.Fatal(err)
		}
		if err := s.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInsertEvents(b *testing.B) {
	for range b.N {
		b.StopTimer()
		s, _ := spikeStore(b)
		b.StartTimer()
		insertSpike(b, s)
		b.StopTimer()
		b.ReportMetric(float64(spikeRows), "events/op")
	}
}
