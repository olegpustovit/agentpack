package network

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/olegpustovit/agentpack/internal/state"
)

var (
	errSessionArchived    = errors.New("archived")
	errSessionNotFound    = errors.New("session not found")
	errSessionUnavailable = errors.New("session unavailable")
)

func sessionState(ctx context.Context, record WorkerRecord) (TerminalState, error) {
	response, err := workerRequest(ctx, record, http.MethodGet, "/state", "")
	if err != nil {
		return TerminalState{}, errors.Join(errSessionUnavailable, err)
	}
	defer response.Body.Close()
	var state TerminalState
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&state) != nil {
		return TerminalState{}, errSessionUnavailable
	}
	return state, nil
}

func normalizeSessionName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 80 {
		return "", errors.New("session name must be at most 80 characters")
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return "", errors.New("session name cannot contain control characters")
		}
	}
	return name, nil
}

func RenameSession(ctx context.Context, home, id, name string) (SessionInfo, error) {
	name, err := normalizeSessionName(name)
	if err != nil {
		return SessionInfo{}, err
	}
	store, err := state.Open(home)
	if err != nil {
		return SessionInfo{}, err
	}
	defer store.Close()
	info, err := storedSession(ctx, store.Reader, id)
	if err != nil {
		return info, err
	}
	local, err := store.NodeID(ctx)
	if err != nil {
		return info, err
	}
	if record, recordErr := workerRecord(home, id); info.NodeID == local && recordErr == nil {
		info.State, err = sessionState(ctx, record)
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

var errSessionRunning = errors.New("session is running")

// HideSession archives a stopped session from the user's view: it leaves every list on the mesh, and its
// history stays in place until the hourly mover takes it to the archive like any other idle chat.
func HideSession(ctx context.Context, home, id string) error {
	store, err := state.Open(home)
	if err != nil {
		return err
	}
	defer store.Close()
	info, err := storedSession(ctx, store.Reader, id)
	if err != nil {
		return err
	}
	// A starting chat has no worker record yet, but its agent is on the way up.
	if _, err := workerRecord(home, id); err == nil || info.ConversationState == "starting" && info.EndedAt.IsZero() {
		return errSessionRunning
	}
	_, err = store.DB.ExecContext(ctx, "UPDATE sessions SET hidden=1 WHERE id=? AND hidden=0", id)
	return err
}
