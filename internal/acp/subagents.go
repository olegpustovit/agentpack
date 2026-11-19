package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
)

// SubagentKey marks a log entry that belongs to a subagent rather than to the conversation itself.
// Its value is the child's ACP session ID, or "tool:<toolCallId>" for an agent that flattens its
// subagents into the parent session (not cancellable).
const SubagentKey = "_subagent"

// ErrSubagentNotCancellable reports a child the agent did not announce with a cancel capability.
var ErrSubagentNotCancellable = errors.New("subagent cannot be cancelled")

// noteSubagent records a subagent_update announcement. The RFD requires the announcement before any
// message bearing the child's session ID, so announced IDs are exactly the child sessions.
// Updates are patches: capabilities replace the earlier ones only when present.
func (log *EventLog) noteSubagent(id string, capabilities json.RawMessage) {
	if id == "" {
		return
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.subagents == nil {
		log.subagents = map[string]bool{}
	}
	cancellable := log.subagents[id]
	if len(capabilities) > 0 {
		var caps struct {
			Cancel json.RawMessage `json:"cancel"`
		}
		_ = json.Unmarshal(capabilities, &caps)
		cancellable = len(caps.Cancel) > 0 && string(caps.Cancel) != "null"
	}
	log.subagents[id] = cancellable
}

// subagentSession returns sessionID when it is an announced child session, else "".
func (log *EventLog) subagentSession(sessionID string) string {
	log.mu.RLock()
	defer log.mu.RUnlock()
	if _, ok := log.subagents[sessionID]; ok {
		return sessionID
	}
	return ""
}

// SubagentCancellable reports whether the agent announced id with the cancel capability.
func (log *EventLog) SubagentCancellable(id string) bool {
	log.mu.RLock()
	defer log.mu.RUnlock()
	return log.subagents[id]
}

// CancelSubagent sends session/cancel for one announced child, leaving the parent turn running.
func CancelSubagent(ctx context.Context, log *EventLog, broker *Broker, id string) error {
	if strings.HasPrefix(id, "tool:") || !log.SubagentCancellable(id) {
		return ErrSubagentNotCancellable
	}
	return broker.cancelSession(ctx, id)
}

// subagentID picks the stamp for an update: the announced child session, else a flattened-subagent
// marker from the agent's _meta (Claude's parentToolUseId, OpenCode's child session).
func subagentID(child string, meta map[string]any) string {
	if child != "" {
		return child
	}
	if claude, _ := meta["claudeCode"].(map[string]any); claude != nil {
		if id, _ := claude["parentToolUseId"].(string); id != "" {
			return "tool:" + id
		}
	}
	switch v := meta["opencode/child-session"].(type) {
	case string:
		return v
	case map[string]any:
		id, _ := v["sessionId"].(string)
		return id
	}
	return ""
}

// stampSubagent adds the top-level SubagentKey to an object payload.
func stampSubagent(payload json.RawMessage, id string) json.RawMessage {
	if id == "" || len(payload) < 2 || payload[0] != '{' {
		return payload
	}
	key, _ := json.Marshal(id)
	out := make(json.RawMessage, 0, len(payload)+len(key)+16)
	out = append(out, `{"`+SubagentKey+`":`...)
	out = append(out, key...)
	if rest := strings.TrimSpace(string(payload[1:])); rest != "}" {
		out = append(out, ',')
	}
	return append(out, payload[1:]...)
}

func payloadSubagent(payload json.RawMessage) string {
	var body struct {
		Subagent string `json:"_subagent"`
	}
	_ = json.Unmarshal(payload, &body)
	return body.Subagent
}

// AppendRawSessionUpdate logs session/update variants SDK v0.13.5 cannot decode and reports
// whether the line was consumed. It also registers announced subagents before their own updates
// reach the SDK, so HarnessClient can stamp them.
func AppendRawSessionUpdate(log *EventLog, line []byte) bool {
	var message struct {
		Method string `json:"method"`
		Params struct {
			SessionID string          `json:"sessionId"`
			Update    json.RawMessage `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &message) != nil || message.Method != "session/update" || len(message.Params.Update) == 0 {
		return false
	}
	var update struct {
		Kind         string          `json:"sessionUpdate"`
		SessionID    string          `json:"sessionId"`
		Capabilities json.RawMessage `json:"capabilities"`
		Content      json.RawMessage `json:"content"`
		Meta         map[string]any  `json:"_meta"`
	}
	if json.Unmarshal(message.Params.Update, &update) != nil {
		return false
	}
	switch update.Kind {
	case "subagent_update":
		log.noteSubagent(update.SessionID, update.Capabilities)
	case "notice", "compaction_update", "compaction_summary_chunk", "session_message", "session_message_chunk":
	case "tool_call", "tool_call_update":
		// The SDK rejects the RFD's {"type":"session"} tool call content.
		if !hasSessionContent(update.Content) {
			return false
		}
	default:
		return false
	}
	id := subagentID(log.subagentSession(message.Params.SessionID), update.Meta)
	log.Append(update.Kind, time.Now().UnixMilli(), stampSubagent(message.Params.Update, id))
	return true
}

func hasSessionContent(content json.RawMessage) bool {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		if block.Type == "session" {
			return true
		}
	}
	return false
}

func sessionUpdateMeta(u sdkacp.SessionUpdate) map[string]any {
	switch {
	case u.AgentMessageChunk != nil:
		return u.AgentMessageChunk.Meta
	case u.AgentThoughtChunk != nil:
		return u.AgentThoughtChunk.Meta
	case u.UserMessageChunk != nil:
		return u.UserMessageChunk.Meta
	case u.ToolCall != nil:
		return u.ToolCall.Meta
	case u.ToolCallUpdate != nil:
		return u.ToolCallUpdate.Meta
	}
	return nil
}
