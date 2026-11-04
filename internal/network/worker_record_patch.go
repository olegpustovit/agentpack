package network

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// patchWorkerConversationState updates conversation_state on the worker record when it changes.
func patchWorkerConversationState(sessionDir, state string) error {
	path := filepath.Join(sessionDir, "worker.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var record WorkerRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	if record.Session.ConversationState == state {
		return nil
	}
	record.Session.ConversationState = state
	updated, err := json.Marshal(record)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, updated, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
