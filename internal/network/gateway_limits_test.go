package network

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness"
)

func TestLimitsSurviveRestartAndRefreshBehindTheReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network", "limits.json")
	runs := make(chan string, 1)
	release := make(chan struct{})
	probe := func(context.Context) []harness.LimitSnapshot {
		<-release
		return []harness.LimitSnapshot{{Harness: <-runs}}
	}

	first := newLimitsCache(path, probe)
	runs <- "first"
	close(release)
	if got := first.get(context.Background()); got[0].Harness != "first" {
		t.Fatalf("a cold cache must collect: %#v", got)
	}

	release = make(chan struct{})
	restarted := newLimitsCache(path, probe)
	if got := restarted.current().Snapshots; len(got) != 1 || got[0].Harness != "first" {
		t.Fatalf("restart lost the cache: %#v", got)
	}
	restarted.saved.At = time.Now().Add(-2 * limitsCacheTTL)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	// The probe is blocked on release, so this returning proves the reader did not wait for it.
	if got := restarted.get(cancelled); got[0].Harness != "first" {
		t.Fatalf("stale read: %#v", got)
	}
	runs <- "second"
	close(release)
	for restarted.refreshing.Load() {
		time.Sleep(time.Millisecond)
	}
	if got := restarted.current().Snapshots; got[0].Harness != "second" {
		t.Fatalf("refresh did not replace the cache: %#v", got)
	}
}
