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
	writes            sync.RWMutex
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
	titleChange       func(string) // the agent named the chat (session_info_update)
	publishedState    string
	lastSeq           int64
	toolCalls         uint64
	permissions       uint64
	stopReason        string
	stderr            *diagnosticStderr
	agentExitCode     *atomic.Int32
	sessionID         string
	workspace         string // session working directory; set by the worker, bounds revert

	// Queue and goal state (conversation_queue.go, conversation_goal.go). queueMu orders queue
	// changes with their log entries; it is taken before mu and never inside a log callback.
	queueMu         sync.Mutex
	queue           []queuedPrompt
	running         bool // a turn loop owns the agent
	userCancelled   bool // the user stopped the running turn
	archived        bool
	now             func() time.Time
	goal            *Goal
	goalCleared     *Goal // the last goal, kept to log its "cleared" state
	goalPublished   string
	goalDirty       bool // the agent reported a native goal snapshot
	goalPublishMu   sync.Mutex
	goalChange      func(*Goal)
	nativeGoal      bool // the agent advertises a /goal command
	turnText        []byte
	turnTools       int
	goalBlocked     int
	goalIdle        int
	usageUsed       int64
	usageCost       float64
	subagents       map[string]*subagentState
	subagentsSaved  string
	subagentsChange func([]SubagentSummary)
}

func NewConversationSession(log *acp.EventLog, broker *acp.Broker) *ConversationSession {
	if log == nil {
		log = acp.NewEventLog(0)
	}
	session := &ConversationSession{log: log, broker: broker, state: "idle", initialized: make(chan struct{})}
	session.reducer.StateOnly = true
	entries := log.SetOnAppend(session.applyLogEntry)
	for _, entry := range entries {
		session.observeEntry(entry)
		session.replayEntry(entry)
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
		return string(stateNeedsYou)
	case thread.Working:
		return string(stateWorking)
	default:
		return string(stateIdle)
	}
}

func (session *ConversationSession) applyLogEntry(entry acp.LogEntry) {
	<-session.initialized
	session.mu.Lock()
	session.observeEntry(entry)
	session.applyConfigUpdate(entry)
	if entry.Kind == "agent_exit" {
		session.agentExited = true
	}
	state := conversationState(session.reducer.Apply(entry))
	changed := state != session.state
	session.state = state
	titleChange, title := session.titleChange, agentTitle(entry)
	goalDirty := session.goalDirty
	session.goalDirty = false
	subagents, subagentsChanged := session.applySubagent(entry)
	subagentsChange := session.subagentsChange
	session.mu.Unlock()
	if titleChange != nil && title != "" {
		titleChange(title)
	}
	if subagentsChanged && subagentsChange != nil {
		subagentsChange(subagents)
	}
	if goalDirty {
		// Appending from inside a log callback would deadlock; publish once this entry is done.
		obs.Go("acp.goal_publish", session.publishGoal)
	}
	if changed {
		session.publishState()
		session.maybeDrain()
	}
}

func agentTitle(entry acp.LogEntry) string {
	if entry.Kind != "session_info_update" {
		return ""
	}
	var update struct {
		Title *string `json:"title"`
	}
	if json.Unmarshal(entry.Payload, &update) != nil || update.Title == nil {
		return ""
	}
	title, err := normalizeSessionName(*update.Title)
	if err != nil {
		return ""
	}
	return title
}

// applyConfigUpdate keeps the cached options in step with the agent. The caller holds mu.
func (session *ConversationSession) applyConfigUpdate(entry acp.LogEntry) {
	switch entry.Kind {
	case "config_option_update":
		var update struct {
			ConfigOptions json.RawMessage `json:"configOptions"`
		}
		if json.Unmarshal(entry.Payload, &update) == nil && len(update.ConfigOptions) > 0 {
			session.configOptions = update.ConfigOptions
		}
	case "current_mode_update":
		var update struct {
			CurrentModeID string `json:"currentModeId"`
		}
		if json.Unmarshal(entry.Payload, &update) != nil || update.CurrentModeID == "" {
			return
		}
		var modes map[string]any
		if json.Unmarshal(session.modes, &modes) == nil && modes != nil {
			modes["currentModeId"] = update.CurrentModeID
			session.modes, _ = json.Marshal(modes)
		}
		var options []map[string]any
		if json.Unmarshal(session.configOptions, &options) == nil {
			for _, option := range options {
				if option["category"] == "mode" {
					option["currentValue"] = update.CurrentModeID
				}
			}
			session.configOptions, _ = json.Marshal(options)
		}
	}
}

// bypassModes are agent mode IDs that mean "never ask". Claude Code's bypassPermissions still
// requests permission for some commands; the user's choice of mode answers those.
var bypassModes = map[string]bool{"bypassPermissions": true}

// BypassingPermissions reports whether the agent's current mode is one where the user is never asked.
func (session *ConversationSession) BypassingPermissions() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	var options []struct {
		Category     string          `json:"category"`
		CurrentValue json.RawMessage `json:"currentValue"`
	}
	if json.Unmarshal(session.configOptions, &options) == nil {
		for _, option := range options {
			var value string
			if option.Category == "mode" && json.Unmarshal(option.CurrentValue, &value) == nil {
				return bypassModes[value]
			}
		}
	}
	var modes struct {
		CurrentModeID string `json:"currentModeId"`
	}
	return json.Unmarshal(session.modes, &modes) == nil && bypassModes[modes.CurrentModeID]
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
	session.writes.RLock()
	defer session.writes.RUnlock()
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
	session.observeGoalEntry(entry)
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

// Cancel is the user's Stop: it ends the running turn and pauses an active goal.
func (session *ConversationSession) Cancel(ctx context.Context) error {
	session.mu.Lock()
	broker := session.broker
	if session.running {
		session.userCancelled = true
	}
	session.mu.Unlock()
	return broker.Cancel(ctx)
}

func (session *ConversationSession) AnswerPermission(ctx context.Context, requestID, optionID string) error {
	session.writes.RLock()
	defer session.writes.RUnlock()
	session.mu.Lock()
	broker := session.broker
	log := session.log
	exited := session.agentExited
	session.mu.Unlock()
	if exited {
		return ErrSessionEnded
	}
	if err := broker.AnswerPermission(ctx, requestID, optionID); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"requestId": requestID, "optionId": optionID})
	log.Append("permission_result", time.Now().UnixMilli(), payload)
	return nil
}

func (session *ConversationSession) AnswerElicitation(ctx context.Context, elicitationID, value string) error {
	session.writes.RLock()
	defer session.writes.RUnlock()
	session.mu.Lock()
	broker := session.broker
	log := session.log
	exited := session.agentExited
	session.mu.Unlock()
	if exited {
		return ErrSessionEnded
	}
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
