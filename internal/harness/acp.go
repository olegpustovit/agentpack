package harness

import (
	"encoding/json"
	"os/exec"
)

// ACPLaunch starts an ACP-speaking agent subprocess. Rewrite adjusts client→agent
// JSON-RPC lines; AdaptAgent normalizes agent→client lines into standard ACP plus
// optional harness-specific event-log rows.
type ACPLaunch struct {
	Cmd                  *exec.Cmd
	AuthenticateMethodID string
	Rewrite              func([]byte) []byte
	AdaptAgent           func([]byte) AgentAdaptResult
}

// LogKind is one synthetic event-log row produced by AdaptAgent.
type LogKind struct {
	Kind    string
	Payload json.RawMessage
}

// AgentAdaptResult is the outcome of adapting one agent stdout JSON-RPC line.
// Forward nil leaves the line unchanged; non-nil replaces it (empty drops it).
type AgentAdaptResult struct {
	Forward []byte
	Log     []LogKind
}
