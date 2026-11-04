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
