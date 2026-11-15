package network

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
	"github.com/tmaxmax/go-sse"
)

func persistConversationChanges(ctx context.Context, db *sql.DB, id string, changes acp.LogChanges) error {
	return state.Transaction(ctx, db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE sessions SET last_seq=max(last_seq,?),last_event_at=?,
			conversation_truncated=max(conversation_truncated,?) WHERE id=? AND archived_at=0 AND node_id=`+state.LocalNodeSQL,
			changes.LastSeq, time.Now().UnixNano(), changes.Truncated, id)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil {
			return err
		} else if count != 1 {
			var archived bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sessions WHERE id=? AND archived_at>0 AND node_id="+state.LocalNodeSQL+")", id).Scan(&archived); err != nil {
				return err
			}
			if archived {
				return errSessionArchived
			}
			return errSessionNotFound
		}
		for _, seq := range changes.Removed {
			if seq <= 0 || seq > changes.LastSeq {
				return errors.New("invalid absorbed conversation sequence")
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM conversation_events WHERE session_id=? AND seq=?", id, seq); err != nil {
				return err
			}
		}
		insert, err := tx.PrepareContext(ctx, `INSERT INTO conversation_events(session_id,seq,body) VALUES (?,?,?)
			ON CONFLICT(session_id,seq) DO UPDATE SET body=excluded.body WHERE body<>excluded.body`)
		if err != nil {
			return err
		}
		defer insert.Close()
		for _, entry := range changes.Entries {
			if entry.Seq <= 0 || entry.Seq > changes.LastSeq {
				return errors.New("invalid conversation sequence")
			}
			body, err := json.Marshal(entry)
			if err != nil {
				return fmt.Errorf("encode conversation entry %d: %w", entry.Seq, err)
			}
			if _, err := insert.ExecContext(ctx, id, entry.Seq, string(body)); err != nil {
				return err
			}
		}
		return nil
	})
}

func storedConversationTail(ctx context.Context, db *sql.DB, id string, since int64) ([]acp.LogEntry, int64, bool, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, false, err
	}
	defer tx.Rollback()
	var lastSeq int64
	var truncated bool
	err = tx.QueryRowContext(ctx, "SELECT last_seq,conversation_truncated FROM sessions WHERE id=?", id).Scan(&lastSeq, &truncated)
	if errors.Is(err, sql.ErrNoRows) {
		err = errSessionNotFound
	}
	if err != nil {
		return nil, 0, false, err
	}
	entries, truncated, err := readConversationEntries(ctx, tx, id, since, lastSeq, truncated)
	if err != nil {
		return nil, 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, false, err
	}
	return entries, lastSeq, truncated, nil
}

func readConversationEntries(ctx context.Context, tx *sql.Tx, id string, since, lastSeq int64, truncated bool) ([]acp.LogEntry, bool, error) {
	if lastSeq < 0 {
		return nil, false, errors.New("invalid stored conversation sequence")
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq,length(CAST(body AS BLOB)),
		CASE WHEN length(CAST(body AS BLOB))<=? THEN body ELSE '' END
		FROM conversation_events WHERE session_id=? AND seq>? ORDER BY seq DESC`, maxConversationSnapshotBytes, id, since)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	entries := []acp.LogEntry{}
	total := 0
	for rows.Next() {
		var seq int64
		var size int
		var body string
		if err := rows.Scan(&seq, &size, &body); err != nil {
			return nil, false, err
		}
		if size+1 > maxConversationSnapshotBytes-total {
			truncated = true
			break
		}
		var entry acp.LogEntry
		if err := json.Unmarshal([]byte(body), &entry); err != nil {
			return nil, false, fmt.Errorf("decode conversation entry %d: %w", seq, err)
		}
		if seq <= 0 || entry.Seq != seq || seq > lastSeq {
			return nil, false, errors.New("invalid stored conversation sequence")
		}
		total += size + 1
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	slices.Reverse(entries)
	return entries, truncated, nil
}

func newConversationLog(ctx context.Context, store *state.Store, id string, fail func(error)) (*acp.EventLog, error) {
	entries, lastSeq, truncated, err := storedConversationTail(ctx, store.Reader, id, 0)
	if err != nil {
		return nil, err
	}
	log := acp.NewEventLog(maxConversationSnapshotBytes)
	if err := log.Restore(entries, lastSeq, truncated); err != nil {
		return nil, err
	}
	var report sync.Once
	err = log.EnableSink(func(changes acp.LogChanges) error {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err := persistConversationChanges(writeCtx, store.DB, id, changes)
		if err != nil {
			if err == errSessionArchived {
				fail(err)
				return err
			}
			report.Do(func() {
				_, event := obs.Start(ctx, "worker", "persist_conversation")
				event.Set("session_id", id)
				event.Fail(err)
				event.End()
				fail(err)
			})
		}
		return err
	})
	return log, err
}

func (g *Gateway) serveConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	info, err := storedSession(r.Context(), g.auth.store.Reader, id)
	if err == nil && !info.ArchivedAt.IsZero() {
		g.serveArchivedConversation(w, r, info, g.tailnetTarget)
		return
	}
	if record, err := g.workerRecord(r.Context(), id); err == nil && proxyWorkerConversation(w, r, record, "") {
		return
	}
	if r.Context().Err() != nil {
		return
	}
	if errors.Is(err, errSessionNotFound) || err == nil && info.Transport != "acp" {
		http.Error(w, "conversation unavailable", http.StatusNotFound)
		return
	}
	var entries []acp.LogEntry
	var truncated bool
	if err == nil {
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		entries, _, truncated, err = storedConversationTail(r.Context(), g.auth.store.Reader, id, since)
	}
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if event := obs.From(r.Context()); event != nil {
			event.Fail(err)
		}
		http.Error(w, "conversation history unavailable", http.StatusInternalServerError)
		return
	}
	writeConversationEntries(w, r, entries, truncated)
}

func writeConversationEntries(w http.ResponseWriter, r *http.Request, entries []acp.LogEntry, truncated bool) {
	if truncated {
		w.Header().Set("X-Agentpack-Truncated", "true")
	}
	stream := r.URL.Query().Get("stream") == "1" || strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		for _, entry := range entries {
			data, err := json.Marshal(entry)
			if err != nil {
				return
			}
			var message sse.Message
			message.AppendData(string(data))
			if _, err := message.WriteTo(w); err != nil {
				return
			}
		}
		sent := false
		_ = writeTruncatedSSE(w, truncated, &sent)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	encoder := json.NewEncoder(w)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			return
		}
	}
}
