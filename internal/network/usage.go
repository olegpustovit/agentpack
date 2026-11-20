package network

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
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
	if err = attributeWorktrees(ctx, tx, &report); err != nil {
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

// worktreeDirectory matches $AGENTPACK_HOME/worktrees/<16 hex>/<slug>, for rows from peers that
// do not record worktree_of; their definition root stands in for the main folder.
var worktreeDirectory = regexp.MustCompile(`[\\/]worktrees[\\/][0-9a-f]{16}[\\/][^\\/]+(?:[\\/]|$)`)

// attributeWorktrees counts work done in a worktree under its main folder's project. A worktree
// has its own folder identity, so its rows take the identity of the main folder's own sessions on
// that node, or one named after the main folder when it has none.
func attributeWorktrees(ctx context.Context, tx *sql.Tx, report *usageReport) error {
	type folder struct{ node, id string }
	type row struct{ node, id, directory, main, root string }
	rows, err := scanRows(ctx, tx, `SELECT node_id,folder_id,directory,worktree_of,definition_root FROM sessions
		WHERE folder_id<>'' AND (worktree_of<>'' OR (definition_root<>'' AND directory LIKE '%worktrees%'))`,
		func(rows *sql.Rows, r *row) error { return rows.Scan(&r.node, &r.id, &r.directory, &r.main, &r.root) })
	if err != nil {
		return err
	}
	projects := map[folder]string{}    // worktree folder -> main folder's identity
	mains := map[folder]string{}       // main folder's identity by node and path
	directories := map[string]string{} // project identity -> main folder
	for _, r := range rows {
		if r.main == "" && worktreeDirectory.MatchString(r.directory) {
			r.main = r.root
		}
		if r.main == "" {
			continue
		}
		key := folder{r.node, r.main}
		id, ok := mains[key]
		if !ok {
			err := tx.QueryRowContext(ctx, `SELECT folder_id FROM sessions WHERE node_id=? AND directory=? AND folder_id<>'' AND worktree_of=''
				ORDER BY last_used DESC LIMIT 1`, r.node, r.main).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				id, err = "dir:"+r.main, nil
			}
			if err != nil {
				return err
			}
			mains[key] = id
		}
		projects[folder{r.node, r.id}] = id
		directories[id] = r.main
	}
	for i, hour := range report.Hourly {
		if id, ok := projects[folder{hour.NodeID, hour.FolderID}]; ok {
			report.Hourly[i].FolderID = id
		}
	}
	// A session's directory names its project on the page, so a worktree session shows the main folder.
	for i, session := range report.Sessions {
		if id, ok := projects[folder{session.NodeID, session.FolderID}]; ok {
			report.Sessions[i].FolderID, report.Sessions[i].Directory = id, directories[id]
		}
	}
	return nil
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
