package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

// ConversationSession is the worker-side ACP conversation view. It is three parts:
//   - the core here: the log and its fold, the agent process, its configuration and permissions;
//   - turnRunner (conversation_queue.go): queue, goal, retry, steering and delivery, acting on the
//     state the fold derived once the sink stored it;
//   - conversationStore (conversation_store.go): the persistence adapter. The log's sink stores
//     events with the fold; the adapter stores the rest on one writer goroutine.
//
// Lock order: writes (read side), promptMu (TryLock), the turn runner's queueMu, then mu. mu is a
// leaf: never held while calling the log (its callback takes mu), the broker or the store.
type ConversationSession struct {
	mu                sync.Mutex
	promptMu          sync.Mutex   // one prompt or revert at a time
	writes            sync.RWMutex // write side held while the agent is swapped out
	configMu          sync.Mutex   // orders config requests with wiring a starting agent
	Log               *acp.EventLog
	broker            *acp.Broker
	fold              *conversationFold // the log's fold; read only while the log notifies or at start
	state             string
	agentExited       bool
	initialized       chan struct{}
	configOptions     json.RawMessage
	modes             json.RawMessage
	agentCommands     json.RawMessage
	agentVersion      string
	agentCapabilities *session.AgentCapabilities
	agentSessionID    string
	clientMode        string // Agentpack permission override; "" when unsupported
	afterIdle         func() // one-shot, run when nothing would be cut off (readyToReload)
	waiting           int    // background work items the idle agent will wake for
	lastSeq           int64
	toolCalls         uint64
	permissions       uint64
	stopReason        string
	stderr            *diagnosticStderr
	agentExitCode     *atomic.Int32
	sessionID         string
	workspace         string // session working directory; set by the worker, bounds revert

	turnRunner
	store conversationStore
}

func NewConversationSession(log *acp.EventLog, broker *acp.Broker) *ConversationSession {
	if log == nil {
		log = acp.NewEventLog(0)
	}
	fold, _ := log.Fold().(*conversationFold)
	if fold == nil {
		fold = &conversationFold{}
		log.SetFold(fold, 0)
	}
	session := &ConversationSession{Log: log, broker: broker, fold: fold, initialized: make(chan struct{}), turnRunner: turnRunner{SeenPrompts: make(map[string]struct{})}}
	// A checkpoint carries usage from before the replay tail; entries in the tail update it.
	thread := fold.Thread.State()
	session.usageUsed, session.usageCost = int64(thread.ContextTokens), thread.CostUSD
	entries := log.SetOnAppend(session.applyLogEntry)
	for _, entry := range entries {
		session.observeEntry(entry)
		session.ReplayEntry(entry)
		if entry.Kind == "agent_exit" {
			session.agentExited = true
		}
	}
	// Replayed history already acted on its goal checks.
	session.goalPauseDue, session.goalNoticeDue = false, ""
	thread = fold.Thread.State()
	session.state, session.waiting = conversationState(thread), len(thread.Waiting)
	close(session.initialized)
	return session
}

func (cs *ConversationSession) Attach(since int64) (entries []acp.LogEntry, truncated, gap bool) {
	return cs.Log.ReadSince(since)
}

func conversationState(thread acp.ThreadState) string {
	switch {
	case thread.NeedsYou:
		return string(StateNeedsYou)
	case thread.Working:
		return string(StateWorking)
	default:
		return string(StateIdle)
	}
}

func (cs *ConversationSession) applyLogEntry(entry acp.LogEntry) {
	<-cs.initialized
	cs.mu.Lock()
	parent := cs.observeEntry(entry)
	if entry.Kind == "agent_exit" {
		cs.agentExited = true
	}
	// The log applied this entry to the fold before notifying, and holds later appends until this returns.
	thread := cs.fold.Thread.State()
	state := conversationState(thread)
	changed := state != cs.state
	cs.waiting = len(thread.Waiting)
	if changed {
		cs.followNativeGoal(entry, state)
	}
	cs.state = state
	var settleDelay time.Duration
	var settleGen uint64
	var settleAfter func(time.Duration, func())
	// Claude's adapter reports a model cycle's result as a usage_update with its cost: inside a
	// long turn (a native /goal, a Stop-hook continuation) that is when the next prompt goes in.
	cycleEnd := false
	if entry.Kind == "usage_update" && parent {
		settleDelay, settleGen, settleAfter = cs.unpromptedSettlePlan()
		cycleEnd = cs.queuesPrompts && cs.delivering > 0 && bytes.Contains(entry.Payload, []byte(`"cost"`))
	}
	goalDirty, pause, notice := cs.goalDirty, cs.goalPauseDue, cs.goalNoticeDue
	cs.goalDirty, cs.goalPauseDue, cs.goalNoticeDue = false, false, ""
	var afterIdle func()
	if cs.afterIdle != nil && cs.idleLocked() {
		afterIdle, cs.afterIdle = cs.afterIdle, nil
	}
	if parent {
		switch entry.Kind {
		case "config_option_update", "current_mode_update":
			if cs.clientMode != "" {
				cs.later(cs.logConfiguration)
				break
			}
			fallthrough
		case "agent_configuration", "available_commands_update":
			cs.later(cs.saveSnapshot)
		}
	}
	if changed || afterIdle != nil {
		// The sink stores the new state with the events behind it before anything acts on it; a
		// failed write stays pending in the log, which retries it. On the writer, so a slow store
		// never holds up this append.
		cs.later(func() {
			_ = cs.Log.Flush()
			if afterIdle != nil {
				afterIdle()
			}
			cs.maybeDrain()
		})
	}
	cs.mu.Unlock()
	if settleDelay > 0 {
		cs.scheduleUnpromptedSettle(settleDelay, settleGen, "end_turn", settleAfter)
	}
	if goalDirty {
		// Appending from inside a log callback would deadlock; publish once this entry is done.
		obs.Go("acp.goal_publish", cs.publishGoal)
	}
	if pause {
		obs.Go("acp.goal_pause", func() {
			if notice != "" {
				cs.goalNotice(notice)
			}
			cs.pauseNativeGoal(context.Background())
		})
	}
	if cycleEnd {
		obs.Go("acp.deliver", func() { cs.deliverDuringTurn(true) })
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
	title, err := NormalizeSessionName(*update.Title)
	if err != nil {
		return ""
	}
	return title
}

// applyConfigUpdate keeps the cached options in step with the agent. The caller holds mu.
func (cs *ConversationSession) applyConfigUpdate(entry acp.LogEntry) {
	switch entry.Kind {
	case "available_commands_update":
		cs.agentCommands = entry.Payload
	case "config_option_update":
		var update struct {
			ConfigOptions json.RawMessage `json:"configOptions"`
		}
		if json.Unmarshal(entry.Payload, &update) == nil && len(update.ConfigOptions) > 0 {
			cs.configOptions = update.ConfigOptions
		}
	case "current_mode_update":
		var update struct {
			CurrentModeID string `json:"currentModeId"`
		}
		if json.Unmarshal(entry.Payload, &update) != nil || update.CurrentModeID == "" {
			return
		}
		var modes map[string]any
		if json.Unmarshal(cs.modes, &modes) == nil && modes != nil {
			modes["currentModeId"] = update.CurrentModeID
			cs.modes, _ = json.Marshal(modes)
		}
		var options []map[string]any
		if json.Unmarshal(cs.configOptions, &options) == nil {
			for _, option := range options {
				if option["category"] == "mode" {
					option["currentValue"] = update.CurrentModeID
				}
			}
			cs.configOptions, _ = json.Marshal(options)
		}
	}
}

// bypassModes are agent mode IDs that mean "never ask". Claude Code's bypassPermissions and
// Codex's agent-full-access still request permission for some calls (Codex: app MCP tools);
// the user's choice of mode answers those.
var bypassModes = map[string]bool{"bypassPermissions": true, "agent-full-access": true}

// BypassingPermissions reports whether the agent's current mode is one where the user is never asked.
func (cs *ConversationSession) BypassingPermissions() bool {
	_, value := cs.permissionMode()
	return bypassModes[value]
}

// applyRememberedPermissions puts a starting agent in the permission mode last chosen for it.
// An agent that does not offer that mode keeps its own.
func (cs *ConversationSession) applyRememberedPermissions(ctx context.Context, store *state.Store, harness string) {
	mode, err := store.Mesh().Setting(ctx, permissionSettingKey(harness))
	if err != nil || mode == "" {
		return
	}
	cs.applyPermissionMode(ctx, mode)
}

// defaultBypassMode prefers an advertised native bypass mode to client-side approvals.
func (cs *ConversationSession) defaultBypassMode() (key, mode string) {
	key, current := cs.permissionMode()
	if bypassModes[current] {
		return key, current
	}
	for _, mode := range []string{"agent-full-access", "bypassPermissions"} {
		if allowed, _ := cs.wantedChoice(key, MustRawJSON(mode)); allowed {
			return key, mode
		}
	}
	cs.mu.Lock()
	cs.clientMode = "default"
	cs.mu.Unlock()
	return CmpOr(key, "mode"), "bypassPermissions"
}

func (cs *ConversationSession) applyPermissionMode(ctx context.Context, mode string) {
	key, current := cs.permissionMode()
	if key == "" || current == mode {
		return
	}
	if err := cs.setConfigOption(ctx, key, mode, MustRawJSON(mode)); err != nil {
		_, event := obs.Start(ctx, "worker", "apply_permissions")
		event.Set("mode", mode)
		event.Fail(err)
		event.End()
	}
}

// clientModes are the permission modes agentpack offers for an agent without its own.
var clientModes = []map[string]string{{"value": "default", "name": "Ask"}, {"value": "bypassPermissions", "name": "Bypass Permissions"}}

func (cs *ConversationSession) setClientMode(value any) error {
	mode, _ := value.(string)
	if !slices.ContainsFunc(clientModes, func(item map[string]string) bool { return item["value"] == mode }) {
		return errors.New("unknown permission mode")
	}
	cs.mu.Lock()
	cs.clientMode = mode
	cs.mu.Unlock()
	return nil
}

// permissionMode is the ID of the agent's permission option ("mode" for agents that only list
// modes) and its current value; both are empty when the agent has none.
func (cs *ConversationSession) permissionMode() (key, value string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	key, value = cs.agentPermissionMode()
	if cs.clientMode != "" {
		if key == "" {
			return "mode", cs.clientMode
		}
		if bypassModes[cs.clientMode] {
			return key, cs.clientMode
		}
	}
	return key, value
}

// agentPermissionMode reads the native mode with session.mu held.
func (cs *ConversationSession) agentPermissionMode() (key, value string) {
	var options []struct {
		ID           string          `json:"id"`
		Category     string          `json:"category"`
		CurrentValue json.RawMessage `json:"currentValue"`
	}
	if json.Unmarshal(cs.configOptions, &options) == nil {
		for _, option := range options {
			if option.Category == "mode" && json.Unmarshal(option.CurrentValue, &value) == nil {
				return option.ID, value
			}
		}
	}
	var modes struct {
		CurrentModeID string `json:"currentModeId"`
	}
	if json.Unmarshal(cs.modes, &modes) == nil && modes.CurrentModeID != "" {
		return "mode", modes.CurrentModeID
	}
	return "", ""
}

func (cs *ConversationSession) ConversationState() string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.state
}

func (cs *ConversationSession) AgentExited() bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.agentExited
}

// idleLocked reports whether swapping the agent would cut nothing off: the chat is idle, nothing is
// queued, no goal wants a turn and no background work will wake the agent. The caller holds mu.
func (cs *ConversationSession) idleLocked() bool {
	return cs.state == string(StateIdle) && len(cs.Queue) == 0 && !cs.goalWantsTurn() && cs.waiting == 0
}

// readyToReload is idleLocked for a caller that holds queueMu, the lock that admits prompts, so
// none starts while the agent is swapped. Otherwise run is registered for the moment it is ready,
// replacing any earlier one.
func (cs *ConversationSession) readyToReload(run func()) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.idleLocked() {
		return true
	}
	if run != nil {
		cs.afterIdle = run
	}
	return false
}

func (cs *ConversationSession) Prompt(ctx context.Context, input acp.PromptInput) (result error) {
	cs.writes.RLock()
	defer cs.writes.RUnlock()
	if !cs.promptMu.TryLock() {
		return errors.New("ACP session already has an active prompt")
	}
	defer cs.promptMu.Unlock()
	_, event := obs.Start(ctx, "acp.turn", "prompt")
	cs.mu.Lock()
	broker, exited := cs.broker, cs.agentExited
	event.Set("session_id", cs.sessionID)
	first, tools, permissions := cs.lastSeq+1, cs.toolCalls, cs.permissions
	cs.stopReason = ""
	cs.mu.Unlock()
	defer func() {
		cs.mu.Lock()
		if cs.lastSeq < first {
			first = cs.lastSeq
		}
		if cs.stopReason == "" && result != nil {
			cs.stopReason = "error"
			if errors.Is(result, context.Canceled) {
				cs.stopReason = "cancelled"
			}
		}
		event.Set("first_seq", first)
		event.Set("last_seq", cs.lastSeq)
		event.Set("tool_calls", cs.toolCalls-tools)
		event.Set("permission_requests", cs.permissions-permissions)
		event.Set("stop_reason", cs.stopReason)
		cs.mu.Unlock()
		event.Fail(result)
		event.End()
	}()
	if exited {
		return errors.New("ACP adapter has exited")
	}
	cs.beginDeliver()
	return cs.deliver(ctx, broker, input)
}

func (cs *ConversationSession) beginDeliver() {
	cs.mu.Lock()
	cs.delivering++
	cs.mu.Unlock()
}

func (cs *ConversationSession) endDeliver() {
	cs.mu.Lock()
	cs.delivering--
	if cs.delivering == 0 && cs.delivered != nil {
		close(cs.delivered)
		cs.delivered = nil
	}
	cs.mu.Unlock()
}

// deliver sends input to the agent, counted by the caller in delivering until the agent answers.
func (cs *ConversationSession) deliver(ctx context.Context, broker *acp.Broker, input acp.PromptInput) error {
	err := broker.Prompt(ctx, input)
	cs.endDeliver()
	return err
}

// awaitDelivered waits until the agent answered every prompt sent to it.
func (cs *ConversationSession) awaitDelivered() {
	cs.mu.Lock()
	if cs.delivering == 0 {
		cs.mu.Unlock()
		return
	}
	if cs.delivered == nil {
		cs.delivered = make(chan struct{})
	}
	done := cs.delivered
	cs.mu.Unlock()
	<-done
}

// observeEntry counts events as they arrive, even when the replay tail evicts them, and reports
// whether the entry is the chat's own. A subagent's entries (its options, usage, replies and turn
// ends) never drive the chat's goal, usage, retry or stop reason.
// The caller holds mu, or is initializing the session before publication.
func (cs *ConversationSession) observeEntry(entry acp.LogEntry) bool {
	cs.lastSeq = entry.Seq
	switch entry.Kind {
	case "tool_call":
		cs.toolCalls++
	case "permission_request":
		cs.permissions++
	}
	if entrySubagent(entry.Payload) != "" {
		return false
	}
	if entry.SteeredPrompt() {
		return true
	}
	cs.observeGoalEntry(entry)
	cs.applyConfigUpdate(entry)
	switch entry.Kind {
	case "permission_request", "elicitation":
		cs.requestOwnTurn = !cs.inPromptTurn
	case "prompt", "turn_end", "agent_exit":
		cs.requestOwnTurn = false
	}
	switch entry.Kind {
	case "prompt":
		cs.inPromptTurn = true
		cs.lastParentKind = "prompt"
		cs.unpromptedUsagePending = false
		cs.advanceSettle()
	case "turn_end", "agent_exit":
		cs.inPromptTurn = false
		cs.lastParentKind = entry.Kind
		cs.unpromptedUsagePending = false
		cs.advanceSettle()
	case "agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update",
		"permission_request", "permission_result", "elicitation", "elicitation_result":
		cs.lastParentKind = entry.Kind
		cs.unpromptedUsagePending = false
		cs.advanceSettle()
	case "usage_update":
		cs.advanceSettle()
		cs.unpromptedUsagePending = !cs.inPromptTurn &&
			(cs.lastParentKind == "agent_message_chunk" || cs.lastParentKind == "agent_thought_chunk")
	}
	if entry.Kind == "turn_end" {
		var end struct {
			StopReason string `json:"stopReason"`
			Status     string `json:"status"`
		}
		if json.Unmarshal(entry.Payload, &end) == nil {
			for _, reason := range []string{end.StopReason, end.Status} {
				switch reason {
				case "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled", "completed", "error", "failed":
					cs.stopReason = reason
					return true
				}
			}
		}
	}
	return true
}

// Cancel is the user's Stop: it ends the running turn and pauses an active goal.
func (cs *ConversationSession) Cancel(ctx context.Context) error {
	cs.mu.Lock()
	broker := cs.broker
	activeNativeGoal := cs.goal != nil && cs.goal.Engine == "native" && cs.goal.State == "active"
	var pause func(context.Context) error
	if activeNativeGoal {
		pause = cs.nativeGoalPauseRPC
	}
	if cs.running || activeNativeGoal {
		cs.userCancelled = true
	}
	var settleDelay time.Duration
	var settleGen uint64
	var settleAfter func(time.Duration, func())
	if cs.unpromptedUsageSettle > 0 && !cs.running && !cs.inPromptTurn &&
		cs.state == string(StateWorking) && cs.closedErr() == nil {
		cs.advanceSettle()
		settleDelay, settleGen, settleAfter = cs.unpromptedUsageSettle, cs.settleGen, cs.after
	}
	cs.mu.Unlock()
	var pauseErr error
	if pause != nil {
		// Pause future cycles first, so cancellation cannot immediately start another one.
		pauseErr = cs.requestNativeGoalPause(ctx, pause)
		if pauseErr == nil {
			cs.goalNotice(goalInterruptedNotice)
		}
	}
	err := broker.Cancel(ctx)
	if err == nil && settleDelay > 0 {
		cs.scheduleUnpromptedSettle(settleDelay, settleGen, "cancelled", settleAfter)
	}
	if pauseErr != nil {
		return errors.Join(pauseErr, err)
	}
	return err
}

// SetUnpromptedUsageSettle enables closing an unprompted cycle with a synthetic turn_end once
// a parent usage_update stays quiet for delay (longer after thought-only steps).
func (cs *ConversationSession) SetUnpromptedUsageSettle(delay time.Duration) {
	cs.mu.Lock()
	cs.unpromptedUsageSettle = delay
	settleDelay, settleGen, settleAfter := cs.unpromptedSettlePlan()
	cs.mu.Unlock()
	if settleDelay > 0 {
		cs.scheduleUnpromptedSettle(settleDelay, settleGen, "end_turn", settleAfter)
	}
}

// unpromptedSettlePlan returns the delay and generation to watch when an unprompted cycle
// reached a quiet usage_update. The caller holds mu.
func (cs *ConversationSession) unpromptedSettlePlan() (time.Duration, uint64, func(time.Duration, func())) {
	if cs.unpromptedUsageSettle <= 0 || !cs.unpromptedUsagePending ||
		cs.running || cs.inPromptTurn || cs.state != string(StateWorking) || cs.closedErr() != nil {
		return 0, 0, nil
	}
	delay := cs.unpromptedUsageSettle
	if cs.lastParentKind != "agent_message_chunk" {
		delay = max(15*time.Second, delay*8)
	}
	return delay, cs.settleGen, cs.after
}

// advanceSettle invalidates any pending unprompted settle timer. The caller holds mu.
func (cs *ConversationSession) advanceSettle() {
	cs.settleGen++
	if cs.settleTimer != nil {
		cs.settleTimer.Stop()
		cs.settleTimer = nil
	}
}

func (cs *ConversationSession) scheduleUnpromptedSettle(delay time.Duration, gen uint64, stopReason string, after func(time.Duration, func())) {
	run := func() {
		cs.writes.RLock()
		defer cs.writes.RUnlock()
		cs.mu.Lock()
		if cs.settleGen != gen || cs.running || cs.inPromptTurn ||
			cs.state != string(StateWorking) || cs.closedErr() != nil {
			cs.mu.Unlock()
			return
		}
		cs.advanceSettle()
		cs.unpromptedUsagePending = false
		log, now := cs.Log, cs.clock().UnixMilli()
		cs.mu.Unlock()
		log.Append("turn_end", now, MustRawJSON(map[string]string{"stopReason": stopReason}))
	}
	if after != nil {
		after(delay, run)
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.settleGen != gen || cs.closedErr() != nil {
		return
	}
	cs.settleTimer = time.AfterFunc(delay, run)
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

func (cs *ConversationSession) AnswerPermission(ctx context.Context, requestID, optionID string) error {
	cs.writes.RLock()
	defer cs.writes.RUnlock()
	cs.mu.Lock()
	broker := cs.broker
	log := cs.Log
	exited := cs.agentExited
	cs.mu.Unlock()
	if exited {
		return ErrSessionEnded
	}
	return broker.ResolvePermission(ctx, log, requestID, optionID)
}

func (cs *ConversationSession) AnswerElicitation(ctx context.Context, elicitationID, value string) error {
	cs.writes.RLock()
	defer cs.writes.RUnlock()
	cs.mu.Lock()
	broker := cs.broker
	log := cs.Log
	exited := cs.agentExited
	cs.mu.Unlock()
	if exited {
		return ErrSessionEnded
	}
	if log == nil {
		return errors.New("conversation unavailable")
	}
	if !broker.Answer(elicitationID, value) {
		return errors.New("elicitation already answered")
	}
	log.Append("elicitation_result", time.Now().UnixMilli(), MustRawJSON(map[string]string{
		"elicitationId": elicitationID,
		"value":         value,
	}))
	return nil
}

func (cs *ConversationSession) SetConfigOption(ctx context.Context, key string, value json.RawMessage) error {
	var configValue any
	if err := json.Unmarshal(value, &configValue); err != nil {
		return errors.New("invalid config value")
	}
	switch configValue.(type) {
	case string, bool:
	default:
		return errors.New("config value must be a string or boolean")
	}
	if err := cs.setConfigOption(ctx, key, configValue, value); err != nil {
		return err
	}
	if mode, ok := configValue.(string); ok {
		if permissions, _ := cs.permissionMode(); key == permissions {
			cs.rememberPermissions(mode)
			if bypassModes[mode] {
				cs.mu.Lock()
				broker, log := cs.broker, cs.Log
				cs.mu.Unlock()
				broker.ApprovePendingPermissions(ctx, log)
			}
		}
	}
	return nil
}

func (cs *ConversationSession) setConfigOption(ctx context.Context, key string, value any, raw json.RawMessage) error {
	cs.mu.Lock()
	clientMode, broker := cs.clientMode, cs.broker
	var nativeKey string
	if clientMode != "" {
		nativeKey, _ = cs.agentPermissionMode()
	}
	cs.mu.Unlock()
	if clientMode != "" && ((nativeKey == "" && key == "mode") || (key == nativeKey && value == "bypassPermissions")) {
		return cs.setClientMode(value)
	}
	if err := broker.SetConfig(ctx, key, string(raw)); err != nil {
		return err
	}
	if clientMode != "" && key == nativeKey {
		return cs.setClientMode("default")
	}
	return nil
}

func (cs *ConversationSession) SetConfigOptions(raw json.RawMessage) {
	cs.mu.Lock()
	cs.configOptions = raw
	cs.mu.Unlock()
}

func (cs *ConversationSession) SetModes(raw json.RawMessage) {
	cs.mu.Lock()
	cs.modes = raw
	cs.mu.Unlock()
}

func (cs *ConversationSession) SetAgentCapabilities(capabilities *session.AgentCapabilities) {
	cs.mu.Lock()
	cs.agentCapabilities = capabilities
	cs.mu.Unlock()
}

func (cs *ConversationSession) ConfigJSON() json.RawMessage {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.configJSONLocked()
}

func (cs *ConversationSession) configJSONLocked() json.RawMessage {
	options := cs.configOptions
	if cs.clientMode != "" {
		options = cs.configWithClientBypass()
	}
	raw, _ := json.Marshal(struct {
		ConfigOptions json.RawMessage `json:"configOptions,omitempty"`
		Modes         json.RawMessage `json:"modes,omitempty"`
	}{
		ConfigOptions: options,
		Modes:         cs.modes,
	})
	return raw
}

// configWithClientBypass augments the permission picker with session.mu held.
func (cs *ConversationSession) configWithClientBypass() json.RawMessage {
	var options []map[string]any
	_ = json.Unmarshal(cs.configOptions, &options)
	bypass := clientModes[1]
	for _, option := range options {
		if option["category"] != "mode" || option["type"] != "select" {
			continue
		}
		choices, _ := option["options"].([]any)
		option["options"] = append(choices, bypass)
		if bypassModes[cs.clientMode] {
			option["currentValue"] = cs.clientMode
		}
		return MustRawJSON(options)
	}

	var modes struct {
		Current string           `json:"currentModeId"`
		Choices []map[string]any `json:"availableModes"`
	}
	current := cs.clientMode
	var choices any = clientModes
	if json.Unmarshal(cs.modes, &modes) == nil && modes.Current != "" {
		choices = append(modes.Choices, map[string]any{"value": bypass["value"], "name": bypass["name"]})
		if !bypassModes[current] {
			current = modes.Current
		}
	}
	options = append(options, map[string]any{
		"id": "mode", "name": "Permissions", "category": "mode", "type": "select",
		"currentValue": current, "options": choices,
	})
	return MustRawJSON(options)
}

func (cs *ConversationSession) AgentCapabilitiesView() *session.AgentCapabilities {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.agentCapabilities
}

// ChatState is a chat's conversation_state as tools and the UI see it.
type ChatState string

const (
	StateStarting ChatState = "starting"
	StateWorking  ChatState = "working"
	StateNeedsYou ChatState = "needs_you"
	StateIdle     ChatState = "idle"
	StateStopped  ChatState = "stopped"
)

func NormalizeSessionName(name string) (string, error) {
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
