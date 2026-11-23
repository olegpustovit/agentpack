package obs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticReadersDoNotCreateState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	ctx := context.Background()
	issues, err := ListIssues(ctx, home, "open")
	if err != nil || len(issues) != 0 {
		t.Fatalf("issues=%v err=%v", issues, err)
	}
	events, err := Query(ctx, home, Filter{})
	if err != nil || len(events) != 0 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if _, err := GetIssue(ctx, home, strings.Repeat("a", 64)); !errors.Is(err, ErrIssueNotFound) {
		t.Fatalf("missing issue: %v", err)
	}
	for _, filter := range []Filter{{Limit: -1}, {Limit: 10001}, {Level: "warn"}, {Trace: "bad"}, {Trace: strings.Repeat("0", 32)}} {
		if _, err := Query(ctx, home, filter); err == nil {
			t.Fatalf("accepted invalid filter: %+v", filter)
		}
	}
	if _, err := ListIssues(ctx, home, "invalid"); err == nil {
		t.Fatal("accepted invalid status")
	}
	if _, err := TraceEvents(ctx, home, ""); err == nil {
		t.Fatal("accepted empty trace")
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created the home: %v", err)
	}
}

func TestDiagnosticReadersMergePublishedPendingAndSpool(t *testing.T) {
	home, _, logs, clock := issueFixture(t)
	ctx := context.Background()
	at := time.UnixMilli(clock.Load())
	first := issueEvent(at, "v1.0.0")
	first.SessionID = "session-test"
	logs.Write(first)
	flushStore(t, logs)
	for range 99 {
		event := issueEvent(at, "v1.0.0")
		event.SessionID = first.SessionID
		logs.Write(event)
	}
	parent := logEvent(at.Add(-time.Second), "http")
	parent.TraceID = first.TraceID
	parent.Name = "Needle request"
	logs.Write(parent)
	flushStore(t, logs)
	spool, err := OpenSpool(home, SpoolOptions{Proc: "worker", Now: func() time.Time { return at.Add(-3 * time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	remote := issueEvent(at.Add(time.Second), "v1.1.0")
	remote.NodeID = "other-node"
	remote.TraceID = first.TraceID
	spool.Write(remote)
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	issue, err := GetIssue(ctx, home, strings.ToUpper(first.Fingerprint))
	if err != nil || issue.Count != 101 || len(issue.Counts) != 2 || issue.Sample == nil {
		t.Fatalf("merged issue=%+v err=%v", issue, err)
	}
	trace, err := TraceEvents(ctx, home, first.TraceID)
	if err != nil || len(trace) != 3 || trace[0].Name != parent.Name || trace[2].NodeID != remote.NodeID {
		t.Fatalf("trace=%+v err=%v", trace, err)
	}
	for _, test := range []struct {
		filter Filter
		want   int
	}{{Filter{Grep: "needle"}, 1}, {Filter{Grep: "' OR 1=1 --"}, 0}, {Filter{Node: "other-node", Level: "error"}, 1}, {Filter{Session: first.SessionID, Limit: 1}, 1}, {Filter{Since: at.Add(500 * time.Millisecond)}, 1}, {Filter{Kind: "absent"}, 0}, {Filter{Trace: strings.ToUpper(first.TraceID), Limit: 2}, 2}} {
		events, err := Query(ctx, home, test.filter)
		if err != nil || len(events) != test.want {
			t.Fatalf("filter=%+v count=%d want=%d err=%v", test.filter, len(events), test.want, err)
		}
	}
	files, err := spoolFiles(filepath.Join(home, "logs", "spool"))
	if err != nil || len(files) != 1 {
		t.Fatalf("spool=%v err=%v", files, err)
	}
	path := filepath.Join(home, "logs", "spool", files[0].name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if stats, err := logs.IngestSpool(ctx); err != nil || stats.Events != 1 {
		t.Fatalf("ingest=%+v err=%v", stats, err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	issue, err = GetIssue(ctx, home, first.Fingerprint)
	if err != nil || issue.Count != 101 {
		t.Fatalf("replayed issue=%+v err=%v", issue, err)
	}
	trace, err = TraceEvents(ctx, home, first.TraceID)
	if err != nil || len(trace) != 3 {
		t.Fatalf("replayed trace count=%d err=%v", len(trace), err)
	}
	all, err := IssueEvents(ctx, home, first.Fingerprint)
	if err != nil || len(all) < len(trace) || all[0].Name != parent.Name {
		t.Fatalf("issue traces=%d err=%v", len(all), err)
	}
}

func TestDiagnosticResolveDoesNotRegressOnDelayedOldEvents(t *testing.T) {
	home, canonical, logs, clock := issueFixture(t)
	at := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	clock.Store(at.UnixMilli())
	first := issueEvent(at, "v1.2.0")
	logs.Write(first)
	flushStore(t, logs)
	logs.Write(issueEvent(at.Add(time.Second), "v1.2.0"))
	flushStore(t, logs)
	issue, err := ChangeIssue(context.Background(), canonical, home, first.Fingerprint, "resolved", "v1.2.0", "fixed", "tester")
	if err != nil || issue.Status != "resolved" || issue.Count != 2 {
		t.Fatalf("resolve=%+v err=%v", issue, err)
	}
	clock.Add(int64(time.Minute / time.Millisecond))
	flushStore(t, logs)
	var status string
	if err := canonical.Reader.QueryRow("SELECT status FROM issues WHERE fingerprint=?", first.Fingerprint).Scan(&status); err != nil || status != "resolved" {
		t.Fatalf("old unpublished event reopened issue: %s %v", status, err)
	}
	logs.Write(issueEvent(issue.ResolvedAt.Add(time.Second), "v1.2.0"))
	flushStore(t, logs)
	issue, err = GetIssue(context.Background(), home, first.Fingerprint)
	if err != nil || issue.Status != "open" || !issue.Regressed || issue.Count != 3 {
		t.Fatalf("new occurrence=%+v err=%v", issue, err)
	}
}

func TestDiagnosticReaderRejectsDatabaseSymlinks(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "elsewhere"), filepath.Join(home, "logs", "2026-11-14.db")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Query(context.Background(), home, Filter{}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink accepted: %v", err)
	}
}
