package network

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
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

func sessionNamePath(home, id string) (string, error) {
	if !strings.HasPrefix(id, "session-") || filepath.Base(id) != id || strings.ContainsAny(id, "/\\") {
		return "", errors.New("invalid session ID")
	}
	return filepath.Join(home, "network", "sessions", id, "name"), nil
}

func readSessionName(home, id string) (string, error) {
	path, err := sessionNamePath(home, id)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return normalizeSessionName(string(data))
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
	var info SessionInfo
	name, err := normalizeSessionName(name)
	if err != nil {
		return info, err
	}
	record, err := workerRecord(home, id)
	if err != nil {
		return info, errors.Join(errSessionNotFound, err)
	}
	state, err := sessionState(ctx, record)
	if err != nil {
		return info, err
	}
	path, err := sessionNamePath(home, id)
	if err != nil {
		return info, err
	}
	if name == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return info, err
		}
	} else {
		directory := filepath.Dir(path)
		file, err := os.CreateTemp(directory, ".name-*")
		if err != nil {
			return info, err
		}
		defer os.Remove(file.Name())
		if err = file.Chmod(0600); err == nil {
			_, err = file.WriteString(name)
		}
		if err = errors.Join(err, file.Close()); err != nil {
			return info, err
		}
		if err := os.Rename(file.Name(), path); err != nil {
			return info, err
		}
	}
	info = record.Session
	info.ID, info.Name, info.State = id, name, state
	return info, nil
}
