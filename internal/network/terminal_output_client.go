package network

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
)

// TerminalReplayTail returns up to maxBytes from the end of the worker PTY replay.
func TerminalReplayTail(ctx context.Context, home, sessionID string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		maxBytes = chunkSize
	}
	if maxBytes > replayLimit {
		maxBytes = replayLimit
	}
	record, err := workerRecord(home, sessionID)
	if err != nil {
		return "", err
	}
	path := "/output?tail=" + strconv.Itoa(maxBytes)
	response, err := workerRequest(ctx, record, "GET", path, "")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errSessionUnavailable
	}
	var out TerminalOutput
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Output, nil
}
