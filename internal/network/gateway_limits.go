package network

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/agy"
	"github.com/olegpustovit/agentpack/internal/harness/claude"
	"github.com/olegpustovit/agentpack/internal/harness/codex"
	"github.com/olegpustovit/agentpack/internal/harness/cursor"
	"github.com/olegpustovit/agentpack/internal/harness/grok"
	"github.com/olegpustovit/agentpack/internal/harness/opencode"
)

const (
	limitsCacheTTL    = 60 * time.Second
	limitsRefreshGate = 10 * time.Second
)

// limitsCache holds one collection. The mutex serializes collection, so concurrent requests
// share the probe run that a previous caller already started.
type limitsCache struct {
	mu        sync.Mutex
	at        time.Time
	snapshots []harness.LimitSnapshot
}

func (c *limitsCache) get(ctx context.Context, maxAge time.Duration) []harness.LimitSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snapshots != nil && time.Since(c.at) < maxAge {
		return c.snapshots
	}
	c.snapshots = collectLimits(ctx)
	c.at = time.Now()
	return c.snapshots
}

func collectLimits(ctx context.Context) []harness.LimitSnapshot {
	probes := []func(context.Context) harness.LimitSnapshot{
		func(ctx context.Context) harness.LimitSnapshot { return codex.ReadLimits(ctx, "") },
		claude.ReadLimits,
		agy.ReadLimits,
		grok.ReadLimits,
		opencode.ReadLimits,
		cursor.ReadLimits,
	}
	snapshots := make([]harness.LimitSnapshot, len(probes))
	var wg sync.WaitGroup
	for index, probe := range probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshots[index] = probe(ctx)
		}()
	}
	wg.Wait()
	return snapshots
}

func (g *Gateway) registerLimits(mux *http.ServeMux) {
	cache := &limitsCache{}
	mux.HandleFunc("GET /api/limits", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"limits": cache.get(r.Context(), limitsCacheTTL)})
	})
	mux.HandleFunc("POST /api/limits/refresh", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"limits": cache.get(r.Context(), limitsRefreshGate)})
	})
}
