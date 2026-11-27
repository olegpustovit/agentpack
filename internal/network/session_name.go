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
	if info.NodeID == local {
		record, recordErr := ReadWorkerRecord(home, id)
		if info.EndedAt.IsZero() && recordErr == nil {
			info.State, err = SessionState(ctx, record)
			if err != nil {
				return info, err
			}
		} else {
			info.State.Running, info.State.Restarting = false, false
		}
	}
	err = store.DB.QueryRowContext(ctx, "UPDATE sessions SET name=? WHERE id=? AND archived_at=0 RETURNING name", name, id).Scan(&info.Name)
	if errors.Is(err, sql.ErrNoRows) {
		err = session.ErrSessionNotFound
	}
	return info, err
}
