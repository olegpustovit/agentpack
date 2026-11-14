package network

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const sessionColumns = `id,last_used,folder_id,name,directory,definition_root,harness,mode,
	transport,agent_session_id,agent_capabilities,conversation_state,state,started_at,started_by`

func upsertSession(ctx context.Context, db *sql.DB, info SessionInfo) error {
	if info.ID == "" {
		return errors.New("session ID is required")
	}
	capabilities, err := json.Marshal(info.AgentCapabilities)
	if err != nil {
		return err
	}
	owner, err := json.Marshal(info.StartedBy)
	if err != nil {
		return err
	}
	terminal, err := json.Marshal(info.State)
	if err != nil {
		return err
	}
	var email string
	if info.StartedBy != nil {
		email, _ = normalizeEmail(info.StartedBy.User)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO sessions (`+sessionColumns+`,node_id,owner_email)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET
		last_used=excluded.last_used,folder_id=excluded.folder_id,directory=excluded.directory,
		definition_root=excluded.definition_root,harness=excluded.harness,mode=excluded.mode,
		transport=excluded.transport,agent_session_id=excluded.agent_session_id,
		agent_capabilities=excluded.agent_capabilities,conversation_state=excluded.conversation_state,
		state=excluded.state,started_at=excluded.started_at,started_by=excluded.started_by,
		node_id=excluded.node_id,owner_email=excluded.owner_email,ended_at=0`,
		info.ID, sessionTimestamp(info.LastUsed), info.FolderID, info.Name, info.Directory,
		info.DefinitionRoot, info.Harness, info.Mode, info.Transport, info.AgentSessionID,
		string(capabilities), info.ConversationState, string(terminal), sessionTimestamp(info.StartedAt),
		string(owner), localStateNode, email)
	return err
}

func scanSession(row interface{ Scan(...any) error }) (SessionInfo, error) {
	var info SessionInfo
	var lastUsed, startedAt int64
	var capabilities, owner, terminal string
	err := row.Scan(&info.ID, &lastUsed, &info.FolderID, &info.Name, &info.Directory,
		&info.DefinitionRoot, &info.Harness, &info.Mode, &info.Transport, &info.AgentSessionID,
		&capabilities, &info.ConversationState, &terminal, &startedAt, &owner)
	if err != nil {
		return info, err
	}
	for _, value := range []struct {
		body string
		into any
	}{{capabilities, &info.AgentCapabilities}, {owner, &info.StartedBy}, {terminal, &info.State}} {
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
	return info, nil
}

func sessionTimestamp(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

func storedSession(ctx context.Context, db *sql.DB, id string) (SessionInfo, error) {
	info, err := scanSession(db.QueryRowContext(ctx, "SELECT "+sessionColumns+" FROM sessions WHERE id=? AND node_id=?", id, localStateNode))
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
	if errors.Is(err, errSessionNotFound) && record.Session != (SessionInfo{}) {
		// Running legacy workers retain their metadata until the stopped-session import.
		return record, nil
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
	_, err = db.ExecContext(ctx, "UPDATE sessions SET ended_at=?,state=? WHERE id=? AND node_id=? AND ended_at=0", time.Now().UnixNano(), string(body), id, localStateNode)
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
