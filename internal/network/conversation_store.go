package network

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
	"github.com/tmaxmax/go-sse"
)

// PersistConversationChangesSince is for a live log restored at restoredSeq: its agent process is new,
// so a running cost total logged before then belongs to the previous process. The batch, the row's
// derived columns and the fold's checkpoint commit together, so the row is always the fold of the
// stored events. holdStarting keeps an instant start's row "starting" until its worker serves.
func PersistConversationChangesSince(ctx context.Context, db *sql.DB, id string, changes acp.LogChanges, restoredSeq int64, holdStarting bool) error {
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
				return session.ErrSessionArchived
			}
			return session.ErrSessionNotFound
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
			usage, counts := acp.ParseUsage(entry)
			// A replayed or rewritten event already counted when its row was first inserted.
			if counts {
				var exists bool
				if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM conversation_events WHERE session_id=? AND seq=?)", id, entry.Seq).Scan(&exists); err != nil {
					return err
				}
				counts = !exists
			}
			if _, err := insert.ExecContext(ctx, id, entry.Seq, string(body)); err != nil {
				return err
			}
			if entry.Kind == "agent_exit" && entry.Seq > restoredSeq {
				if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state=json_set(state,'$.running',json('false'))
					WHERE id=? AND json_extract(state,'$.running')=1`, id); err != nil {
					return err
				}
			}
			if counts {
				if err := recordUsage(ctx, tx, id, entry, usage, restoredSeq); err != nil {
					return fmt.Errorf("record usage of entry %d: %w", entry.Seq, err)
				}
			}
		}
		return storeConversationFold(ctx, tx, id, changes.Checkpoint, holdStarting)
	})
}

// storeConversationFold writes the fold's derived columns and checkpoint; nil means the log has no fold.
func storeConversationFold(ctx context.Context, tx *sql.Tx, id string, checkpoint json.RawMessage, holdStarting bool) error {
	if checkpoint == nil {
		return nil
	}
	var fold conversationFold
	if err := json.Unmarshal(checkpoint, &fold); err != nil {
		return fmt.Errorf("decode conversation checkpoint: %w", err)
	}
	derived := fold.derived()
	var goal any
	if fold.GoalKnown {
		goal = string(fold.Goal)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET
		conversation_state=CASE WHEN ? AND conversation_state='starting' THEN 'starting' ELSE ? END,
		waiting=?,subagents=?,goal=coalesce(?,goal),title=CASE WHEN ?='' THEN title ELSE ? END
		WHERE id=? AND node_id=`+state.LocalNodeSQL,
		holdStarting, derived.State, sessionJSON(derived.Waiting), sessionJSON(derived.Subagents), goal,
		fold.Title, fold.Title, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO conversation_checkpoints(session_id,seq,body) VALUES (?,?,?)
		ON CONFLICT(session_id) DO UPDATE SET seq=excluded.seq,body=excluded.body`, id, fold.Seq, string(checkpoint))
	return err
}

// storedConversationFold is the session's last checkpoint, or a new fold at seq 0.
func storedConversationFold(ctx context.Context, db *sql.DB, id string) (*conversationFold, error) {
	var body string
	err := db.QueryRowContext(ctx, "SELECT body FROM conversation_checkpoints WHERE session_id=?", id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return &conversationFold{}, nil
	}
	if err != nil {
		return nil, err
	}
	fold := &conversationFold{}
	if json.Unmarshal([]byte(body), fold) != nil {
		return &conversationFold{}, nil // an unreadable checkpoint is rebuilt from the tail
	}
	return fold, nil
}

// recordUsage adds one new event's usage to its session and to this node's hour. Rows of this node have no
// other writer, so the increments survive replication.
func recordUsage(ctx context.Context, tx *sql.Tx, id string, entry acp.LogEntry, usage acp.Usage, restoredSeq int64) error {
	if usage.CostTotalMicros >= 0 {
		// Stored as the difference from the previous total; a smaller total means the adapter started over.
		var previous sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT body FROM conversation_events WHERE session_id=? AND seq<? AND seq>?
			AND kind='usage_update' AND json_extract(body,'$.payload.cost.amount') IS NOT NULL
			AND json_extract(body,'$.payload._subagent') IS NULL ORDER BY seq DESC LIMIT 1`, id, entry.Seq, restoredSeq).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		usage.CostMicros = usage.CostTotalMicros
		var last acp.LogEntry
		if previous.Valid && json.Unmarshal([]byte(previous.String), &last) == nil {
			if before, ok := acp.ParseUsage(last); ok && before.CostTotalMicros >= 0 && before.CostTotalMicros <= usage.CostTotalMicros {
				usage.CostMicros = usage.CostTotalMicros - before.CostTotalMicros
			}
		}
	}
	var agentMS int64
	if entry.Kind == "turn_end" {
		// Agent time runs from this turn's prompt; an autonomous cycle has none and adds no time.
		var kind string
		var at int64
		err := tx.QueryRowContext(ctx, `SELECT kind,json_extract(body,'$.ts') FROM conversation_events
			WHERE session_id=? AND seq<? AND kind IN ('prompt','turn_end') ORDER BY seq DESC LIMIT 1`, id, entry.Seq).Scan(&kind, &at)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if kind == "prompt" {
			agentMS = max(0, entry.TS-at)
		}
	}
	var harness, folder, model string
	var turns int64
	if err := tx.QueryRowContext(ctx, `UPDATE sessions SET turns=turns+?,input_tokens=input_tokens+?,output_tokens=output_tokens+?,
		cache_read_tokens=cache_read_tokens+?,cache_write_tokens=cache_write_tokens+?,
		cost_micros=CASE WHEN ?<0 THEN cost_micros ELSE max(cost_micros,0)+? END,model=CASE WHEN ?='' THEN model ELSE ? END
		WHERE id=? RETURNING harness,folder_id,model,turns`,
		usage.Turns, usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens,
		usage.CostMicros, usage.CostMicros, usage.Model, usage.Model, id).Scan(&harness, &folder, &model, &turns); err != nil {
		return err
	}
	if usage.Turns == 0 && usage.CostMicros <= 0 {
		return nil
	}
	firstTurn := 0
	if usage.Turns > 0 && turns == usage.Turns {
		firstTurn = 1
	}
	hour := entry.TS / int64(time.Hour/time.Millisecond) * 3600
	_, err := tx.ExecContext(ctx, `INSERT INTO usage_hourly(hour,node_id,harness,folder_id,model,turns,sessions,input_tokens,output_tokens,
		cache_read_tokens,cache_write_tokens,cost_micros,agent_ms) VALUES (?,`+state.LocalNodeSQL+`,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(hour,node_id,harness,folder_id,model) DO UPDATE SET turns=turns+excluded.turns,sessions=sessions+excluded.sessions,
		input_tokens=input_tokens+excluded.input_tokens,output_tokens=output_tokens+excluded.output_tokens,
		cache_read_tokens=cache_read_tokens+excluded.cache_read_tokens,cache_write_tokens=cache_write_tokens+excluded.cache_write_tokens,
		cost_micros=cost_micros+excluded.cost_micros,agent_ms=agent_ms+excluded.agent_ms`,
		hour, harness, folder, model, usage.Turns, firstTurn, usage.InputTokens, usage.OutputTokens,
		usage.CacheReadTokens, usage.CacheWriteTokens, max(usage.CostMicros, 0), agentMS)
	return err
}

func StoredConversationTail(ctx context.Context, db *sql.DB, id string, since int64) ([]acp.LogEntry, int64, bool, error) {
	return StoredConversationRange(ctx, db, id, conversationRange{since: since})
}

// MaxConversationSnapshotBytes caps one conversation read, matching the worker log budget.
const MaxConversationSnapshotBytes = 4 << 20

// Older history arrives in pages this size, so scrolling up stays quick on long conversations.
const conversationPageBytes = 1 << 20

// conversationRange selects seq in (since, before); before 0 means the newest tail.
type conversationRange struct {
	since, Before int64
	snapshot      bool
}

// ConversationPage reads ?before= from a request: 0 when absent or invalid.
func ConversationPage(r *http.Request) conversationRange {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if IsConversationStream(r) {
		lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
		since = min(max(since, lastID), math.MaxInt64-1)
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	return conversationRange{since: max(since, 0), Before: max(before, 0), snapshot: !IsConversationStream(r)}
}

// readConversationEntries returns the newest entries in the range that fit the byte budget, oldest first.
// truncated reports that older entries exist that were not returned. For the tail it also carries the
// session's recorded loss; a page reports only its own budget, since the rest is still stored.
// storedConversationRange reads one range of a session's stored conversation and its last sequence.
func StoredConversationRange(ctx context.Context, db *sql.DB, id string, span conversationRange) ([]acp.LogEntry, int64, bool, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, false, err
	}
	defer tx.Rollback()
	var lastSeq int64
	var truncated bool
	err = tx.QueryRowContext(ctx, "SELECT last_seq,conversation_truncated FROM sessions WHERE id=?", id).Scan(&lastSeq, &truncated)
	if errors.Is(err, sql.ErrNoRows) {
		err = session.ErrSessionNotFound
	}
	if err != nil {
		return nil, 0, false, err
	}
	entries, truncated, err := ReadConversationEntries(ctx, tx, id, span, lastSeq, truncated)
	if err != nil {
		return nil, 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, false, err
	}
	return entries, lastSeq, truncated, nil
}

func ReadConversationEntries(ctx context.Context, tx *sql.Tx, id string, span conversationRange, lastSeq int64, truncated bool) ([]acp.LogEntry, bool, error) {
	if lastSeq < 0 {
		return nil, false, errors.New("invalid stored conversation sequence")
	}
	budget := MaxConversationSnapshotBytes
	if span.snapshot && span.since == 0 {
		budget = 256 << 10
	}
	if span.Before > 0 {
		budget, truncated = conversationPageBytes, false
	}
	upper := span.Before
	if upper == 0 {
		upper = lastSeq + 1
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq,length(CAST(body AS BLOB)),
		CASE WHEN length(CAST(body AS BLOB))<=? THEN body ELSE '' END
		FROM conversation_events WHERE session_id=? AND seq>? AND seq<? ORDER BY seq DESC`, MaxConversationSnapshotBytes, id, span.since, upper)
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
		// Keep a single large event readable so its sequence remains a usable paging cursor.
		if size+1 > budget-total && !(span.snapshot && len(entries) == 0 && size+1 <= MaxConversationSnapshotBytes) {
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

// NewConversationLog restores a session's log and its fold, and stores what is appended to it.
// starting, when set, holds an instant start's row "starting" until it is cleared and Republish runs.
func NewConversationLog(ctx context.Context, store *state.Store, id string, fail func(error), starting *atomic.Bool) (*acp.EventLog, error) {
	entries, lastSeq, truncated, err := StoredConversationTail(ctx, store.Reader, id, 0)
	if err != nil {
		return nil, err
	}
	log := acp.NewEventLog(MaxConversationSnapshotBytes)
	if err := log.Restore(entries, lastSeq, truncated); err != nil {
		return nil, err
	}
	fold, err := storedConversationFold(ctx, store.Reader, id)
	if err != nil {
		return nil, err
	}
	if fold.Seq > lastSeq || len(entries) > 0 && fold.Seq < entries[0].Seq-1 {
		fold = &conversationFold{} // not from this log, or older than the tail: fold the tail as before
	}
	log.SetFold(fold, fold.Seq)
	// A failing write is retried by the log, which keeps the events in memory meanwhile; only an
	// archived chat stops the worker. Each run of failures is recorded once.
	var failing atomic.Bool
	err = log.EnableSink(func(changes acp.LogChanges) error {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err := PersistConversationChangesSince(writeCtx, store.DB, id, changes, lastSeq, starting != nil && starting.Load())
		switch {
		case err == session.ErrSessionArchived:
			fail(err)
		case err != nil && !failing.Swap(true):
			_, event := obs.Start(ctx, "worker", "persist_conversation")
			event.Set("session_id", id)
			event.Fail(err)
			event.End()
		case err == nil:
			failing.Store(false)
		}
		return err
	})
	return log, err
}

func WriteConversationEntries(w http.ResponseWriter, r *http.Request, entries []acp.LogEntry, truncated bool) {
	if truncated {
		w.Header().Set("X-Agentpack-Truncated", "true")
	}
	if IsConversationStream(r) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		for _, entry := range entries {
			if err := writeConversationSSE(w, entry); err != nil {
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

func IsConversationStream(r *http.Request) bool {
	return r.URL.Query().Get("stream") == "1" || strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

func writeConversationSSE(w io.Writer, entry acp.LogEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	message := sse.Message{ID: sse.ID(strconv.FormatInt(entry.Seq, 10))}
	message.AppendData(string(data))
	_, err = message.WriteTo(w)
	return err
}

func StoredPromptID(ctx context.Context, store *state.Store, session, id string) (bool, error) {
	var exists bool
	err := store.Reader.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM conversation_events WHERE session_id=? AND json_extract(body, '$.payload.queueId')=?
		AND kind IN ('queued_prompt', 'prompt', 'dequeued_prompt')
	)`, session, id).Scan(&exists)
	return exists, err
}

// conversationStore is a conversation's persistence adapter. The log's sink (NewConversationLog)
// stores events with the fold; the adapter stores the rest: the agent snapshot, the remembered
// permission mode, images, and stored prompt IDs. Work a log callback asks for runs on its one
// writer goroutine, in order, so no database write runs while the log holds an append.
//
// Its fields are guarded by the core's mu; the writer runs jobs with no lock held.
type conversationStore struct {
	ctx       context.Context // the worker's, for diagnostics
	db        *state.Store    // nil until wired: nothing is stored
	info      session.SessionInfo
	directory string // the session's directory, which holds its blobs
	jobs      []func()
	writing   bool
}

// wireStore connects the conversation to the state store and the session's directory.
func (cs *ConversationSession) wireStore(ctx context.Context, db *state.Store, info session.SessionInfo, directory string) {
	info.ID = filepath.Base(directory)
	cs.mu.Lock()
	cs.store.ctx, cs.store.db, cs.store.info, cs.store.directory = ctx, db, info, directory
	cs.mu.Unlock()
}

// later queues job for the writer goroutine. The caller holds mu.
func (cs *ConversationSession) later(job func()) {
	cs.store.jobs = append(cs.store.jobs, job)
	if !cs.store.writing {
		cs.store.writing = true
		obs.Go("acp.persist", cs.runStoreJobs)
	}
}

func (cs *ConversationSession) runStoreJobs() {
	for {
		cs.mu.Lock()
		if len(cs.store.jobs) == 0 {
			cs.store.writing = false
			cs.mu.Unlock()
			return
		}
		job := cs.store.jobs[0]
		cs.store.jobs = cs.store.jobs[1:]
		cs.mu.Unlock()
		job()
	}
}

// wired is the adapter as wired now.
func (cs *ConversationSession) wired() conversationStore {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return conversationStore{ctx: cs.store.ctx, db: cs.store.db, info: cs.store.info, directory: cs.store.directory}
}

// saveSnapshot stores the agent's options and commands for chats that have not started it yet.
func (cs *ConversationSession) saveSnapshot() {
	if store := cs.wired(); store.db != nil {
		persistAgentSnapshot(store.ctx, store.db.DB, store.info, cs)
	}
}

// rememberPermissions keeps the chosen permission mode for this agent on every chat and machine.
func (cs *ConversationSession) rememberPermissions(mode string) {
	store := cs.wired()
	if store.db == nil {
		return
	}
	if err := store.db.Mesh().SetSetting(context.Background(), permissionSettingKey(store.info.Harness), mode); err != nil {
		_, event := obs.Start(store.ctx, "worker", "remember_permissions")
		event.Fail(err)
		event.End()
	}
}

func permissionSettingKey(harness string) string { return "permission_mode." + harness }

// promptStored reports whether a prompt with this queue ID is already in the stored log.
func (cs *ConversationSession) promptStored(ctx context.Context, id string) (bool, error) {
	store := cs.wired()
	if store.db == nil {
		return false, nil
	}
	return StoredPromptID(ctx, store.db, store.info.ID, id)
}

// storeImage keeps an image an agent or tool sent in the session's blob store.
func (cs *ConversationSession) storeImage(data []byte) (string, error) {
	store := cs.wired()
	if store.db == nil {
		return "", errors.New("image storage unavailable")
	}
	sha, err := StoreSessionBlob(store.directory, data)
	switch {
	case errors.Is(err, ErrSessionBlobQuota):
		return "", err
	case err != nil && SniffBlobMIME(data) == "":
		return "", errors.New("unsupported image type")
	case err != nil:
		return "", errors.New("could not store it")
	}
	// The image shows on this machine either way; peers find it once its location is recorded.
	if err := AdvertiseBlob(context.Background(), store.db, store.info.ID, sha, int64(len(data))); err != nil {
		_, event := obs.Start(store.ctx, "worker", "advertise_blob")
		event.Set("session_id", store.info.ID)
		event.Fail(err)
		event.End()
	}
	return sha, nil
}
