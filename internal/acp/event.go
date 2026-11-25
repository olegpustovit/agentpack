package acp

import "encoding/json"

// LogEntry is one append-only row in a session event log. Payload is the ACP
// sessionUpdate body or a synthetic envelope for prompt, permission, and turn events.
type LogEntry struct {
	Seq     int64           `json:"seq"`
	TS      int64           `json:"ts"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// SteeredPrompt reports whether the prompt joined an existing turn.
func (entry LogEntry) SteeredPrompt() bool {
	if entry.Kind != "prompt" {
		return false
	}
	var payload struct {
		Steered bool `json:"steered"`
	}
	return json.Unmarshal(entry.Payload, &payload) == nil && payload.Steered
}
