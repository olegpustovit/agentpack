package network

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
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
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

const (
	limitsCacheTTL     = 60 * time.Second
	limitsRefreshGate  = 10 * time.Second
	limitsProbeTimeout = 30 * time.Second
)

// limitsCache keeps the last collection on disk. A reader gets it at once, however old, and a stale one
// refreshes behind it; only a machine that has never collected makes the reader wait.
type limitsCache struct {
	store *state.Store
	node  string
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

func newLimitsCache(store *state.Store, node string, probe func(context.Context) []harness.LimitSnapshot) (*limitsCache, error) {
	if store == nil {
		return nil, errors.New("gateway state unavailable")
	}
	cache := &limitsCache{store: store, node: node, probe: probe}
	rows, err := store.Reader.Query("SELECT snapshot, collected_at FROM limits WHERE node_id = ? ORDER BY rowid", node)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		var collected int64
		if err := rows.Scan(&data, &collected); err != nil {
			return nil, err
		}
		var snapshot harness.LimitSnapshot
		if err := json.Unmarshal([]byte(data), &snapshot); err != nil {
			return nil, err
		}
		cache.saved.Snapshots = append(cache.saved.Snapshots, snapshot)
		at := time.UnixMilli(collected)
		if cache.saved.At.IsZero() || at.Before(cache.saved.At) {
			cache.saved.At = at
		}
	}
	return cache, rows.Err()
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
		_, event := obs.Start(ctx, "limits", "persist")
		event.Fail(err)
		event.End()
		log.Printf("limits cache is not saved: %v", err)
	}
	return saved.Snapshots
}

func (c *limitsCache) save(saved savedLimits) error {
	tx, err := c.store.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A running cache follows the offline-to-tailnet binding without a restart.
	var node string
	if err := tx.QueryRow("SELECT CASE WHEN EXISTS(SELECT 1 FROM local_node_aliases WHERE node_id=?) THEN "+state.LocalNodeSQL+" ELSE ? END", c.node, c.node).Scan(&node); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM limits WHERE node_id = ?", node); err != nil {
		return err
	}
	for _, snapshot := range saved.Snapshots {
		data, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO limits(node_id,harness,snapshot,collected_at) VALUES (?,?,?,?)", node, snapshot.Harness, string(data), saved.At.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	nodeID, cacheErr := g.auth.store.NodeID(context.Background())
	var cache *limitsCache
	if cacheErr == nil {
		cache, cacheErr = newLimitsCache(g.auth.store, nodeID, collectLimits)
	}
	if cacheErr != nil && g.store != nil {
		log.Printf("limits cache cannot be loaded: %v", cacheErr)
	}
	mux.HandleFunc("GET /api/limits", func(w http.ResponseWriter, r *http.Request) {
		if cacheErr != nil {
			http.Error(w, "limits state unavailable", http.StatusServiceUnavailable)
			return
		}
		// Reads never wait for a harness or peer; refresh the local cache beside them.
		if (r.URL.Query().Get("node") == "" || r.URL.Query().Get("node") == nodeID) && time.Since(cache.current().At) >= limitsCacheTTL && cache.refreshing.CompareAndSwap(false, true) {
			obs.Go("gateway.limits_refresh", func() {
				defer cache.refreshing.Store(false)
				cache.collect(r.Context(), limitsCacheTTL)
				// Browsers refetch on this instead of polling for the refresh to land.
				g.emitEvent(eventMessage{Topic: "limits"})
			})
		}
		limits, err := g.meshLimits(r.Context(), r.URL.Query().Get("node"))
		if err != nil {
			meshReadError(w, r, err)
			return
		}
		writeJSON(w, map[string]any{"limits": limits})
	})
	mux.HandleFunc("POST /api/limits/refresh", func(w http.ResponseWriter, r *http.Request) {
		if cacheErr != nil {
			http.Error(w, "limits state unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]any{"limits": cache.collect(r.Context(), limitsRefreshGate)})
	})
}

type meshLimit struct {
	harness.LimitSnapshot
	NodeID    string    `json:"node_id"`
	Reachable bool      `json:"reachable"`
	LastSync  time.Time `json:"last_sync,omitzero"`
}

func (g *Gateway) meshLimits(ctx context.Context, node string) ([]meshLimit, error) {
	local, err := g.auth.store.NodeID(ctx)
	if err != nil {
		return nil, err
	}
	query := "SELECT node_id,snapshot FROM limits"
	var args []any
	if node != "" {
		query += " WHERE node_id=?"
		args = append(args, node)
	}
	rows, err := g.auth.store.Reader.QueryContext(ctx, query+" ORDER BY node_id,harness", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []meshLimit{}
	for rows.Next() {
		var entry meshLimit
		var body string
		if err := rows.Scan(&entry.NodeID, &body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &entry.LimitSnapshot); err != nil {
			return nil, err
		}
		entry.Reachable, entry.LastSync = g.nodeContact(entry.NodeID, local)
		result = append(result, entry)
	}
	return result, rows.Err()
}
