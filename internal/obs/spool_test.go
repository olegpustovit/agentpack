package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpoolsIngestOnceAfterTwoMinutes(t *testing.T) {
	home := t.TempDir()
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(at.Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	for _, proc := range []string{"worker-a", "worker-b"} {
		spool, err := OpenSpool(home, SpoolOptions{Proc: proc, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		for range 150 {
			spool.Write(logEvent(at, "worker"))
		}
		if err := spool.Close(); err != nil {
			t.Fatal(err)
		}
		if spool.Dropped() != 0 {
			t.Fatal("unexpected spool drops")
		}
	}
	directory := filepath.Join(home, "logs", "spool")
	files, err := spoolFiles(directory)
	if err != nil || len(files) != 2 {
		t.Fatalf("spool files=%v err=%v", files, err)
	}
	if !strings.Contains(files[0].name, "-"+strconv.Itoa(os.Getpid())+"-") || !strings.HasSuffix(files[0].name, fmt.Sprintf("-%d.ndjson", at.Unix()/60)) {
		t.Fatal("spool filename omits process/minute", files[0].name)
	}
	replay, err := os.ReadFile(filepath.Join(directory, files[0].name))
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(home, StoreOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stats, err := store.IngestSpool(context.Background())
	if err != nil || stats.Files != 0 {
		t.Fatalf("ingested active minute: %+v %v", stats, err)
	}
	clock.Store(at.Add(2 * time.Minute).Unix())
	stats, err = store.IngestSpool(context.Background())
	if err != nil || stats.Files != 2 || stats.Events != 300 || stats.Malformed != 0 {
		t.Fatalf("ingestion %+v: %v", stats, err)
	}
	remaining, err := spoolFiles(directory)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("committed files remain: %+v %v", remaining, err)
	}
	// Simulate a crash after commit but before unlinking the first spool.
	if err := os.WriteFile(filepath.Join(directory, files[0].name), replay, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestSpool(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := dayReader(t, home, at).QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil || count != 300 {
		t.Fatalf("replay changed event count=%d err=%v", count, err)
	}
}

func TestSpoolSkipsMalformedOversizedAndPartialLines(t *testing.T) {
	home := t.TempDir()
	at := time.Now().UTC()
	store, err := Open(home, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	directory := filepath.Join(home, "logs", "spool")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	encode := func(event Event) string {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	invalid := logEvent(at, "worker")
	invalid.TraceID = "malformed"
	lines := encode(logEvent(at, "worker")) + "\n{broken}\n" + strings.Repeat("x", MaxEventBytes+10) + "\n" + encode(invalid) + "\n" + encode(logEvent(at, "worker")) + "\n" + `{"ts":"incomplete`
	path := filepath.Join(directory, fmt.Sprintf("worker-1-%d.ndjson", at.Unix()/60-2))
	if err := os.WriteFile(path, []byte(lines), 0600); err != nil {
		t.Fatal(err)
	}
	stats, err := store.IngestSpool(context.Background())
	if err != nil || stats.Events != 2 || stats.Malformed != 4 || stats.Files != 1 {
		t.Fatalf("malformed ingestion %+v err=%v", stats, err)
	}
	var count int
	if err := dayReader(t, home, at).QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil || count != 2 {
		t.Fatalf("good lines lost: %d %v", count, err)
	}
}

func TestSpoolDirectoryCapDeletesOldestFirst(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC()
	store, err := Open(home, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	directory := filepath.Join(home, "logs", "spool")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for i := range 3 {
		name := fmt.Sprintf("worker-%d-%d.ndjson", i+1, now.Unix()/60)
		names = append(names, name)
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, make([]byte, (8<<20)+1), 0600); err != nil {
			t.Fatal(err)
		}
		modified := now.Add(time.Duration(i-3) * time.Hour)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := store.IngestSpool(context.Background())
	if err != nil || stats.DroppedFiles != 2 || stats.DroppedBytes != 2*((8<<20)+1) {
		t.Fatalf("cap stats %+v err=%v", stats, err)
	}
	remaining, err := spoolFiles(directory)
	if err != nil || len(remaining) != 1 || remaining[0].name != names[2] || remaining[0].size > MaxSpoolBytes {
		t.Fatalf("cap retained wrong files: %+v %v", remaining, err)
	}
}

func TestSpoolImportFailurePreservesFileAndCanRetry(t *testing.T) {
	home := t.TempDir()
	at := time.Now().UTC()
	store, err := Open(home, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	directory := filepath.Join(home, "logs", "spool")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(logEvent(at, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, fmt.Sprintf("worker-1-%d.ndjson", at.Unix()/60-2))
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(home, "logs", at.Format(time.DateOnly)+".db")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestSpool(context.Background()); err == nil {
		t.Fatal("ignored write failure")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("failed import removed source", err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestSpool(context.Background()); err != nil {
		t.Fatal("retry failed", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("successful retry retained source: %v", err)
	}
	var count int
	if err := dayReader(t, home, at).QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry count=%d err=%v", count, err)
	}
}

func TestEmptySpoolWritesNothingAndRejectsInvalidProcess(t *testing.T) {
	home := t.TempDir()
	if _, err := OpenSpool(home, SpoolOptions{Proc: "../escape"}); err == nil {
		t.Fatal("accepted unsafe process name")
	}
	spool, err := OpenSpool(home, SpoolOptions{Proc: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "logs")); !os.IsNotExist(err) {
		t.Fatalf("empty spool touched logs: %v", err)
	}
}
