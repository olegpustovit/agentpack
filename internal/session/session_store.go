package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/state"
)

const sessionWriteColumns = `id,last_used,folder_id,name,directory,definition_root,harness,mode,
	transport,agent_session_id,agent_capabilities,conversation_state,state,started_at,started_by,title,worktree_of`

// goal, subagents, waiting and an ACP chat's conversation_state are written by its log's sink (storeConversationFold), so upserts never clobber them.
// Upserts set worktree_of, parent_id, tools and keep_native_subagents only on insert.
// Tools can change via POST /tools.
const SessionColumns = sessionWriteColumns + ",node_id,ended_at,archived_at,goal,subagents,waiting,last_event_at,parent_id,tools,keep_native_subagents"

func SessionValues(info SessionInfo) ([]any, error) {
	if info.ID == "" {
		return nil, errors.New("session ID is required")
	}
	capabilities, err := json.Marshal(info.AgentCapabilities)
	if err != nil {
		return nil, err
	}
	owner, err := json.Marshal(info.StartedBy)
	if err != nil {
		return nil, err
	}
	terminal, err := json.Marshal(info.State)
	if err != nil {
		return nil, err
	}
	var email string
	if info.StartedBy != nil {
		email, _ = NormalizeEmail(info.StartedBy.User)
	}
	return []any{info.ID, SessionTimestamp(info.LastUsed), info.FolderID, info.Name, info.Directory,
		info.DefinitionRoot, info.Harness, info.Mode, info.Transport, info.AgentSessionID,
		string(capabilities), info.ConversationState, string(terminal), SessionTimestamp(info.StartedAt),
		string(owner), info.Title, info.WorktreeOf, email, info.ParentID, info.Tools, info.KeepNativeSubagents}, nil
}

const InsertSessionSQL = `INSERT INTO sessions (` + sessionWriteColumns + `,node_id,owner_email,parent_id,tools,keep_native_subagents)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,` + state.LocalNodeSQL + `,?,?,?,?)`

func UpsertSession(ctx context.Context, db *sql.DB, info SessionInfo) error {
	args, err := SessionValues(info)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, InsertSessionSQL+` ON CONFLICT(id) DO UPDATE SET
		last_used=excluded.last_used,folder_id=excluded.folder_id,directory=excluded.directory,
		definition_root=excluded.definition_root,harness=excluded.harness,mode=excluded.mode,
		transport=excluded.transport,agent_session_id=excluded.agent_session_id,
		agent_capabilities=excluded.agent_capabilities,
		conversation_state=CASE WHEN excluded.transport='acp' THEN sessions.conversation_state ELSE excluded.conversation_state END,
		state=excluded.state,started_at=excluded.started_at,started_by=excluded.started_by,
		node_id=excluded.node_id,owner_email=excluded.owner_email,ended_at=0 WHERE sessions.archived_at=0`, args...)
	if err == nil {
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return countErr
		}
		if count == 0 {
			return ErrSessionArchived
		}
	}
	return err
}

func ScanSession(row interface{ Scan(...any) error }) (SessionInfo, error) {
	var info SessionInfo
	var lastUsed, startedAt, endedAt, archivedAt, lastEvent int64
	var capabilities, owner, terminal, goal, subagents, waiting string
	err := row.Scan(&info.ID, &lastUsed, &info.FolderID, &info.Name, &info.Directory,
		&info.DefinitionRoot, &info.Harness, &info.Mode, &info.Transport, &info.AgentSessionID,
		&capabilities, &info.ConversationState, &terminal, &startedAt, &owner, &info.Title, &info.WorktreeOf, &info.NodeID, &endedAt, &archivedAt,
		&goal, &subagents, &waiting, &lastEvent, &info.ParentID, &info.Tools, &info.KeepNativeSubagents)
	if err != nil {
		return info, err
	}
	for _, value := range []struct {
		body string
		into any
	}{{capabilities, &info.AgentCapabilities}, {owner, &info.StartedBy}, {terminal, &info.State}, {goal, &info.Goal}, {subagents, &info.Subagents}, {waiting, &info.Waiting}} {
		if value.body != "" {
			if err := json.Unmarshal([]byte(value.body), value.into); err != nil {
				return info, fmt.Errorf("decode session %s: %w", info.ID, err)
			}
		}
	}
	if lastUsed != 0 {
		info.LastUsed = time.Unix(0, lastUsed).UTC()
	}
	if startedAt != 0 {
		info.StartedAt = time.Unix(0, startedAt).UTC()
	}
	if endedAt != 0 {
		info.EndedAt = time.Unix(0, endedAt).UTC()
	}
	if archivedAt != 0 {
		info.ArchivedAt = time.Unix(0, archivedAt).UTC()
	}
	if lastEvent != 0 {
		info.LastEventAt = time.Unix(0, lastEvent).UTC()
	}
	return info, nil
}

func SessionTimestamp(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

// SessionRowReads counts storedSession calls so tests can pin the reads per session request.
var SessionRowReads atomic.Int64

func StoredSession(ctx context.Context, db *sql.DB, id string) (SessionInfo, error) {
	SessionRowReads.Add(1)
	info, err := ScanSession(db.QueryRowContext(ctx, "SELECT "+SessionColumns+" FROM sessions WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return info, ErrSessionNotFound
	}
	return info, err
}

func EndSession(ctx context.Context, db *sql.DB, id string, terminal TerminalState) error {
	terminal.Running, terminal.Restarting = false, false
	body, err := json.Marshal(terminal)
	if err != nil {
		return err
	}
	return state.Transaction(ctx, db, func(tx *sql.Tx) error {
		// A stopped chat has no turn, answerable request or running subagent left, whatever its last state said.
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET ended_at=?,state=?,conversation_state=CASE WHEN transport='acp' THEN 'idle' ELSE conversation_state END,subagents='',waiting='' WHERE id=? AND node_id="+state.LocalNodeSQL+" AND ended_at=0", time.Now().UnixNano(), string(body), id); err != nil {
			return err
		}
		// A chat that ended before its first message has nothing to keep in the list, unless it failed to start.
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET hidden=1 WHERE id=? AND transport='acp' AND hidden=0 AND node_id=`+state.LocalNodeSQL+`
 AND NOT EXISTS (SELECT 1 FROM conversation_events WHERE session_id=sessions.id AND (kind='prompt' OR kind='notice' AND json_extract(body,'$.payload.severity')='error'))`, id)
		return err
	})
}

func QuerySessions(ctx context.Context, db *sql.DB, query string, args ...any) ([]SessionInfo, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []SessionInfo{}
	for rows.Next() {
		info, err := ScanSession(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, info)
	}
	return entries, rows.Err()
}

var (
	ErrSessionArchived    = errors.New("archived")
	ErrSessionNotFound    = errors.New("session not found")
	ErrSessionUnavailable = errors.New("session unavailable")
)
