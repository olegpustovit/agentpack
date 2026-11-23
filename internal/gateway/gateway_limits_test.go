package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/state"
)

func TestLimitsSurviveRestartAndRefreshBehindTheReader(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs := make(chan string, 1)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	probe := func(context.Context) []harness.LimitSnapshot {
		started <- struct{}{}
		<-release
		return []harness.LimitSnapshot{{Harness: <-runs}}
	}

	first, err := newLimitsCache(store, "test-node", probe)
	if err != nil {
		t.Fatal(err)
	}
	runs <- "first"
	close(release)
	if got := first.collect(context.Background(), limitsCacheTTL); got[0].Harness != "first" {
		t.Fatalf("a cold cache must collect: %#v", got)
	}

	<-started
	release = make(chan struct{})
	restarted, err := newLimitsCache(store, "test-node", probe)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.current().Snapshots; len(got) != 1 || got[0].Harness != "first" {
		t.Fatalf("restart lost the cache: %#v", got)
	}
	restarted.saved.At = time.Now().Add(-2 * limitsCacheTTL)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	runs <- "second"
	close(release)
	// A cancelled viewer must not cache cancelled probes: collect outlives the request.
	if got := restarted.collect(cancelled, limitsCacheTTL); got[0].Harness != "second" {
		t.Fatalf("stale cache was not refreshed: %#v", got)
	}
	if got := restarted.current().Snapshots; got[0].Harness != "second" {
		t.Fatalf("refresh did not replace the cache: %#v", got)
	}
}

func TestLimitsPersistPerNodeAndReplaceOnlyTheirOwnRows(t *testing.T) {
	home := t.TempDir()
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	probe := func(context.Context) []harness.LimitSnapshot {
		return []harness.LimitSnapshot{{Harness: "codex"}, {Harness: "claude"}}
	}
	a, err := newLimitsCache(store, "node-a", probe)
	if err != nil {
		t.Fatal(err)
	}
	a.collect(context.Background(), limitsCacheTTL)
	b, err := newLimitsCache(store, "node-b", probe)
	if err != nil {
		t.Fatal(err)
	}
	b.collect(context.Background(), limitsCacheTTL)
	if err := a.save(savedLimits{At: time.Now(), Snapshots: []harness.LimitSnapshot{{Harness: "claude"}}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.Reader.QueryRow("SELECT count(*) FROM limits WHERE node_id='node-a'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("own rows=%d err=%v", count, err)
	}
	if err := store.Reader.QueryRow("SELECT count(*) FROM limits WHERE node_id='node-b'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("other node rows=%d err=%v", count, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restarted, err := newLimitsCache(store, "node-a", probe)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.current().Snapshots; len(got) != 1 || got[0].Harness != "claude" {
		t.Fatalf("lost persisted replacement: %#v", got)
	}
}
