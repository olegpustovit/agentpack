package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

func (g *Gateway) ServePrompt(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	suffix := "/prompt"
	if strings.HasSuffix(r.URL.Path, "/images") {
		suffix = "/prompt/images"
	}
	if record, err := g.workerRecord(ctx, id); err == nil {
		if session.AuthorizeWrite(w, r, record.Session) {
			network.ProxyWorkerConversation(w, r, record, suffix)
		}
		return
	}
	info, err := session.StoredSession(ctx, g.store.Reader, id)
	if err != nil || info.Transport != "acp" || !network.ValidSessionID(id) {
		http.Error(w, "session unavailable", http.StatusNotFound)
		return
	}
	if !session.AuthorizeWrite(w, r, info) {
		return
	}
	if info.ConversationState != "starting" || !info.EndedAt.IsZero() {
		http.Error(w, "session unavailable", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	prompt, steer, ok := network.ReadPromptRequest(w, r)
	if !ok {
		return
	}
	prompt.From = network.PromptFromContext(ctx)
	directory := filepath.Join(g.options.Home, "network", "sessions", id)
	if err := paths.EnsurePrivateDirectory(directory); err != nil {
		promptQueueError(w, r, err)
		return
	}
	lock, err := network.LockPromptHandoff(ctx, directory)
	if err != nil {
		promptQueueError(w, r, err)
		return
	}
	if record, err := g.workerRecord(ctx, id); err == nil {
		_ = lock.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		if session.AuthorizeWrite(w, r, record.Session) {
			network.ProxyWorkerConversation(w, r, record, suffix)
		}
		return
	}
	defer lock.Close()
	if prompt.QueueID == "" {
		prompt.QueueID = rand.Text()
	}
	store := g.store
	seen, err := network.StoredPromptID(ctx, store, id, prompt.QueueID)
	if err != nil {
		promptQueueError(w, r, err)
		return
	}
	if !seen {
		item := network.QueuedPrompt{ID: prompt.QueueID, Input: prompt, Steer: steer}
		err := state.Transaction(ctx, store.DB, func(tx *sql.Tx) error {
			return appendStartingPrompt(ctx, tx, id, item)
		})
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "session unavailable", http.StatusConflict)
			return
		}
		if err != nil {
			promptQueueError(w, r, err)
			return
		}
	}

	network.WritePromptAccepted(w, prompt.QueueID)
}

func appendStartingPrompt(ctx context.Context, tx *sql.Tx, id string, item network.QueuedPrompt) error {
	now := time.Now()
	entry := acp.LogEntry{TS: now.UnixMilli(), Kind: "queued_prompt", Payload: network.QueuedPromptPayload(item)}
	err := tx.QueryRowContext(ctx, `UPDATE sessions SET last_seq=last_seq+1,last_event_at=?,
		title=CASE WHEN title='' THEN ? ELSE title END
		WHERE id=? AND conversation_state='starting' AND ended_at=0 AND archived_at=0 AND node_id=`+state.LocalNodeSQL+` RETURNING last_seq`,
		now.UnixNano(), network.TitleFromPrompt(item.Input.Text), id).Scan(&entry.Seq)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO conversation_events(session_id,seq,body) VALUES (?,?,?)", id, entry.Seq, string(network.MustRawJSON(entry)))
	return err
}

// RemoveStartingPrompt drops a prompt queued while its chat was still starting, as the worker
// would ("removed"), so the worker it starts never sends it. False when there was nothing to drop
// here: the worker took over, or the chat is not starting.
func (g *Gateway) RemoveStartingPrompt(ctx context.Context, id, queueID string) (bool, error) {
	directory := filepath.Join(g.options.Home, "network", "sessions", id)
	if err := paths.EnsurePrivateDirectory(directory); err != nil {
		return false, err
	}
	lock, err := network.LockPromptHandoff(ctx, directory)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if _, err := g.workerRecord(ctx, id); err == nil {
		return false, nil
	}
	err = state.Transaction(ctx, g.store.DB, func(tx *sql.Tx) error {
		now := time.Now()
		entry := acp.LogEntry{TS: now.UnixMilli(), Kind: "dequeued_prompt", Payload: network.MustRawJSON(map[string]string{"queueId": queueID, "reason": "removed"})}
		err := tx.QueryRowContext(ctx, `UPDATE sessions SET last_seq=last_seq+1,last_event_at=?
			WHERE id=? AND conversation_state='starting' AND ended_at=0 AND archived_at=0 AND node_id=`+state.LocalNodeSQL+` RETURNING last_seq`, now.UnixNano(), id).Scan(&entry.Seq)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO conversation_events(session_id,seq,body) VALUES (?,?,?)", id, entry.Seq, string(network.MustRawJSON(entry)))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func promptQueueError(w http.ResponseWriter, r *http.Request, err error) {
	if event := obs.From(r.Context()); event != nil {
		event.Fail(err)
	}
	http.Error(w, "could not queue prompt", http.StatusServiceUnavailable)
}
