package obs

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dayReader(t *testing.T, home string, day time.Time) *sql.DB {
	t.Helper()
	path := filepath.Join(home, "logs", day.UTC().Format(time.DateOnly)+".db")
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(path))+"?agentpack_plain=1&mode=ro&_query_only=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func logEvent(at time.Time, kind string) Event {
	trace := NewTrace()
	return Event{TS: at, TraceID: trace.ID, SpanID: trace.Span, NodeID: "test-node", Proc: "gateway", Kind: kind, Name: "operation", Level: "info", Attrs: map[string]any{"version": "v0.13.9", "commit": "abc", "os": runtime.GOOS, "arch": runtime.GOARCH, "schema_version": currentSchema, "pid": os.Getpid()}}
}

func flushStore(t *testing.T, store *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDayStoreRollsAtUTCMidnightAndDeduplicatesStacks(t *testing.T) {
	home := t.TempDir()
	var clock atomic.Int64
	before := time.Date(2026, 11, 14, 23, 59, 0, 0, time.UTC)
	clock.Store(before.UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).In(time.FixedZone("local", 3*3600)) }
	store, err := Open(home, StoreOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := logEvent(now(), "worker")
	first.Level = "error"
	first.Stack = "github.com/olegpustovit/agentpack/internal/network.run\n\t/source.go:12"
	first.Attrs["message"] = "failed"
	for range 3 {
		next := first
		trace := NewTrace()
		next.TraceID, next.SpanID = trace.ID, trace.Span
		store.Write(next)
	}
	flushStore(t, store)
	clock.Store(before.Add(2 * time.Minute).UnixNano())
	next := logEvent(now(), "worker")
	store.Write(next)
	store.Write(next) // Spool replay must not duplicate a committed event.
	flushStore(t, store)
	for _, test := range []struct {
		day  time.Time
		want int
	}{{before, 3}, {before.Add(2 * time.Minute), 1}} {
		db := dayReader(t, home, test.day)
		var count int
		if err := db.QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil || count != test.want {
			t.Fatalf("day count=%d want=%d err=%v", count, test.want, err)
		}
		var kind, version string
		if err := db.QueryRow("SELECT typeof(attrs), json_extract(attrs,'$.version') FROM events LIMIT 1").Scan(&kind, &version); err != nil || kind != "blob" || version != "v0.13.9" {
			t.Fatalf("attrs %s %s %v", kind, version, err)
		}
		if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name LIKE 'crsql_%'").Scan(&count); err != nil || count != 0 {
			t.Fatalf("day file must be plain SQLite: %d %v", count, err)
		}
	}
	var stacks int
	if err := dayReader(t, home, before).QueryRow("SELECT count(*) FROM stacks").Scan(&stacks); err != nil || stacks != 1 {
		t.Fatalf("stack count=%d err=%v", stacks, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(home, "logs", before.Format(time.DateOnly)+".db"))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("day permissions: %v %v", info, err)
		}
	}
}

func TestDayStoreSweepsExpiredFilesAtStartAndHourly(t *testing.T) {
	home := t.TempDir()
	var clock atomic.Int64
	start := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	clock.Store(start.UnixNano())
	directory := filepath.Join(home, "logs")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(directory, start.AddDate(0, 0, -6).Format(time.DateOnly)+".db")
	recent := filepath.Join(directory, start.AddDate(0, 0, -4).Format(time.DateOnly)+".db")
	orphan := filepath.Join(directory, start.AddDate(0, 0, -7).Format(time.DateOnly)+".db-wal")
	for _, path := range []string{old, recent, orphan, filepath.Join(directory, "keep.txt")} {
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := Open(home, StoreOptions{Now: func() time.Time { return time.Unix(0, clock.Load()) }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, path := range []string{old, orphan} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expired file remains %s: %v", path, err)
		}
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatal("recent file removed", err)
	}
	clock.Store(start.AddDate(0, 0, 3).UnixNano())
	flushStore(t, store)
	if _, err := os.Stat(recent); !os.IsNotExist(err) {
		t.Fatalf("hourly sweep missed expired file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "keep.txt")); err != nil {
		t.Fatal("sweep removed unrelated file", err)
	}
}

func TestDayStoreWritesSeveralBatchesWithoutDroppingEvents(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC()
	store, err := Open(home, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const eventCount = 1024
	for i := range eventCount {
		event := logEvent(now, "worker")
		event.Attrs["i"] = i
		store.Write(event)
		if i%256 == 255 {
			flushStore(t, store)
		}
	}
	flushStore(t, store)
	if store.Dropped() != 0 {
		t.Fatalf("unexpected drops=%d", store.Dropped())
	}
	var count int
	if err := dayReader(t, home, now).QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil || count != eventCount {
		t.Fatalf("persisted count=%d err=%v", count, err)
	}
}

func TestDayStoreCapKeepsErrorsAndRecordsDrops(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC()
	store, err := Open(home, StoreOptions{MaxDayBytes: 1, Metadata: Metadata{NodeID: "gateway-a", Proc: "gateway", Version: "v0.13.9"}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for range 10 {
		store.Write(logEvent(now, "http"))
	}
	for range 2 {
		event := logEvent(now, "worker")
		event.Level = "error"
		event.Attrs["message"] = "failed"
		store.Write(event)
	}
	flushStore(t, store)
	db := dayReader(t, home, now)
	var info, failed, dropped int
	if err := db.QueryRow("SELECT count(*) FROM events WHERE kind='http'").Scan(&info); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM events WHERE level='error'").Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT sum(json_extract(attrs,'$.dropped')) FROM events WHERE kind='obs.drop'").Scan(&dropped); err != nil {
		t.Fatal(err)
	}
	var node, version string
	if err := db.QueryRow("SELECT node_id, json_extract(attrs,'$.version') FROM events WHERE kind='obs.drop'").Scan(&node, &version); err != nil || node != "gateway-a" || version != "v0.13.9" {
		t.Fatalf("drop metadata node=%s version=%s err=%v", node, version, err)
	}
	if info != 0 || failed != 2 || dropped != 10 || store.Dropped() != 10 {
		t.Fatalf("cap info=%d errors=%d drops=%d total=%d", info, failed, dropped, store.Dropped())
	}
}

func TestDayStoreRefusesSymlinkAndReportsWriteFailure(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC()
	store, err := Open(home, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	target := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "logs", now.Format(time.DateOnly)+".db")
	if err := os.Symlink(target, path); err != nil {
		t.Skip("symlinks unavailable", err)
	}
	store.Write(logEvent(now, "http"))
	if err := store.Flush(context.Background()); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("write failure not reported: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("symlink target changed: %q %v", data, err)
	}
	if err := store.Close(); err == nil {
		t.Fatal("Close ignored persistence failure")
	}
}
