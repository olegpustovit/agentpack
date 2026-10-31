package network

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/agy"
	"github.com/olegpustovit/agentpack/internal/harness/claude"
	"github.com/olegpustovit/agentpack/internal/harness/codex"
	"github.com/olegpustovit/agentpack/internal/harness/cursor"
	"github.com/olegpustovit/agentpack/internal/harness/grok"
	"github.com/olegpustovit/agentpack/internal/harness/omp"
	"github.com/olegpustovit/agentpack/internal/harness/opencode"
)

const (
	limitsCacheTTL     = 60 * time.Second
	limitsRefreshGate  = 10 * time.Second
	limitsProbeTimeout = 30 * time.Second
)

// limitsCache keeps the last collection on disk. A reader gets it at once, however old, and a stale one
// refreshes behind it; only a machine that has never collected makes the reader wait.
type limitsCache struct {
	path  string
	probe func(context.Context) []harness.LimitSnapshot

	mu    sync.Mutex // guards saved
	saved savedLimits
	// One probe run at a time: concurrent requests share the run that a previous caller started.
	collecting sync.Mutex
	refreshing atomic.Bool
}

type savedLimits struct {
	At        time.Time               `json:"at"`
	Snapshots []harness.LimitSnapshot `json:"snapshots"`
}

func newLimitsCache(path string, probe func(context.Context) []harness.LimitSnapshot) *limitsCache {
	cache := &limitsCache{path: path, probe: probe}
	// A missing or unreadable file only means the first reader waits for a collection.
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &cache.saved) != nil {
		cache.saved = savedLimits{}
	}
	return cache
}

func (c *limitsCache) current() savedLimits {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saved
}

func (c *limitsCache) get(ctx context.Context) []harness.LimitSnapshot {
	saved := c.current()
	if saved.Snapshots == nil {
		return c.collect(ctx, limitsCacheTTL)
	}
	if time.Since(saved.At) >= limitsCacheTTL && c.refreshing.CompareAndSwap(false, true) {
		go func() {
			defer c.refreshing.Store(false)
			c.collect(ctx, limitsCacheTTL)
		}()
	}
	return saved.Snapshots
}

// collect outlives the request: a viewer leaving the page must not cache cancelled probes as failures.
func (c *limitsCache) collect(ctx context.Context, maxAge time.Duration) []harness.LimitSnapshot {
	c.collecting.Lock()
	defer c.collecting.Unlock()
	if saved := c.current(); saved.Snapshots != nil && time.Since(saved.At) < maxAge {
		return saved.Snapshots
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limitsProbeTimeout)
	defer cancel()
	saved := savedLimits{At: time.Now(), Snapshots: c.probe(ctx)}
	c.mu.Lock()
	c.saved = saved
	c.mu.Unlock()
	if err := c.save(saved); err != nil {
		log.Printf("limits cache is not saved: %v", err)
	}
	return saved.Snapshots
}

func (c *limitsCache) save(saved savedLimits) error {
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if err := EnsurePrivateDirectory(filepath.Dir(c.path)); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(c.path), ".limits-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), c.path)
}

func collectLimits(ctx context.Context) []harness.LimitSnapshot {
	probes := []func(context.Context) harness.LimitSnapshot{
		func(ctx context.Context) harness.LimitSnapshot { return codex.ReadLimits(ctx, "") },
		claude.ReadLimits,
		agy.ReadLimits,
		grok.ReadLimits,
		opencode.ReadLimits,
		cursor.ReadLimits,
		omp.ReadLimits,
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
	cache := newLimitsCache(filepath.Join(g.options.Home, "network", "limits.json"), collectLimits)
	mux.HandleFunc("GET /api/limits", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"limits": cache.get(r.Context())})
	})
	mux.HandleFunc("POST /api/limits/refresh", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"limits": cache.collect(r.Context(), limitsRefreshGate)})
	})
}
