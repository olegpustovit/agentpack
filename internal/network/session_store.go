package network

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/olegpustovit/agentpack/internal/state"
)

const sessionWriteColumns = `id,last_used,folder_id,name,directory,definition_root,harness,mode,
	transport,agent_session_id,agent_capabilities,conversation_state,state,started_at,started_by,title,worktree_of`

// goal, subagents and waiting are written only by the worker (worker_record_patch.go), so upserts never clobber them.
// worktree_of, parent_id and tools are set on insert only: tools is the gateway's switch (POST /tools).
const sessionColumns = sessionWriteColumns + ",node_id,ended_at,archived_at,goal,subagents,waiting,last_event_at,parent_id,tools"

func sessionValues(info SessionInfo) ([]any, error) {
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
		email, _ = normalizeEmail(info.StartedBy.User)
	}
	return []any{info.ID, sessionTimestamp(info.LastUsed), info.FolderID, info.Name, info.Directory,
		info.DefinitionRoot, info.Harness, info.Mode, info.Transport, info.AgentSessionID,
		string(capabilities), info.ConversationState, string(terminal), sessionTimestamp(info.StartedAt),
		string(owner), info.Title, info.WorktreeOf, email, info.ParentID, info.Tools}, nil
}

const insertSessionSQL = `INSERT INTO sessions (` + sessionWriteColumns + `,node_id,owner_email,parent_id,tools)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,` + state.LocalNodeSQL + `,?,?,?)`

func upsertSession(ctx context.Context, db *sql.DB, info SessionInfo) error {
	args, err := sessionValues(info)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, insertSessionSQL+` ON CONFLICT(id) DO UPDATE SET
		last_used=excluded.last_used,folder_id=excluded.folder_id,directory=excluded.directory,
		definition_root=excluded.definition_root,harness=excluded.harness,mode=excluded.mode,
		transport=excluded.transport,agent_session_id=excluded.agent_session_id,
		agent_capabilities=excluded.agent_capabilities,conversation_state=excluded.conversation_state,
		state=excluded.state,started_at=excluded.started_at,started_by=excluded.started_by,
		node_id=excluded.node_id,owner_email=excluded.owner_email,ended_at=0 WHERE sessions.archived_at=0`, args...)
	if err == nil {
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return countErr
		}
		if count == 0 {
			return errSessionArchived
		}
	}
	return err
}

func scanSession(row interface{ Scan(...any) error }) (SessionInfo, error) {
	var info SessionInfo
	var lastUsed, startedAt, endedAt, archivedAt, lastEvent int64
	var capabilities, owner, terminal, goal, subagents, waiting string
	err := row.Scan(&info.ID, &lastUsed, &info.FolderID, &info.Name, &info.Directory,
		&info.DefinitionRoot, &info.Harness, &info.Mode, &info.Transport, &info.AgentSessionID,
		&capabilities, &info.ConversationState, &terminal, &startedAt, &owner, &info.Title, &info.WorktreeOf, &info.NodeID, &endedAt, &archivedAt,
		&goal, &subagents, &waiting, &lastEvent, &info.ParentID, &info.Tools)
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

func sessionTimestamp(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

func storedSession(ctx context.Context, db *sql.DB, id string) (SessionInfo, error) {
	info, err := scanSession(db.QueryRowContext(ctx, "SELECT "+sessionColumns+" FROM sessions WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return info, errSessionNotFound
	}
	return info, err
}

func (g *Gateway) workerRecord(ctx context.Context, id string) (WorkerRecord, error) {
	record, err := workerRecord(g.options.Home, id)
	if err != nil {
		return record, err
	}
	info, err := storedSession(ctx, g.auth.store.Reader, id)
	if err == nil {
		local, localErr := g.auth.store.NodeID(ctx)
		if localErr != nil {
			return record, localErr
		}
		if info.NodeID != local {
			return record, errSessionUnavailable
		}
	}
	record.Session = info
	return record, err
}

func endSession(ctx context.Context, db *sql.DB, id string, terminal TerminalState) error {
	terminal.Running, terminal.Restarting = false, false
	body, err := json.Marshal(terminal)
	if err != nil {
		return err
	}
	// A stopped chat has no turn, answerable request or running subagent left, whatever its last state said.
	if _, err = db.ExecContext(ctx, "UPDATE sessions SET ended_at=?,state=?,conversation_state=CASE WHEN transport='acp' THEN 'idle' ELSE conversation_state END,subagents='',waiting='' WHERE id=? AND node_id="+state.LocalNodeSQL+" AND ended_at=0", time.Now().UnixNano(), string(body), id); err != nil {
		return err
	}
	// A chat that ended before its first message has nothing to keep in the list, unless it failed to start.
	_, err = db.ExecContext(ctx, `UPDATE sessions SET hidden=1 WHERE id=? AND transport='acp' AND hidden=0 AND node_id=`+state.LocalNodeSQL+`
 AND NOT EXISTS (SELECT 1 FROM conversation_events WHERE session_id=sessions.id AND (body LIKE '%"kind":"prompt"%' OR body LIKE '%"kind":"notice"%"severity":"error"%'))`, id)
	return err
}

func querySessions(ctx context.Context, db *sql.DB, query string, args ...any) ([]SessionInfo, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []SessionInfo{}
	for rows.Next() {
		info, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, info)
	}
	return entries, rows.Err()
}
