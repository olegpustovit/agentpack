package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/network"
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
	if err := writeLimitSamples(context.Background(), tx, node, saved.At, saved.Snapshots); err != nil {
		return err
	}
	return tx.Commit()
}

// limitSampleRetention keeps a week of hourly readings; the Usage page draws the last day.
const limitSampleRetention = 7 * 24 * time.Hour

// writeLimitSamples keeps each window's last reading of the hour, for the burn line.
func writeLimitSamples(ctx context.Context, tx *sql.Tx, node string, at time.Time, snapshots []harness.LimitSnapshot) error {
	hour := at.Truncate(time.Hour).Unix()
	for _, snapshot := range snapshots {
		for _, window := range snapshot.Windows {
			used := window.UsedPercent
			if used == nil && window.RemainingPercent != nil {
				used = new(100 - *window.RemainingPercent)
			}
			if used == nil {
				continue
			}
			var reset int64
			if window.ResetAt != nil {
				reset = window.ResetAt.Unix()
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO limit_samples(hour,node_id,harness,window_id,used_percent,reset_at) VALUES (?,?,?,?,?,?)
				ON CONFLICT(hour,node_id,harness,window_id) DO UPDATE SET used_percent=excluded.used_percent,reset_at=excluded.reset_at`,
				hour, node, snapshot.Harness, window.ID, *used, reset); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM limit_samples WHERE node_id=? AND hour<?", node, at.Add(-limitSampleRetention).Unix())
	return err
}

func collectLimits(ctx context.Context) []harness.LimitSnapshot {
	// Names match session harnesses, so a finished turn can refresh its own.
	snapshots := make([]harness.LimitSnapshot, len(network.Agents))
	var wg sync.WaitGroup
	for index, agent := range network.Agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshots[index] = agent.Limits(ctx)
		}()
	}
	wg.Wait()
	return snapshots
}

func readLimitsOf(ctx context.Context, name string) (harness.LimitSnapshot, bool) {
	for _, agent := range network.Agents {
		if string(agent.Target) == name {
			ctx, cancel := context.WithTimeout(ctx, limitsProbeTimeout)
			defer cancel()
			return agent.Limits(ctx), true
		}
	}
	return harness.LimitSnapshot{}, false
}

// limitsTurnGate is how often finished turns may re-read one agent's limits.
const limitsTurnGate = 5 * time.Minute

// turnLimits re-reads an agent's limits once its turns on this machine move, so every peer sees fresh
// limits without anyone opening the page. Turns come from the session counters the event store keeps.
type turnLimits struct {
	store *state.Store
	read  func(ctx context.Context, harness string) (harness.LimitSnapshot, bool)
	now   func() time.Time
	turns map[string]int64 // turns at the last refresh; nil until the first check records them
}

func (t *turnLimits) check(ctx context.Context) error {
	rows, err := t.store.Reader.QueryContext(ctx, `SELECT s.harness,sum(s.turns),coalesce(max(l.collected_at),0) FROM sessions s
		LEFT JOIN limits l ON l.node_id=s.node_id AND l.harness=s.harness
		WHERE s.node_id=`+state.LocalNodeSQL+` AND s.turns>0 GROUP BY s.harness`)
	if err != nil {
		return err
	}
	defer rows.Close()
	first := t.turns == nil
	if first {
		t.turns = map[string]int64{}
	}
	var due []string
	for rows.Next() {
		var name string
		var turns, collected int64
		if err := rows.Scan(&name, &turns, &collected); err != nil {
			return err
		}
		// Turns inside the gate stay unrecorded, so the first check after it still refreshes.
		if first || turns != t.turns[name] && t.now().Sub(time.UnixMilli(collected)) >= limitsTurnGate {
			if !first {
				due = append(due, name)
			}
			t.turns[name] = turns
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, name := range due {
		if snapshot, ok := t.read(ctx, name); ok {
			if err := saveLimitSnapshot(ctx, t.store.DB, t.now(), snapshot); err != nil {
				return err
			}
		}
	}
	return nil
}

// saveLimitSnapshot replaces one agent's row of this machine, leaving the others as collected.
func saveLimitSnapshot(ctx context.Context, db *sql.DB, at time.Time, snapshot harness.LimitSnapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return state.Transaction(ctx, db, func(tx *sql.Tx) error {
		var node string
		if err := tx.QueryRowContext(ctx, "SELECT node_id FROM local_node WHERE id=1").Scan(&node); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO limits(node_id,harness,snapshot,collected_at) VALUES (?,?,?,?)
			ON CONFLICT(node_id,harness) DO UPDATE SET snapshot=excluded.snapshot,collected_at=excluded.collected_at`,
			node, snapshot.Harness, string(data), at.UnixMilli()); err != nil {
			return err
		}
		return writeLimitSamples(ctx, tx, node, at, []harness.LimitSnapshot{snapshot})
	})
}

func (g *Gateway) runLimitsAfterTurns(ctx context.Context, interval time.Duration) {
	// Usage probes can start a native agent and load its configuration and plugins.
	if runtime.GOOS == "darwin" {
		return
	}
	if g.logs != nil {
		node, _ := g.store.NodeID(ctx)
		ctx = obs.WithSink(ctx, g.logs, obs.BuildMetadata(g.options.Version, node, "gateway"))
	}
	turns := &turnLimits{store: g.store, read: readLimitsOf, now: time.Now}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := turns.check(ctx); err != nil && ctx.Err() == nil {
			_, event := obs.Start(ctx, "limits", "after_turn")
			event.Fail(err)
			event.End()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (g *Gateway) registerLimits(mux routeRegistrar) {
	nodeID, cacheErr := g.nodeID(context.Background())
	if cacheErr == nil {
		g.limits, cacheErr = newLimitsCache(g.store, nodeID, collectLimits)
	}
	cache := g.limits
	if cacheErr != nil && g.store != nil {
		log.Printf("limits cache cannot be loaded: %v", cacheErr)
	}
	mux.HandleFunc("GET /api/limits", func(w http.ResponseWriter, r *http.Request) {
		if cacheErr != nil {
			http.Error(w, "limits state unavailable", http.StatusServiceUnavailable)
			return
		}
		// Reads never wait for a harness or peer; refresh the local cache beside them.
		local, _ := g.nodeID(r.Context())
		if runtime.GOOS != "darwin" && (r.URL.Query().Get("node") == "" || r.URL.Query().Get("node") == local) && time.Since(cache.current().At) >= limitsCacheTTL && cache.refreshing.CompareAndSwap(false, true) {
			if g.beginBackground(&g.operations) {
				obs.Go("gateway.limits_refresh", func() {
					defer g.operations.Done()
					defer cache.refreshing.Store(false)
					cache.collect(r.Context(), limitsCacheTTL)
					// Browsers refetch on this instead of polling for the refresh to land.
					g.emitEvent(eventMessage{Topic: "limits"})
				})
			} else {
				cache.refreshing.Store(false)
			}
		}
		limits, err := g.meshLimits(r.Context(), r.URL.Query().Get("node"))
		if err != nil {
			meshReadError(w, r, err)
			return
		}
		network.WriteJSON(w, map[string]any{"limits": limits})
	})
	mux.HandleFunc("POST /api/limits/refresh", func(w http.ResponseWriter, r *http.Request) {
		if cacheErr != nil {
			http.Error(w, "limits state unavailable", http.StatusServiceUnavailable)
			return
		}
		if runtime.GOOS == "darwin" && r.URL.Query().Get("explicit") != "1" {
			network.WriteJSON(w, map[string]any{"limits": cache.current().Snapshots})
			return
		}
		network.WriteJSON(w, map[string]any{"limits": cache.collect(r.Context(), limitsRefreshGate)})
	})
	mux.HandleFunc("GET /api/usage", g.serveUsage)
}

func (g *Gateway) meshLimits(ctx context.Context, node string) ([]harness.NodeLimit, error) {
	local, err := g.store.NodeID(ctx)
	if err != nil {
		return nil, err
	}
	query := "SELECT node_id,snapshot FROM limits"
	var args []any
	if node != "" {
		query += " WHERE node_id=?"
		args = append(args, node)
	}
	rows, err := g.store.Reader.QueryContext(ctx, query+" ORDER BY node_id,harness", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []harness.NodeLimit{}
	for rows.Next() {
		var entry harness.NodeLimit
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
