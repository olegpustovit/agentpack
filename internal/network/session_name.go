package network

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

func SessionState(ctx context.Context, record WorkerRecord) (session.TerminalState, error) {
	response, err := WorkerRequest(ctx, record, http.MethodGet, "/state", "")
	if err != nil {
		return session.TerminalState{}, errors.Join(session.ErrSessionUnavailable, err)
	}
	defer response.Body.Close()
	var state session.TerminalState
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&state) != nil {
		return session.TerminalState{}, session.ErrSessionUnavailable
	}
	return state, nil
}

func RenameSession(ctx context.Context, store *state.Store, home, id, name string) (session.SessionInfo, error) {
	name, err := NormalizeSessionName(name)
	if err != nil {
		return session.SessionInfo{}, err
	}
	info, err := session.StoredSession(ctx, store.Reader, id)
	if err != nil {
		return info, err
	}
	local, err := store.NodeID(ctx)
	if err != nil {
		return info, err
	}
	if record, recordErr := ReadWorkerRecord(home, id); info.NodeID == local && recordErr == nil {
		info.State, err = SessionState(ctx, record)
		if err != nil {
			return info, err
		}
	} else if info.NodeID == local {
		info.State.Running, info.State.Restarting = false, false
	}
	_, err = store.DB.ExecContext(ctx, "UPDATE sessions SET name=? WHERE id=?", name, id)
	info.Name = name
	return info, err
}

var ErrSessionRunning = errors.New("session is running")

// HideSession archives a stopped session from the user's view: it leaves every list on the mesh, and its
// history stays in place until the hourly mover takes it to the archive like any other idle chat.
func HideSession(ctx context.Context, store *state.Store, home, id string) (bool, error) {
	var hidden, starting bool
	err := store.Reader.QueryRowContext(ctx, `SELECT hidden, conversation_state='starting' AND ended_at=0 FROM sessions WHERE id=?`, id).Scan(&hidden, &starting)
	if errors.Is(err, sql.ErrNoRows) {
		return false, session.ErrSessionNotFound
	}
	if err != nil || hidden {
		return false, err
	}
	// A starting chat can have no endpoint yet. Check again in the write in case it just resumed.
	if _, err := ReadWorkerRecord(home, id); err == nil || starting {
		return false, ErrSessionRunning
	}
	result, err := store.DB.ExecContext(ctx, `UPDATE sessions SET hidden=1 WHERE id=? AND hidden=0
		AND NOT (conversation_state='starting' AND ended_at=0)`, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 0 {
		return count != 0, err
	}
	if err := store.Reader.QueryRowContext(ctx, "SELECT hidden FROM sessions WHERE id=?", id).Scan(&hidden); err != nil {
		return false, err
	}
	if !hidden {
		return false, ErrSessionRunning
	}
	return false, nil
}
