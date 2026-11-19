package network

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
)

// maxUsageSpan bounds one read: a month and the month before it, which the page compares against.
const maxUsageSpan = 100 * 24 * time.Hour

type usageHour struct {
	Hour             int64  `json:"hour"`
	NodeID           string `json:"node_id"`
	Harness          string `json:"harness"`
	FolderID         string `json:"folder_id"`
	Model            string `json:"model"`
	Turns            int64  `json:"turns"`
	Sessions         int64  `json:"sessions"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	CostMicros       int64  `json:"cost_micros"`
	AgentMS          int64  `json:"agent_ms"`
}

type usageSession struct {
	ID               string `json:"id"`
	NodeID           string `json:"node_id"`
	Title            string `json:"title"`
	Name             string `json:"name"`
	Directory        string `json:"directory"`
	FolderID         string `json:"folder_id"`
	Harness          string `json:"harness"`
	Model            string `json:"model"`
	Turns            int64  `json:"turns"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	CostMicros       int64  `json:"cost_micros"` // -1: the agent doesn't report cost
	StartedMS        int64  `json:"started_ms"`
	LastEventMS      int64  `json:"last_event_ms"`
}

type limitSample struct {
	Hour        int64    `json:"hour"`
	NodeID      string   `json:"node_id"`
	Harness     string   `json:"harness"`
	WindowID    string   `json:"window_id"`
	UsedPercent *float64 `json:"used_percent"`
	ResetAt     int64    `json:"reset_at"`
}

type usageReport struct {
	Hourly   []usageHour    `json:"hourly"`
	Sessions []usageSession `json:"sessions"`
	Samples  []limitSample  `json:"samples"`
}

// serveUsage reads replicated rows only, so the page works offline and never asks a peer.
// from and to are Unix seconds; hours and limit samples start inside them, sessions were active in them.
func (g *Gateway) serveUsage(w http.ResponseWriter, r *http.Request) {
	from, fromErr := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, toErr := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if fromErr != nil || toErr != nil || to <= from || to-from > int64(maxUsageSpan/time.Second) {
		http.Error(w, "from and to must be Unix seconds, at most 100 days apart", http.StatusBadRequest)
		return
	}
	report, err := readUsage(r.Context(), g.auth.store.Reader, from, to)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if event := obs.From(r.Context()); event != nil {
			event.Fail(err)
		}
		http.Error(w, "usage unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, report)
}

func readUsage(ctx context.Context, db *sql.DB, from, to int64) (report usageReport, err error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	report.Hourly, err = scanRows(ctx, tx, `SELECT hour,node_id,harness,folder_id,model,turns,sessions,input_tokens,output_tokens,
		cache_read_tokens,cache_write_tokens,cost_micros,agent_ms FROM usage_hourly WHERE hour>=? AND hour<? ORDER BY hour`,
		func(row *sql.Rows, h *usageHour) error {
			return row.Scan(&h.Hour, &h.NodeID, &h.Harness, &h.FolderID, &h.Model, &h.Turns, &h.Sessions, &h.InputTokens,
				&h.OutputTokens, &h.CacheReadTokens, &h.CacheWriteTokens, &h.CostMicros, &h.AgentMS)
		}, from, to)
	if err != nil {
		return report, err
	}
	report.Sessions, err = scanRows(ctx, tx, `SELECT id,node_id,title,name,directory,folder_id,harness,model,turns,input_tokens,output_tokens,
		cache_read_tokens,cache_write_tokens,cost_micros,started_at/1000000,last_event_at/1000000 FROM sessions
		WHERE turns>0 AND started_at<? AND last_event_at>=? ORDER BY started_at DESC`,
		func(row *sql.Rows, s *usageSession) error {
			return row.Scan(&s.ID, &s.NodeID, &s.Title, &s.Name, &s.Directory, &s.FolderID, &s.Harness, &s.Model, &s.Turns,
				&s.InputTokens, &s.OutputTokens, &s.CacheReadTokens, &s.CacheWriteTokens, &s.CostMicros, &s.StartedMS, &s.LastEventMS)
		}, to*int64(time.Second), from*int64(time.Second))
	if err != nil {
		return report, err
	}
	report.Samples, err = scanRows(ctx, tx, `SELECT hour,node_id,harness,window_id,used_percent,reset_at FROM limit_samples
		WHERE hour>=? AND hour<? ORDER BY hour`,
		func(row *sql.Rows, s *limitSample) error {
			return row.Scan(&s.Hour, &s.NodeID, &s.Harness, &s.WindowID, &s.UsedPercent, &s.ResetAt)
		}, from, to)
	if err != nil {
		return report, err
	}
	return report, tx.Commit()
}

// scanRows returns an empty slice rather than nil, so JSON readers always get an array.
func scanRows[T any](ctx context.Context, tx *sql.Tx, query string, scan func(*sql.Rows, *T) error, args ...any) ([]T, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var item T
		if err := scan(rows, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}
