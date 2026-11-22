package network

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
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
	agentCommands     json.RawMessage
	agentVersion      string
	snapshotChanged   func()
	agentCapabilities *AgentCapabilities
	agentSessionID    string
	stateChange       func(string)
	titleChange       func(string) // the agent named the chat (session_info_update)
	permissionChange  func(string) // someone chose a permission mode; remembered for the agent
	clientMode        string       // Agentpack permission override; "" when unsupported
	publishedState    string
	afterIdle         func() // one-shot, run when the conversation next settles idle
	lastSeq           int64
	toolCalls         uint64
	permissions       uint64
	stopReason        string
	stderr            *diagnosticStderr
	agentExitCode     *atomic.Int32
	sessionID         string
	workspace         string         // session working directory; set by the worker, bounds revert
	imageStore        acp.ImageStore // the session's blob store, for images agents and tools send

	// Queue and goal state (conversation_queue.go, conversation_goal.go). queueMu orders queue
	// changes with their log entries; it is taken before mu and never inside a log callback.
	configMu               sync.Mutex
	queueMu                sync.Mutex
	queue                  []queuedPrompt
	seenPrompts            map[string]struct{}
	starting               bool
	promptStored           func(context.Context, string) (bool, error)
	running                bool // a turn loop owns the agent
	userCancelled          bool // the user stopped the running turn
	archived               bool
	now                    func() time.Time
	after                  func(time.Duration, func()) // schedules network retries; tests replace it
	networkRetries         int                         // retries since the last turn that reached the agent's service
	turnsEnded             int                         // a retry is dropped when another turn ended meanwhile
	goal                   *Goal
	goalCleared            *Goal // the last goal, kept to log its "cleared" state
	goalPublished          string
	goalDirty              bool // the agent reported a native goal snapshot
	goalSettled            bool // the native goal was marked met because the agent went idle
	goalPauseDue           bool // the user stopped unprompted native goal work; tell the agent to pause
	goalPublishMu          sync.Mutex
	goalChange             func(*Goal)
	nativeGoal             bool // the agent advertises a /goal command
	turnText               []byte
	turnTools              int
	goalBlocked            int
	goalIdle               int
	usageUsed              int64
	usageCost              float64
	subagents              map[string]*subagentState
	subagentsSaved         string
	subagentsChange        func([]SubagentSummary)
	unpromptedUsageSettle  time.Duration // quiet delay after an unprompted usage_update before closing the cycle
	inPromptTurn           bool
	lastParentKind         string
	unpromptedUsagePending bool
	settleGen              uint64
	settleTimer            *time.Timer
}

func NewConversationSession(log *acp.EventLog, broker *acp.Broker) *ConversationSession {
	if log == nil {
		log = acp.NewEventLog(0)
	}
	session := &ConversationSession{log: log, broker: broker, state: "idle", initialized: make(chan struct{}), seenPrompts: make(map[string]struct{})}
	entries := log.SetOnAppend(session.applyLogEntry)
	for _, entry := range entries {
		session.observeEntry(entry)
		session.applyConfigUpdate(entry)
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
	if changed {
		session.followNativeGoal(entry, state)
	}
	session.state = state
	var settleDelay time.Duration
	var settleGen uint64
	var settleAfter func(time.Duration, func())
	if entry.Kind == "usage_update" && entrySubagent(entry.Payload) == "" {
		settleDelay, settleGen, settleAfter = session.unpromptedSettlePlan()
	}
	titleChange, title := session.titleChange, agentTitle(entry)
	goalDirty, pause := session.goalDirty, session.goalPauseDue
	session.goalDirty, session.goalPauseDue = false, false
	subagents, subagentsChanged := session.applySubagent(entry)
	subagentsChange := session.subagentsChange
	snapshotChanged := session.snapshotChanged
	session.mu.Unlock()
	if snapshotChanged != nil && entrySubagent(entry.Payload) == "" {
		switch entry.Kind {
		case "agent_configuration", "config_option_update", "current_mode_update", "available_commands_update":
			snapshotChanged()
		}
	}
	if settleDelay > 0 {
		session.scheduleUnpromptedSettle(settleDelay, settleGen, "end_turn", settleAfter)
	}
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
	if pause {
		obs.Go("acp.goal_pause", func() { _, _, _ = session.Submit(context.Background(), acp.PromptInput{Text: "/goal pause"}, false) })
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
	// A helper's options (one model, its own mode) describe the helper, not the chat.
	if entrySubagent(entry.Payload) != "" {
		return
	}
	switch entry.Kind {
	case "available_commands_update":
		session.agentCommands = entry.Payload
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

// bypassModes are agent mode IDs that mean "never ask". Claude Code's bypassPermissions and
// Codex's agent-full-access still request permission for some calls (Codex: app MCP tools);
// the user's choice of mode answers those.
var bypassModes = map[string]bool{"bypassPermissions": true, "agent-full-access": true}

// BypassingPermissions reports whether the agent's current mode is one where the user is never asked.
func (session *ConversationSession) BypassingPermissions() bool {
	_, value := session.permissionMode()
	return bypassModes[value]
}

// applyRememberedPermissions puts a starting agent in the permission mode last chosen for it.
// An agent that does not offer that mode keeps its own.
func (session *ConversationSession) applyRememberedPermissions(ctx context.Context, store *state.Store, settingKey string) {
	var mode string
	if err := store.Reader.QueryRowContext(ctx, "SELECT value FROM mesh_settings WHERE key=?", settingKey).Scan(&mode); err != nil || mode == "" {
		return
	}
	key, current := session.permissionMode()
	if key == "" || current == mode {
		return
	}
	if err := session.setConfigOption(ctx, key, mode, mustRawJSON(mode)); err != nil {
		_, event := obs.Start(ctx, "worker", "apply_permissions")
		event.Set("mode", mode)
		event.Fail(err)
		event.End()
	}
}

// clientModes are the permission modes agentpack offers for an agent without its own.
var clientModes = []map[string]string{{"value": "default", "name": "Ask"}, {"value": "bypassPermissions", "name": "Bypass Permissions"}}

func (session *ConversationSession) setClientMode(value any) error {
	mode, _ := value.(string)
	if !slices.ContainsFunc(clientModes, func(item map[string]string) bool { return item["value"] == mode }) {
		return errors.New("unknown permission mode")
	}
	session.mu.Lock()
	session.clientMode = mode
	session.mu.Unlock()
	return nil
}

// permissionMode is the ID of the agent's permission option ("mode" for agents that only list
// modes) and its current value; both are empty when the agent has none.
func (session *ConversationSession) permissionMode() (key, value string) {
	session.mu.Lock()
	defer session.mu.Unlock()
	key, value = session.agentPermissionMode()
	if session.clientMode != "" {
		if key == "" {
			return "mode", session.clientMode
		}
		if bypassModes[session.clientMode] {
			return key, session.clientMode
		}
	}
	return key, value
}

// agentPermissionMode reads the native mode with session.mu held.
func (session *ConversationSession) agentPermissionMode() (key, value string) {
	var options []struct {
		ID           string          `json:"id"`
		Category     string          `json:"category"`
		CurrentValue json.RawMessage `json:"currentValue"`
	}
	if json.Unmarshal(session.configOptions, &options) == nil {
		for _, option := range options {
			if option.Category == "mode" && json.Unmarshal(option.CurrentValue, &value) == nil {
				return option.ID, value
			}
		}
	}
	var modes struct {
		CurrentModeID string `json:"currentModeId"`
	}
	if json.Unmarshal(session.modes, &modes) == nil && modes.CurrentModeID != "" {
		return "mode", modes.CurrentModeID
	}
	return "", ""
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
	var afterIdle func()
	if state == string(stateIdle) {
		afterIdle, session.afterIdle = session.afterIdle, nil
	}
	if afterIdle != nil {
		defer afterIdle()
	}
	if callback == nil || state == session.publishedState {
		session.mu.Unlock()
		return
	}
	session.publishedState = state
	session.mu.Unlock()
	callback(state)
}

// whenIdle registers run for the next idle settle, replacing any earlier one; false means
// the conversation is idle now and run was not registered.
func (session *ConversationSession) whenIdle(run func()) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state == string(stateIdle) {
		return false
	}
	session.afterIdle = run
	return true
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
	if entrySubagent(entry.Payload) == "" {
		switch entry.Kind {
		case "prompt":
			session.inPromptTurn = true
			session.lastParentKind = "prompt"
			session.unpromptedUsagePending = false
			session.advanceSettle()
		case "turn_end", "agent_exit":
			session.inPromptTurn = false
			session.lastParentKind = entry.Kind
			session.unpromptedUsagePending = false
			session.advanceSettle()
		case "agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update",
			"permission_request", "permission_result", "elicitation", "elicitation_result":
			session.lastParentKind = entry.Kind
			session.unpromptedUsagePending = false
			session.advanceSettle()
		case "usage_update":
			session.advanceSettle()
			session.unpromptedUsagePending = !session.inPromptTurn &&
				(session.lastParentKind == "agent_message_chunk" || session.lastParentKind == "agent_thought_chunk")
		}
	}
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
	if session.running || session.goal != nil && session.goal.Engine == "native" && session.goal.State == "active" {
		session.userCancelled = true
	}
	var settleDelay time.Duration
	var settleGen uint64
	var settleAfter func(time.Duration, func())
	if session.unpromptedUsageSettle > 0 && !session.running && !session.inPromptTurn &&
		session.state == string(stateWorking) && session.closedErr() == nil {
		session.advanceSettle()
		settleDelay, settleGen, settleAfter = session.unpromptedUsageSettle, session.settleGen, session.after
	}
	session.mu.Unlock()
	err := broker.Cancel(ctx)
	if err == nil && settleDelay > 0 {
		session.scheduleUnpromptedSettle(settleDelay, settleGen, "cancelled", settleAfter)
	}
	return err
}

// SetUnpromptedUsageSettle enables closing an unprompted cycle with a synthetic turn_end once
// a parent usage_update stays quiet for delay (longer after thought-only steps).
func (session *ConversationSession) SetUnpromptedUsageSettle(delay time.Duration) {
	session.mu.Lock()
	session.unpromptedUsageSettle = delay
	settleDelay, settleGen, settleAfter := session.unpromptedSettlePlan()
	session.mu.Unlock()
	if settleDelay > 0 {
		session.scheduleUnpromptedSettle(settleDelay, settleGen, "end_turn", settleAfter)
	}
}

// unpromptedSettlePlan returns the delay and generation to watch when an unprompted cycle
// reached a quiet usage_update. The caller holds mu.
func (session *ConversationSession) unpromptedSettlePlan() (time.Duration, uint64, func(time.Duration, func())) {
	if session.unpromptedUsageSettle <= 0 || !session.unpromptedUsagePending ||
		session.running || session.inPromptTurn || session.state != string(stateWorking) || session.closedErr() != nil {
		return 0, 0, nil
	}
	delay := session.unpromptedUsageSettle
	if session.lastParentKind != "agent_message_chunk" {
		delay = max(15*time.Second, delay*8)
	}
	return delay, session.settleGen, session.after
}

// advanceSettle invalidates any pending unprompted settle timer. The caller holds mu.
func (session *ConversationSession) advanceSettle() {
	session.settleGen++
	if session.settleTimer != nil {
		session.settleTimer.Stop()
		session.settleTimer = nil
	}
}

func (session *ConversationSession) scheduleUnpromptedSettle(delay time.Duration, gen uint64, stopReason string, after func(time.Duration, func())) {
	run := func() {
		session.writes.RLock()
		defer session.writes.RUnlock()
		session.mu.Lock()
		if session.settleGen != gen || session.running || session.inPromptTurn ||
			session.state != string(stateWorking) || session.closedErr() != nil {
			session.mu.Unlock()
			return
		}
		session.advanceSettle()
		session.unpromptedUsagePending = false
		log, now := session.log, session.clock().UnixMilli()
		session.mu.Unlock()
		log.Append("turn_end", now, mustRawJSON(map[string]string{"stopReason": stopReason}))
	}
	if after != nil {
		after(delay, run)
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.settleGen != gen || session.closedErr() != nil {
		return
	}
	session.settleTimer = time.AfterFunc(delay, run)
}

func entrySubagent(payload json.RawMessage) string {
	var origin struct {
		Subagent string `json:"_subagent"`
	}
	if json.Unmarshal(payload, &origin) == nil {
		return origin.Subagent
	}
	return ""
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
	remember := session.permissionChange
	session.mu.Unlock()
	if err := session.setConfigOption(ctx, key, configValue, value); err != nil {
		return err
	}
	if mode, ok := configValue.(string); ok && remember != nil {
		if permissions, _ := session.permissionMode(); key == permissions {
			remember(mode)
		}
	}
	return nil
}

func (session *ConversationSession) setConfigOption(ctx context.Context, key string, value any, raw json.RawMessage) error {
	session.mu.Lock()
	clientMode, broker := session.clientMode, session.broker
	var nativeKey string
	if clientMode != "" {
		nativeKey, _ = session.agentPermissionMode()
	}
	session.mu.Unlock()
	if clientMode != "" && ((nativeKey == "" && key == "mode") || (key == nativeKey && value == "bypassPermissions")) {
		return session.setClientMode(value)
	}
	if err := broker.SetConfig(ctx, key, string(raw)); err != nil {
		return err
	}
	if clientMode != "" && key == nativeKey {
		return session.setClientMode("default")
	}
	return nil
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
	return session.configJSONLocked()
}

func (session *ConversationSession) configJSONLocked() json.RawMessage {
	options := session.configOptions
	if session.clientMode != "" {
		options = session.configWithClientBypass()
	}
	raw, _ := json.Marshal(struct {
		ConfigOptions json.RawMessage `json:"configOptions,omitempty"`
		Modes         json.RawMessage `json:"modes,omitempty"`
	}{
		ConfigOptions: options,
		Modes:         session.modes,
	})
	return raw
}

// configWithClientBypass augments the permission picker with session.mu held.
func (session *ConversationSession) configWithClientBypass() json.RawMessage {
	var options []map[string]any
	_ = json.Unmarshal(session.configOptions, &options)
	bypass := clientModes[1]
	for _, option := range options {
		if option["category"] != "mode" || option["type"] != "select" {
			continue
		}
		choices, _ := option["options"].([]any)
		option["options"] = append(choices, bypass)
		if bypassModes[session.clientMode] {
			option["currentValue"] = session.clientMode
		}
		return mustRawJSON(options)
	}

	var modes struct {
		Current string           `json:"currentModeId"`
		Choices []map[string]any `json:"availableModes"`
	}
	current := session.clientMode
	var choices any = clientModes
	if json.Unmarshal(session.modes, &modes) == nil && modes.Current != "" {
		choices = append(modes.Choices, map[string]any{"value": bypass["value"], "name": bypass["name"]})
		if !bypassModes[current] {
			current = modes.Current
		}
	}
	options = append(options, map[string]any{
		"id": "mode", "name": "Permissions", "category": "mode", "type": "select",
		"currentValue": current, "options": choices,
	})
	return mustRawJSON(options)
}

func (session *ConversationSession) AgentCapabilitiesView() *AgentCapabilities {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.agentCapabilities
}

// storeImage keeps an image an agent or tool sent in the session's blob store.
func (session *ConversationSession) storeImage(data []byte) (string, error) {
	session.mu.Lock()
	store := session.imageStore
	session.mu.Unlock()
	if store == nil {
		return "", errors.New("image storage unavailable")
	}
	return store(data)
}
