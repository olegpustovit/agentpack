package network

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
)

// ConversationSession is the worker-side ACP conversation view.
type ConversationSession struct {
	mu                sync.Mutex
	promptMu          sync.Mutex
	stateWriteMu      sync.Mutex
	log               *acp.EventLog
	broker            *acp.Broker
	reducer           acp.ThreadReducer
	state             string
	agentExited       bool
	initialized       chan struct{}
	configOptions     json.RawMessage
	modes             json.RawMessage
	agentCapabilities *AgentCapabilities
	agentSessionID    string
	stateChange       func(string)
	publishedState    string
	lastSeq           int64
	toolCalls         uint64
	permissions       uint64
	stopReason        string
	stderr            *diagnosticStderr
	agentExitCode     *atomic.Int32
	sessionID         string
}

func NewConversationSession(log *acp.EventLog, broker *acp.Broker) *ConversationSession {
	if log == nil {
		log = acp.NewEventLog(0)
	}
	session := &ConversationSession{log: log, broker: broker, state: "idle", initialized: make(chan struct{})}
	entries := log.SetOnAppend(session.applyLogEntry)
	for _, entry := range entries {
		session.observeEntry(entry)
		if entry.Kind == "agent_exit" {
			session.agentExited = true
		}
		session.state = conversationState(session.reducer.Apply(entry))
	}
	close(session.initialized)
	return session
}

func (session *ConversationSession) Attach(since int64) (entries []acp.LogEntry, truncated bool) {
	return session.log.ReadSince(since)
}

func conversationState(thread acp.ThreadState) string {
	switch {
	case thread.NeedsYou:
		return "needs_you"
	case thread.Working:
		return "working"
	default:
		return "idle"
	}
}

func (session *ConversationSession) applyLogEntry(entry acp.LogEntry) {
	<-session.initialized
	session.mu.Lock()
	session.observeEntry(entry)
	if entry.Kind == "agent_exit" {
		session.agentExited = true
	}
	state := conversationState(session.reducer.Apply(entry))
	changed := state != session.state
	session.state = state
	session.mu.Unlock()
	if changed {
		session.publishState()
	}
}

func (session *ConversationSession) ConversationState() string {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.state
}

func (session *ConversationSession) AgentExited() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.agentExited
}

func (session *ConversationSession) SetStateChange(persistedState string, callback func(string)) {
	session.mu.Lock()
	session.stateChange = callback
	session.publishedState = persistedState
	session.mu.Unlock()
	session.publishState()
}

func (session *ConversationSession) publishState() {
	session.stateWriteMu.Lock()
	defer session.stateWriteMu.Unlock()
	session.mu.Lock()
	callback, state := session.stateChange, session.state
	if callback == nil || state == session.publishedState {
		session.mu.Unlock()
		return
	}
	session.publishedState = state
	session.mu.Unlock()
	callback(state)
}

func (session *ConversationSession) Prompt(ctx context.Context, input acp.PromptInput) (result error) {
	if !session.promptMu.TryLock() {
		return errors.New("ACP session already has an active prompt")
	}
	defer session.promptMu.Unlock()
	_, event := obs.Start(ctx, "acp.turn", "prompt")
	session.mu.Lock()
	broker, exited := session.broker, session.agentExited
	event.Set("session_id", session.sessionID)
	first, tools, permissions := session.lastSeq+1, session.toolCalls, session.permissions
	session.stopReason = ""
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		if session.lastSeq < first {
			first = session.lastSeq
		}
		if session.stopReason == "" && result != nil {
			session.stopReason = "error"
			if errors.Is(result, context.Canceled) {
				session.stopReason = "cancelled"
			}
		}
		event.Set("first_seq", first)
		event.Set("last_seq", session.lastSeq)
		event.Set("tool_calls", session.toolCalls-tools)
		event.Set("permission_requests", session.permissions-permissions)
		event.Set("stop_reason", session.stopReason)
		session.mu.Unlock()
		event.Fail(result)
		event.End()
	}()
	if exited {
		return errors.New("ACP adapter has exited")
	}
	return broker.Prompt(ctx, input)
}

// observeEntry counts events as they arrive, even when the replay tail evicts them.
// The caller holds mu, or is initializing the session before publication.
func (session *ConversationSession) observeEntry(entry acp.LogEntry) {
	session.lastSeq = entry.Seq
	switch entry.Kind {
	case "tool_call":
		session.toolCalls++
	case "permission_request":
		session.permissions++
	case "turn_end":
		var end struct {
			StopReason string `json:"stopReason"`
			Status     string `json:"status"`
		}
		if json.Unmarshal(entry.Payload, &end) == nil {
			for _, reason := range []string{end.StopReason, end.Status} {
				switch reason {
				case "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled", "completed", "error", "failed":
					session.stopReason = reason
					return
				}
			}
		}
	}
}

func (session *ConversationSession) Cancel(ctx context.Context) error {
	session.mu.Lock()
	broker := session.broker
	session.mu.Unlock()
	return broker.Cancel(ctx)
}

func (session *ConversationSession) AnswerPermission(ctx context.Context, requestID, optionID string) error {
	session.mu.Lock()
	broker := session.broker
	log := session.log
	session.mu.Unlock()
	if err := broker.AnswerPermission(ctx, requestID, optionID); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"requestId": requestID, "optionId": optionID})
	log.Append("permission_result", time.Now().UnixMilli(), payload)
	return nil
}

func (session *ConversationSession) AnswerElicitation(ctx context.Context, elicitationID, value string) error {
	session.mu.Lock()
	broker := session.broker
	log := session.log
	session.mu.Unlock()
	if log == nil {
		return errors.New("conversation unavailable")
	}
	if !broker.Answer(elicitationID, value) {
		return errors.New("elicitation already answered")
	}
	log.Append("elicitation_result", time.Now().UnixMilli(), mustRawJSON(map[string]string{
		"elicitationId": elicitationID,
		"value":         value,
	}))
	return nil
}

func (session *ConversationSession) SetConfigOption(ctx context.Context, key string, value json.RawMessage) error {
	var configValue any
	if err := json.Unmarshal(value, &configValue); err != nil {
		return errors.New("invalid config value")
	}
	switch configValue.(type) {
	case string, bool:
	default:
		return errors.New("config value must be a string or boolean")
	}
	session.mu.Lock()
	broker := session.broker
	session.mu.Unlock()
	return broker.SetConfig(ctx, key, string(value))
}

func (session *ConversationSession) SetConfigOptions(raw json.RawMessage) {
	session.mu.Lock()
	session.configOptions = raw
	session.mu.Unlock()
}

func (session *ConversationSession) SetModes(raw json.RawMessage) {
	session.mu.Lock()
	session.modes = raw
	session.mu.Unlock()
}

func (session *ConversationSession) SetAgentCapabilities(capabilities *AgentCapabilities) {
	session.mu.Lock()
	session.agentCapabilities = capabilities
	session.mu.Unlock()
}

func (session *ConversationSession) ConfigJSON() json.RawMessage {
	session.mu.Lock()
	defer session.mu.Unlock()
	raw, _ := json.Marshal(struct {
		ConfigOptions json.RawMessage `json:"configOptions,omitempty"`
		Modes         json.RawMessage `json:"modes,omitempty"`
	}{
		ConfigOptions: session.configOptions,
		Modes:         session.modes,
	})
	return raw
}

func (session *ConversationSession) AgentCapabilitiesView() *AgentCapabilities {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.agentCapabilities
}
