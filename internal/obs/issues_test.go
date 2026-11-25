package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/olegpustovit/agentpack/internal/state"
)

func issueFixture(t *testing.T) (string, *state.Store, *Store, *atomic.Int64) {
	t.Helper()
	home := t.TempDir()
	canonical, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := canonical.Close(); err != nil {
			t.Error(err)
		}
	})
	clock := &atomic.Int64{}
	clock.Store(time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC).UnixMilli())
	logs, err := Open(home, StoreOptions{State: canonical, SweepInterval: 10 * time.Second, Now: func() time.Time { return time.UnixMilli(clock.Load()) }, Metadata: Metadata{NodeID: "test-node", Proc: "gateway"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := logs.Close(); err != nil {
			t.Error(err)
		}
	})
	return home, canonical, logs, clock
}

func issueEvent(at time.Time, version string) Event {
	event := logEvent(at, "worker")
	event.Level = "error"
	event.Attrs["version"] = version
	event.Attrs["message"] = "operation failed"
	event.Stack = "github.com/olegpustovit/agentpack/internal/network.runWorker\n\t/source.go:12"
	return event.Sanitized()
}

func TestIssueIdentityBindingKeepsRecoveredCountsIdempotent(t *testing.T) {
	home := t.TempDir()
	canonical, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.Close()
	options := StoreOptions{State: canonical, Metadata: Metadata{NodeID: "local"}}
	logs, err := Open(home, options)
	if err != nil {
		t.Fatal(err)
	}
	write := func() {
		event := issueEvent(time.Now(), "v1.0.0")
		event.NodeID = "local"
		logs.Write(event)
		flushStore(t, logs)
	}
	write()
	if err := canonical.BindNode(t.Context(), "stable-node"); err != nil {
		t.Fatal(err)
	}
	write()
	if err := logs.Close(); err != nil {
		t.Fatal(err)
	}
	logs, err = Open(home, options)
	if err != nil {
		t.Fatal(err)
	}
	write()
	if err := logs.Close(); err != nil {
		t.Fatal(err)
	}
	var count, rows int
	if err := canonical.Reader.QueryRow("SELECT coalesce(sum(count),0),count(*) FROM issue_counts WHERE node_id='stable-node'").Scan(&count, &rows); err != nil || count != 3 || rows != 1 {
		t.Fatalf("binding/recovery changed counts: count=%d rows=%d err=%v", count, rows, err)
	}
	if err := canonical.Reader.QueryRow("SELECT count(*) FROM issue_counts WHERE node_id<>'stable-node'").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("counts retained old machine identity: %d %v", rows, err)
	}
}

func TestPendingIssueCountsPublishWithoutNewEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		home := t.TempDir()
		canonical, err := state.Open(home)
		if err != nil {
			t.Fatal(err)
		}
		defer canonical.Close()
		logs, err := Open(home, StoreOptions{State: canonical, Metadata: Metadata{NodeID: "test-node"}})
		if err != nil {
			t.Fatal(err)
		}
		defer logs.Close()
		for range 2 {
			logs.Write(issueEvent(time.Now(), "v1.0.0"))
			flushStore(t, logs)
		}
		checkCount := func(want int) {
			t.Helper()
			var count int
			if err := canonical.Reader.QueryRow("SELECT sum(count) FROM issue_counts").Scan(&count); err != nil || count != want {
				t.Fatalf("issue count %d, want %d: %v", count, want, err)
			}
		}
		checkCount(1)
		time.Sleep(issueFlushInterval + logs.options.FlushInterval)
		synctest.Wait()
		checkCount(2)
	})
}

func TestIssuesCountEveryOccurrenceButSampleRepeatedErrors(t *testing.T) {
	home, canonical, logs, clock := issueFixture(t)
	at := time.UnixMilli(clock.Load())
	sample := issueEvent(at, "v1.0.0")
	for range 1000 {
		logs.Write(issueEvent(at, "v1.0.0"))
	}
	flushStore(t, logs)
	var before int
	if err := canonical.Reader.QueryRow("SELECT count FROM issue_counts WHERE fingerprint=?", sample.Fingerprint).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before <= 0 || before >= 1000 {
		t.Fatalf("counts were not coalesced: %d", before)
	}
	clock.Add(int64(31 * time.Second / time.Millisecond))
	flushStore(t, logs)
	var issues, count int
	if err := canonical.Reader.QueryRow("SELECT count(*) FROM issues").Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if err := canonical.Reader.QueryRow("SELECT count FROM issue_counts WHERE fingerprint=?", sample.Fingerprint).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if issues != 1 || count != 1000 {
		t.Fatalf("issues=%d count=%d", issues, count)
	}
	var stored int
	if err := dayReader(t, home, at).QueryRow("SELECT count(*) FROM events WHERE level='error'").Scan(&stored); err != nil || stored != 39 {
		t.Fatalf("sampled rows=%d err=%v", stored, err)
	}
	var encoded string
	if err := canonical.Reader.QueryRow("SELECT sample FROM issues").Scan(&encoded); err != nil || len(encoded) > 16<<10 || !json.Valid([]byte(encoded)) {
		t.Fatalf("invalid issue sample (%d): %v", len(encoded), err)
	}
	clock.Add(int64(time.Hour / time.Millisecond))
	logs.Write(issueEvent(time.UnixMilli(clock.Load()), "v1.0.0"))
	flushStore(t, logs)
	if err := dayReader(t, home, at).QueryRow("SELECT count(*) FROM events WHERE level='error'").Scan(&stored); err != nil || stored != 40 {
		t.Fatalf("new-hour first error was not kept: %d %v", stored, err)
	}
	late := at.Add(-time.Second)
	logs.Write(issueEvent(late, "v1.0.0"))
	clock.Add(int64(31 * time.Second / time.Millisecond))
	flushStore(t, logs)
	var firstSeen int64
	if err := canonical.Reader.QueryRow("SELECT first_seen FROM issues WHERE fingerprint=?", sample.Fingerprint).Scan(&firstSeen); err != nil || firstSeen != late.UnixMilli() {
		t.Fatalf("late first occurrence=%d err=%v", firstSeen, err)
	}
}

func TestIssueRegressionDependsOnIncomingBuildVersion(t *testing.T) {
	_, canonical, logs, clock := issueFixture(t)
	initial := issueEvent(time.UnixMilli(clock.Load()), "v1.0.0")
	logs.Write(initial)
	flushStore(t, logs)
	for _, test := range []struct {
		version   string
		regressed bool
	}{{"v1.1.9", false}, {"v1.2.0", true}, {"v1.3.0", true}, {"development", true}} {
		if _, err := canonical.DB.Exec("UPDATE issues SET status='resolved',resolved_version='v1.2.0',regressed=0 WHERE fingerprint=?", initial.Fingerprint); err != nil {
			t.Fatal(err)
		}
		clock.Add(int64(31 * time.Second / time.Millisecond))
		logs.Write(issueEvent(time.UnixMilli(clock.Load()), test.version))
		flushStore(t, logs)
		var status string
		var regressed int
		if err := canonical.Reader.QueryRow("SELECT status,regressed FROM issues WHERE fingerprint=?", initial.Fingerprint).Scan(&status, &regressed); err != nil {
			t.Fatal(err)
		}
		want := "resolved"
		if test.regressed {
			want = "open"
		}
		if status != want || (regressed == 1) != test.regressed {
			t.Fatalf("version %s: status=%s regressed=%d", test.version, status, regressed)
		}
	}
	if _, err := canonical.DB.Exec("UPDATE issues SET status='ignored',regressed=0 WHERE fingerprint=?", initial.Fingerprint); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(31 * time.Second / time.Millisecond))
	logs.Write(issueEvent(time.UnixMilli(clock.Load()), "v2.0.0"))
	flushStore(t, logs)
	var status string
	if err := canonical.Reader.QueryRow("SELECT status FROM issues WHERE fingerprint=?", initial.Fingerprint).Scan(&status); err != nil || status != "ignored" {
		t.Fatalf("ignored issue changed: %s %v", status, err)
	}
}

func TestIssueSpoolReplayDoesNotInflateSampledCounts(t *testing.T) {
	home, canonical, logs, clock := issueFixture(t)
	at := time.UnixMilli(clock.Load())
	directory := filepath.Join(home, "logs", "spool")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	var contents strings.Builder
	for range 1000 {
		data, err := json.Marshal(issueEvent(at, "v1.0.0"))
		if err != nil {
			t.Fatal(err)
		}
		contents.Write(data)
		contents.WriteByte('\n')
	}
	path := filepath.Join(directory, fmt.Sprintf("worker-1-%d.ndjson", at.Unix()/60-2))
	if err := os.WriteFile(path, []byte(contents.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := logs.IngestSpool(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(31 * time.Second / time.Millisecond))
	flushStore(t, logs)
	if err := os.WriteFile(path, []byte(contents.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := logs.IngestSpool(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(31 * time.Second / time.Millisecond))
	flushStore(t, logs)
	var count int
	if err := canonical.Reader.QueryRow("SELECT sum(count) FROM issue_counts").Scan(&count); err != nil || count != 1000 {
		t.Fatalf("replay count=%d err=%v", count, err)
	}
}

func TestIssueCountersRecoverAfterFailedPublication(t *testing.T) {
	home := t.TempDir()
	canonical, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.Close()
	if _, err := canonical.DB.Exec("CREATE TRIGGER reject_counts BEFORE INSERT ON issue_counts BEGIN SELECT RAISE(ABORT,'offline'); END"); err != nil {
		t.Fatal(err)
	}
	options := StoreOptions{State: canonical, Metadata: Metadata{NodeID: "test-node"}}
	logs, err := Open(home, options)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for range 1000 {
		logs.Write(issueEvent(at, "v1.0.0"))
	}
	if err := logs.Flush(context.Background()); err == nil {
		t.Fatal("ignored failed issue publication")
	}
	if err := logs.Close(); err == nil {
		t.Fatal("Close ignored failed publication")
	}
	if _, err := canonical.DB.Exec("DROP TRIGGER reject_counts"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reopened, err := Open(home, options)
		if err != nil {
			t.Fatal(err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := canonical.Reader.QueryRow("SELECT sum(count) FROM issue_counts").Scan(&count); err != nil || count != 1000 {
			t.Fatalf("recovered count=%d err=%v", count, err)
		}
	}
}

func TestOversizedEventsAndSamplesKeepRequiredMetadata(t *testing.T) {
	event := issueEvent(time.Now(), "v1.2.3")
	for i := range 15 {
		event.Attrs[fmt.Sprint(i)] = strings.Repeat("\x00", 8000)
	}
	for _, limit := range []int{MaxEventBytes, 16 << 10} {
		capped := event.limited(limit)
		data, err := json.Marshal(capped)
		if err != nil || len(data) > limit {
			t.Fatalf("capped event=%d limit=%d err=%v", len(data), limit, err)
		}
		for _, key := range metadataKeys {
			if _, ok := capped.Attrs[key]; !ok {
				t.Fatalf("truncation lost %s", key)
			}
		}
		if capped.Attrs["version"] != "v1.2.3" {
			t.Fatal("truncation lost build version")
		}
	}
}

func TestIssueCoalescingDefersRetentionUntilCountsArePublished(t *testing.T) {
	home, canonical, logs, clock := issueFixture(t)
	start := time.Date(2026, 11, 14, 23, 59, 40, 0, time.UTC)
	clock.Store(start.UnixMilli())
	old := start.AddDate(0, 0, -5)
	logs.Write(issueEvent(old, "v1.0.0"))
	flushStore(t, logs)
	clock.Add(10000)
	logs.Write(issueEvent(old.Add(time.Second), "v1.0.0"))
	flushStore(t, logs)
	clock.Add(10000)
	flushStore(t, logs)
	path := filepath.Join(home, "logs", old.Format(time.DateOnly)+".db")
	if _, err := os.Stat(path); err != nil {
		t.Fatal("retention removed unpublished counts", err)
	}
	var count int
	if err := canonical.Reader.QueryRow("SELECT sum(count) FROM issue_counts").Scan(&count); err != nil || count != 1 {
		t.Fatalf("sweep bypassed the coalescing interval: %d %v", count, err)
	}
	clock.Add(11000)
	flushStore(t, logs)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("published expired day remains: %v", err)
	}
	if err := canonical.Reader.QueryRow("SELECT sum(count) FROM issue_counts").Scan(&count); err != nil || count != 2 {
		t.Fatalf("retention lost counts: %d %v", count, err)
	}
}

func TestReplicatedIssuesCombineMachineCountsAndResolution(t *testing.T) {
	ctx := t.Context()
	homes := []string{t.TempDir(), t.TempDir()}
	stores := make([]*state.Store, 2)
	sample := issueEvent(time.Now(), "v1.0.0")
	for i, home := range homes {
		canonical, err := state.Open(home)
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = canonical
		t.Cleanup(func() { canonical.Close() })
		node := fmt.Sprintf("machine-%d", i)
		if err := canonical.BindNode(ctx, node); err != nil {
			t.Fatal(err)
		}
		logs, err := Open(home, StoreOptions{State: canonical, Metadata: Metadata{NodeID: node}})
		if err != nil {
			t.Fatal(err)
		}
		event := sample
		event.NodeID = node
		logs.Write(event)
		if err := logs.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sync := func(from, to *state.Store) {
		t.Helper()
		site, err := from.SiteID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cursor, err := to.Replicator().Cursor(ctx, site)
		if err != nil {
			t.Fatal(err)
		}
		rows, next, err := from.Replicator().Changes(ctx, cursor, nil, 4<<20)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := to.Replicator().ApplyPage(ctx, rows, site, next); err != nil {
			t.Fatal(err)
		}
	}
	sync(stores[0], stores[1])
	var sampleJSON string
	if err := stores[1].Reader.QueryRow("SELECT sample FROM issues WHERE fingerprint=?", sample.Fingerprint).Scan(&sampleJSON); err != nil || !json.Valid([]byte(sampleJSON)) {
		t.Fatalf("replicated sample: %q %v", sampleJSON, err)
	}
	if _, err := ChangeIssue(ctx, stores[1], homes[1], sample.Fingerprint, "resolved", "v1.1.0", "fixed", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	sync(stores[1], stores[0])
	for _, canonical := range stores {
		var count, nodes int
		if err := canonical.Reader.QueryRow("SELECT sum(count),count(*) FROM issue_counts WHERE fingerprint=?", sample.Fingerprint).Scan(&count, &nodes); err != nil || count != 2 || nodes != 2 {
			t.Fatalf("replicated counts: %d across %d nodes: %v", count, nodes, err)
		}
		var status string
		if err := canonical.Reader.QueryRow("SELECT status FROM issues WHERE fingerprint=?", sample.Fingerprint).Scan(&status); err != nil || status != "resolved" {
			t.Fatalf("replicated resolution: %q %v", status, err)
		}
	}
}

// A crash can lose unsynced day-counter writes after their publication was recorded.
// Reopening must not fail, and the already-published counts must not be double counted.
func TestIssueCountersSurviveCrashLostDayCounterWrites(t *testing.T) {
	home := t.TempDir()
	canonical, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.Close()
	options := StoreOptions{State: canonical, Metadata: Metadata{NodeID: "test-node"}}
	logs, err := Open(home, options)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for range 10 {
		logs.Write(issueEvent(at, "v1.0.0"))
	}
	if err := logs.Close(); err != nil {
		t.Fatal(err)
	}
	// The published cursor is now ahead of what the day counter still holds.
	if _, err := canonical.DB.Exec("UPDATE issue_log_cursors SET count=count+5"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reopened, err := Open(home, options)
		if err != nil {
			t.Fatalf("reopen after lost counter writes: %v", err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatalf("close after lost counter writes: %v", err)
		}
	}
	var count int
	if err := canonical.Reader.QueryRow("SELECT sum(count) FROM issue_counts").Scan(&count); err != nil || count != 10 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestIssueCursorPruneRetriesAfterFailure(t *testing.T) {
	home := t.TempDir()
	canonical, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.Close()
	now := time.Now()
	var clock atomic.Int64
	clock.Store(now.UnixMilli())
	logs, err := Open(home, StoreOptions{State: canonical, SweepInterval: 10 * time.Second, Now: func() time.Time {
		return time.UnixMilli(clock.Load())
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	if _, err := canonical.DB.Exec(`
INSERT INTO issue_log_cursors(day,fingerprint,node_id,version,count) VALUES ('2000-01-01','test','test-node','v1',1);
CREATE TRIGGER reject_cursor_prune BEFORE DELETE ON issue_log_cursors BEGIN SELECT RAISE(ABORT,'busy'); END;`); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(time.Minute / time.Millisecond))
	if err := logs.Flush(t.Context()); err == nil {
		t.Fatal("expected failed cursor pruning")
	}
	if _, err := canonical.DB.Exec("DROP TRIGGER reject_cursor_prune"); err != nil {
		t.Fatal(err)
	}
	// Retry without advancing the clock to another sweep interval.
	flushStore(t, logs)
	var count int
	if err := canonical.Reader.QueryRow("SELECT count(*) FROM issue_log_cursors").Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired cursors after retry = %d, %v", count, err)
	}
}

func TestShutdownLeavesUnpublishedIssuesForRecovery(t *testing.T) {
	home := t.TempDir()
	canonical, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.Close()

	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	options := StoreOptions{State: canonical, StateContext: ctx, Metadata: Metadata{NodeID: "test-node"}}
	logs, err := Open(home, options)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	logs.Write(issueEvent(time.Now(), "v1.0.0"))
	if err := logs.Close(); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		var n int
		if err := canonical.Reader.QueryRow("SELECT coalesce(sum(count),0) FROM issue_counts").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(); got != 0 {
		t.Fatalf("shutdown published %d issues to state.db", got)
	}
	options.StateContext = t.Context()
	logs, err = Open(home, options)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	flushStore(t, logs)
	if got := count(); got != 1 {
		t.Fatalf("recovered %d issues, want one", got)
	}
}
