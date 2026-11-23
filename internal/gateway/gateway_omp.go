package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness/omp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
)

// Reads that run the omp CLI are kept this long so the settings page never waits on repeated runs.
const ompReadTTL = 30 * time.Second

// ompSettings caches this machine's omp answers and the overlay last written from omp_settings.
type ompSettings struct {
	mu         sync.Mutex // held through a settings probe so concurrent readers share one run
	settingsAt time.Time
	version    string
	values     map[string]json.RawMessage
	modelsMu   sync.Mutex // the models probe is slower and must not hold up settings
	modelsAt   time.Time
	models     []json.RawMessage

	overlayMu sync.Mutex
	overlay   []byte // nil until the first write
}

func (g *Gateway) registerOMP(mux routeRegistrar) {
	mux.HandleFunc("GET /api/omp/settings", func(w http.ResponseWriter, r *http.Request) {
		shared, err := g.ompNetwork(r.Context())
		if err != nil {
			fail(w, r, err)
			return
		}
		c := &g.omp
		c.mu.Lock()
		if time.Since(c.settingsAt) >= ompReadTTL {
			version, values, err := omp.Settings(context.WithoutCancel(r.Context()))
			if err != nil {
				// The schema and network values stay useful without this machine's values.
				if event := obs.From(r.Context()); event != nil {
					event.Fail(err)
				}
				values = map[string]json.RawMessage{}
			}
			c.settingsAt, c.version, c.values = time.Now(), version, values
		}
		version, values := c.version, c.values
		c.mu.Unlock()
		network.WriteJSON(w, map[string]any{"version": version, "schema": omp.Schema(), "values": values, "network": shared})
	})
	mux.HandleFunc("GET /api/omp/models", func(w http.ResponseWriter, r *http.Request) {
		c := &g.omp
		c.modelsMu.Lock()
		defer c.modelsMu.Unlock()
		if time.Since(c.modelsAt) >= ompReadTTL {
			models, err := omp.Models(context.WithoutCancel(r.Context()))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			c.modelsAt, c.models = time.Now(), models
		}
		network.WriteJSON(w, map[string]any{"models": c.models})
	})
	mux.HandleFunc("PUT /api/omp/settings/{key}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Value json.RawMessage `json:"value"`
		}
		if !network.ReadJSON(w, r, &input) {
			return
		}
		key := r.PathValue("key")
		_, setting := omp.Lookup(key)
		if _, entry := omp.RecordEntry(key); !setting && !entry {
			http.Error(w, "unknown OMP setting", http.StatusBadRequest)
			return
		}
		if err := omp.Validate(r.Context(), key, input.Value); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var value bytes.Buffer
		if err := json.Compact(&value, input.Value); err != nil {
			http.Error(w, "invalid value", http.StatusBadRequest)
			return
		}
		_, err := g.store.DB.ExecContext(r.Context(), "INSERT INTO omp_settings(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value.String())
		g.respondOMPNetwork(w, r, err)
	})
	mux.HandleFunc("DELETE /api/omp/settings/{key}", func(w http.ResponseWriter, r *http.Request) {
		// Resetting a record resets its entries too.
		key := r.PathValue("key")
		_, err := g.store.DB.ExecContext(r.Context(), "DELETE FROM omp_settings WHERE key=? OR substr(key,1,?)=?", key, len(key)+1, key+".")
		g.respondOMPNetwork(w, r, err)
	})
}

// respondOMPNetwork applies a change to this machine's overlay at once and answers with the network rows.
func (g *Gateway) respondOMPNetwork(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		err = g.writeOMPOverlay(r.Context())
	}
	var shared map[string]json.RawMessage
	if err == nil {
		shared, err = g.ompNetwork(r.Context())
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	network.WriteJSON(w, map[string]any{"network": shared})
}

func (g *Gateway) ompNetwork(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := g.store.Reader.QueryContext(ctx, "SELECT key,value FROM omp_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	network := map[string]json.RawMessage{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if json.Valid([]byte(value)) {
			network[key] = json.RawMessage(value)
		}
	}
	return network, rows.Err()
}

// runOMPSettings keeps the overlay that every OMP launch reads in step with replicated rows.
func (g *Gateway) runOMPSettings(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := g.writeOMPOverlay(ctx); err != nil && ctx.Err() == nil {
			_, event := obs.Start(ctx, "omp.settings", "write overlay")
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

// writeOMPOverlay atomically replaces the overlay file; an empty table removes it.
func (g *Gateway) writeOMPOverlay(ctx context.Context) error {
	network, err := g.ompNetwork(ctx)
	if err != nil {
		return err
	}
	data := []byte{}
	if len(network) > 0 {
		if data, err = omp.RenderOverlay(network); err != nil {
			return err
		}
	}
	c := &g.omp
	c.overlayMu.Lock()
	defer c.overlayMu.Unlock()
	if c.overlay != nil && bytes.Equal(c.overlay, data) {
		return nil
	}
	path, err := omp.OverlayPath()
	if err != nil {
		return err
	}
	if len(data) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		temporary, err := os.CreateTemp(filepath.Dir(path), ".settings-*.yml")
		if err != nil {
			return err
		}
		defer os.Remove(temporary.Name())
		_, err = temporary.Write(data)
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(temporary.Name(), path)
		}
		if err != nil {
			return err
		}
	}
	c.overlay = data
	return nil
}
