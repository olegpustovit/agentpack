package acp

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// ThreadItem is the reduced view the worker keeps for session state.
type ThreadItem struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data,omitempty"`
}

// ThreadState tracks composer-adjacent session UI state.
type ThreadState struct {
	Items         []ThreadItem `json:"items"`
	NeedsYou      bool         `json:"needs_you"`
	Working       bool         `json:"working"`
	PlanKind      string       `json:"plan_kind,omitempty"`
	ContextTokens int          `json:"context_tokens,omitempty"`
	ContextLimit  int          `json:"context_limit,omitempty"`
	CostUSD       float64      `json:"cost_usd,omitempty"`
	// Waiting lists background work the agent will wake for after its turn ends. It never holds
	// commands, prompts or tool arguments: only a kind, a deadline and, for a reply, the asked chat.
	Waiting []WaitItem `json:"waiting,omitempty"`
}

// WaitItem is one piece of open background work. Until (unix ms) bounds it, so a lost close
// signal cannot leave a chat waiting forever; 0 means it closes only by protocol or agent exit.
type WaitItem struct {
	Kind  string `json:"kind"` // shell, monitor, agent, task, wake or reply
	Until int64  `json:"until,omitempty"`
	Peer  string `json:"peer,omitempty"`
	key   string
}

// ThreadReducer applies conversation entries and retains only outstanding requests.
// ReduceLog also keeps the transcript items needed for full replay.
type ThreadReducer struct {
	keepItems   bool
	state       ThreadState
	tools       map[toolInputKey]int
	compactions map[string]int
	afterPrompt bool
	inTurn      bool
	turnTask    string            // the async task of a turn the agent started itself
	waitTools   map[string]string // ScheduleWakeup and ask tool calls by ID, named only on their first update
}

const (
	wakeGrace  = 2 * 60 * 1000       // a scheduled wake-up may fire this late
	replyLimit = 24 * 60 * 60 * 1000 // an ask whose answer never comes stops waiting after a day
)

func (reducer *ThreadReducer) Apply(entry LogEntry) ThreadState {
	state := &reducer.state
	// Some agents (Antigravity) echo the prompt back as a user chunk; the prompt item already shows it.
	echo := reducer.afterPrompt && entry.Kind == "user_message_chunk"
	reducer.afterPrompt = entry.Kind == "prompt" || echo
	if echo {
		return reducer.state
	}
	// A subagent can run on after the parent's turn ends; only the parent's own activity is "working".
	child := bytes.Contains(entry.Payload, []byte(`"`+SubagentKey+`"`)) && payloadSubagent(entry.Payload) != ""
	switch entry.Kind {
	case "agent_message_chunk", "user_message_chunk", "agent_thought_chunk":
		if reducer.keepItems {
			state.appendChunk(entry)
		}
		if entry.Kind != "user_message_chunk" && !state.NeedsYou && !child {
			state.Working = true
		}
	case "tool_call":
		if reducer.keepItems {
			id := toolKey(entry.Payload)
			state.Items = append(state.Items, ThreadItem{Kind: "tool", Data: expandToolDiffs(entry.Payload)})
			if id.call != "" {
				if reducer.tools == nil {
					reducer.tools = map[toolInputKey]int{}
				}
				reducer.tools[id] = len(state.Items) - 1
			}
		}
		if !state.NeedsYou && !child {
			state.Working = true
		}
	case "tool_call_update":
		if !reducer.keepItems {
			break
		}
		id := toolKey(entry.Payload)
		if idx, ok := reducer.tools[id]; ok {
			state.Items[idx].Data = mergeToolUpdate(state.Items[idx].Data, entry.Payload)
		} else {
			state.Items = append(state.Items, ThreadItem{Kind: "tool", Data: expandToolDiffs(entry.Payload)})
		}
	case "plan":
		if reducer.keepItems {
			state.Items = append(state.Items, ThreadItem{Kind: "plan", Data: entry.Payload})
		}
		var plan struct {
			Entries []struct {
				Status  string `json:"status"`
				Content string `json:"content"`
			} `json:"entries"`
		}
		_ = json.Unmarshal(entry.Payload, &plan)
		var active *struct {
			Status  string `json:"status"`
			Content string `json:"content"`
		}
		for i := range plan.Entries {
			e := &plan.Entries[i]
			if e.Status == "in_progress" {
				active = e
				break
			}
		}
		if active == nil {
			for i := range plan.Entries {
				e := &plan.Entries[i]
				if e.Status == "pending" {
					active = e
					break
				}
			}
		}
		if active == nil && len(plan.Entries) > 0 {
			active = &plan.Entries[len(plan.Entries)-1]
		}
		if active != nil {
			state.PlanKind = strings.TrimSpace(active.Content)
		}
	case "permission_request":
		state.Items = append(state.Items, ThreadItem{Kind: "permission", Data: entry.Payload})
		state.NeedsYou = hasOutstandingRequests(state.Items)
	case "permission_result":
		resolveRequest(state.Items, "permission", "requestId", entry.Payload)
		state.NeedsYou = hasOutstandingRequests(state.Items)
	case "elicitation":
		state.Items = append(state.Items, ThreadItem{Kind: "elicitation", Data: entry.Payload})
		state.NeedsYou = hasOutstandingRequests(state.Items)
	case "elicitation_result":
		resolveRequest(state.Items, "elicitation", "elicitationId", entry.Payload)
		state.NeedsYou = hasOutstandingRequests(state.Items)
	case "rich_block":
		if reducer.keepItems {
			state.Items = append(state.Items, ThreadItem{Kind: "rich_block", Data: entry.Payload})
		}
	case "notice":
		if reducer.keepItems {
			state.Items = append(state.Items, ThreadItem{Kind: "divider", Data: noticeViewPayload(entry.Payload)})
		}
	case "compaction_update":
		if reducer.keepItems {
			reducer.updateCompaction(entry.Payload)
		}
	case "compaction_summary_chunk":
		if reducer.keepItems {
			reducer.appendCompactionSummary(entry.Payload)
		}
	case "usage_update":
		var usage struct {
			Used *int `json:"used"`
			Size *int `json:"size"`
			Cost *struct {
				Amount   float64 `json:"amount"`
				Currency string  `json:"currency"`
			} `json:"cost"`
			ContextTokens *int     `json:"contextTokens"`
			ContextLimit  *int     `json:"contextLimit"`
			CostUSD       *float64 `json:"costUsd"`
			Meta          struct {
				Origin json.RawMessage `json:"_claude/origin"`
			} `json:"_meta"`
		}
		_ = json.Unmarshal(entry.Payload, &usage)
		// Claude's own follow-up cycles (after a background task) have no prompt or turn_end;
		// their origin-stamped usage is the end. Inside a user turn it must not clear "working".
		if !reducer.inTurn && !child && len(usage.Meta.Origin) > 0 {
			state.Working = false
		}
		if usage.Used != nil {
			state.ContextTokens = *usage.Used
		} else if usage.ContextTokens != nil {
			state.ContextTokens = *usage.ContextTokens
		}
		if usage.Size != nil {
			state.ContextLimit = *usage.Size
		} else if usage.ContextLimit != nil {
			state.ContextLimit = *usage.ContextLimit
		}
		if usage.Cost != nil {
			state.CostUSD = 0
			if usage.Cost.Currency == "USD" {
				state.CostUSD = usage.Cost.Amount
			}
		} else if usage.CostUSD != nil {
			state.CostUSD = *usage.CostUSD
		}
	case "session_info_update":
		// Codex keeps working on a goal with no prompt or turn_end; its thread status is the only edge.
		if !reducer.inTurn && !child && bytes.Contains(entry.Payload, []byte(`"threadStatus"`)) {
			var update struct {
				Meta struct {
					Codex struct {
						ThreadStatus struct {
							Type string `json:"type"`
						} `json:"threadStatus"`
					} `json:"codex"`
				} `json:"_meta"`
			}
			if json.Unmarshal(entry.Payload, &update) == nil {
				switch update.Meta.Codex.ThreadStatus.Type {
				case "active":
					state.Working = !state.NeedsYou
				case "idle":
					state.Working = false
				}
			}
		}
	case "turn_end", "agent_exit":
		// A failed or cancelled turn leaves its permissions and questions unanswerable; only a URL
		// elicitation (an MCP sign-in) can outlive a turn, and it ends with the agent.
		state.Working = false
		reducer.inTurn = false
		failed := entry.Kind == "turn_end" && turnFailed(entry.Payload)
		for i := range state.Items {
			if isOutstandingRequest(state.Items[i]) && (entry.Kind == "agent_exit" || failed && !isURLElicitation(state.Items[i])) {
				setItemFlag(&state.Items[i], "inactive")
			}
		}
		state.NeedsYou = hasOutstandingRequests(state.Items)
	case "prompt":
		state.Working = true
		reducer.inTurn = true
		if reducer.keepItems {
			state.Items = append(state.Items, ThreadItem{Kind: "user_message_chunk", Data: entry.Payload})
		}
	}
	if !child {
		reducer.applyWaiting(entry)
	}
	if !reducer.keepItems {
		state.Items = slices.DeleteFunc(state.Items, func(item ThreadItem) bool { return !isOutstandingRequest(item) })
	}
	return reducer.state
}

// ReduceLog folds append-only log entries into a thread view.
func ReduceLog(entries []LogEntry) ThreadState {
	reducer := ThreadReducer{keepItems: true}
	for _, entry := range entries {
		reducer.Apply(entry)
	}
	return reducer.state
}

type compactionView struct {
	CompactionID string `json:"compactionId"`
	Status       string `json:"status"`
	Summary      string `json:"summary,omitempty"`
	Error        string `json:"error,omitempty"`
}

func (reducer *ThreadReducer) updateCompaction(payload json.RawMessage) {
	var update struct {
		ID      string          `json:"compactionId"`
		Status  *string         `json:"status"`
		Summary json.RawMessage `json:"summary"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(payload, &update) != nil || update.ID == "" {
		return
	}
	view := reducer.compaction(update.ID)
	if update.Status != nil {
		view.Status = *update.Status
	}
	if len(update.Error) > 0 {
		if string(update.Error) == "null" {
			view.Error = ""
		} else {
			var message string
			if json.Unmarshal(update.Error, &message) == nil {
				view.Error = message
			}
		}
	}
	if len(update.Summary) > 0 {
		if string(update.Summary) == "null" {
			view.Summary = ""
		} else if summary, ok := compactionSummary(update.Summary); ok {
			view.Summary = summary
		}
	}
	reducer.storeCompaction(view)
}

func (reducer *ThreadReducer) appendCompactionSummary(payload json.RawMessage) {
	var chunk struct {
		ID      string          `json:"compactionId"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(payload, &chunk) != nil || chunk.ID == "" {
		return
	}
	text, ok := compactionContentText(chunk.Content)
	if !ok {
		return
	}
	view := reducer.compaction(chunk.ID)
	view.Summary += text
	reducer.storeCompaction(view)
}

func (reducer *ThreadReducer) compaction(id string) compactionView {
	if reducer.compactions == nil {
		reducer.compactions = map[string]int{}
	}
	if index, ok := reducer.compactions[id]; ok {
		var view compactionView
		if json.Unmarshal(reducer.state.Items[index].Data, &view) == nil {
			return view
		}
	}
	return compactionView{CompactionID: id, Status: "in_progress"}
}

func (reducer *ThreadReducer) storeCompaction(view compactionView) {
	data, err := json.Marshal(view)
	if err != nil {
		return
	}
	if index, ok := reducer.compactions[view.CompactionID]; ok {
		reducer.state.Items[index].Data = data
		return
	}
	reducer.compactions[view.CompactionID] = len(reducer.state.Items)
	reducer.state.Items = append(reducer.state.Items, ThreadItem{Kind: "compaction", Data: data})
}

func compactionSummary(raw json.RawMessage) (string, bool) {
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", false
	}
	var summary strings.Builder
	for _, block := range blocks {
		text, ok := compactionContentText(block)
		if ok {
			summary.WriteString(text)
		}
	}
	return summary.String(), true
}

func compactionContentText(raw json.RawMessage) (string, bool) {
	var content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &content); err != nil || content.Type != "text" {
		return "", false
	}
	return content.Text, true
}

func noticeViewPayload(payload json.RawMessage) json.RawMessage {
	var notice struct {
		Title       string  `json:"title"`
		Description *string `json:"description"`
		Message     string  `json:"message"`
		Severity    string  `json:"severity"`
	}
	_ = json.Unmarshal(payload, &notice)
	message := notice.Message
	if message == "" {
		message = notice.Title
	}
	if notice.Description != nil && *notice.Description != "" {
		if message != "" {
			message += " · "
		}
		message += *notice.Description
	}
	data, err := json.Marshal(struct {
		Message  string `json:"message"`
		Severity string `json:"severity,omitempty"`
	}{Message: message, Severity: notice.Severity})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}
func resolveRequest(items []ThreadItem, kind, idField string, result json.RawMessage) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(result, &payload) != nil || payload == nil {
		return
	}
	var id string
	_ = json.Unmarshal(payload[idField], &id)
	if id == "" {
		return
	}
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind != kind || !isOutstandingRequest(items[i]) {
			continue
		}
		var request map[string]json.RawMessage
		if json.Unmarshal(items[i].Data, &request) != nil || request == nil {
			continue
		}
		var requestID string
		_ = json.Unmarshal(request[idField], &requestID)
		if requestID != id {
			continue
		}
		if string(payload["cancelled"]) == "true" {
			request["inactive"] = json.RawMessage("true")
		} else {
			request["resolved"] = json.RawMessage("true")
			request["result"] = result
		}
		if data, err := json.Marshal(request); err == nil {
			items[i].Data = data
		}
		return
	}
}

func hasOutstandingRequests(items []ThreadItem) bool {
	for _, item := range items {
		if isOutstandingRequest(item) {
			return true
		}
	}
	return false
}

func isOutstandingRequest(item ThreadItem) bool {
	if item.Kind != "permission" && item.Kind != "elicitation" {
		return false
	}
	var request struct {
		RequestID     string `json:"requestId"`
		ElicitationID string `json:"elicitationId"`
		Resolved      bool   `json:"resolved"`
		Inactive      bool   `json:"inactive"`
	}
	if json.Unmarshal(item.Data, &request) != nil || request.Resolved || request.Inactive {
		return false
	}
	if item.Kind == "permission" {
		return request.RequestID != ""
	}
	return request.ElicitationID != ""
}

func isURLElicitation(item ThreadItem) bool {
	var request struct {
		AgentElicitationID string `json:"agentElicitationId"`
	}
	return item.Kind == "elicitation" && json.Unmarshal(item.Data, &request) == nil && request.AgentElicitationID != ""
}

func setItemFlag(item *ThreadItem, flag string) {
	var data map[string]json.RawMessage
	if json.Unmarshal(item.Data, &data) != nil || data == nil {
		return
	}
	data[flag] = json.RawMessage("true")
	if encoded, err := json.Marshal(data); err == nil {
		item.Data = encoded
	}
}

func (state *ThreadState) appendChunk(entry LogEntry) {
	if len(state.Items) == 0 || state.Items[len(state.Items)-1].Kind != entry.Kind {
		state.Items = append(state.Items, ThreadItem{Kind: entry.Kind, Data: entry.Payload})
		return
	}
	last := &state.Items[len(state.Items)-1]
	merged, err := mergeTextChunks(last.Data, entry.Payload)
	if err != nil {
		state.Items = append(state.Items, ThreadItem{Kind: entry.Kind, Data: entry.Payload})
		return
	}
	last.Data = merged
}

// Tool updates carry only changed fields. A status update preserves the diff from the call.
func mergeToolUpdate(previous, update json.RawMessage) json.RawMessage {
	merged := make(map[string]json.RawMessage)
	_ = json.Unmarshal(previous, &merged)
	if merged == nil {
		return update
	}
	if err := json.Unmarshal(update, &merged); err != nil || merged == nil {
		return previous
	}
	body, err := json.Marshal(merged)
	if err != nil {
		return previous
	}
	if bytes.Contains(update, []byte(`"newTextFromInput"`)) {
		return expandToolDiffs(body)
	}
	return body
}

func expandToolDiffs(payload json.RawMessage) json.RawMessage {
	if !bytes.Contains(payload, []byte(`"newTextFromInput"`)) {
		return payload
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return payload
	}
	text, blocks := toolDiffInput(fields)
	if len(text) == 0 || len(blocks) == 0 {
		return payload
	}
	for _, block := range blocks {
		if string(block["type"]) == `"diff"` && string(block["newTextFromInput"]) == "true" {
			block["newText"] = text
			delete(block, "newTextFromInput")
		}
	}
	content, err := json.Marshal(blocks)
	if err != nil {
		return payload
	}
	fields["content"] = content
	body, err := json.Marshal(fields)
	if err != nil {
		return payload
	}
	return body
}

// Tool diff text can share the exact file content already in the tool input.
func toolDiffInput(fields map[string]json.RawMessage) (json.RawMessage, []map[string]json.RawMessage) {
	var input struct {
		Content json.RawMessage `json:"content"`
	}
	if len(fields["content"]) == 0 || json.Unmarshal(fields["rawInput"], &input) != nil ||
		len(input.Content) == 0 || input.Content[0] != '"' {
		return nil, nil
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(fields["content"], &blocks) != nil {
		return nil, nil
	}
	return input.Content, blocks
}

func toolKey(payload json.RawMessage) toolInputKey {
	var body struct {
		ToolCallID string `json:"toolCallId"`
		Subagent   string `json:"_subagent"`
	}
	_ = json.Unmarshal(payload, &body)
	return toolInputKey{call: body.ToolCallID, child: body.Subagent}
}

func turnFailed(payload json.RawMessage) bool {
	var end struct {
		StopReason string `json:"stopReason"`
	}
	return json.Unmarshal(payload, &end) == nil && (end.StopReason == "error" || end.StopReason == "cancelled")
}

// applyWaiting opens and closes background-work items. Signals: AIR async_task_* updates (Claude
// and Codex adapters, asyncTasks capability), a ScheduleWakeup tool call, and an agentpack ask
// tool call closed by the reply prompt from the asked chat.
func (reducer *ThreadReducer) applyWaiting(entry LogEntry) {
	state := &reducer.state
	switch entry.Kind {
	case "async_task_spawned":
		var task struct {
			ID       string `json:"asyncTaskId"`
			TaskType string `json:"taskType"`
		}
		if json.Unmarshal(entry.Payload, &task) != nil || task.ID == "" {
			return
		}
		if task.TaskType == "turn" {
			// The agent woke itself (a task finished, a timer fired): it is working, and due wakes are spent.
			reducer.turnTask = task.ID
			state.Working = !state.NeedsYou
			state.Waiting = slices.DeleteFunc(state.Waiting, func(w WaitItem) bool { return w.Kind == "wake" && w.Until-wakeGrace <= entry.TS+60_000 })
			return
		}
		reducer.openWait(WaitItem{Kind: taskKind(task.TaskType), key: "task:" + task.ID})
	case "async_task_state_update":
		var task struct {
			ID    string `json:"asyncTaskId"`
			State string `json:"state"`
		}
		if json.Unmarshal(entry.Payload, &task) != nil || (task.State != "completed" && task.State != "failed" && task.State != "stopped") {
			return
		}
		if task.ID == reducer.turnTask {
			reducer.turnTask = ""
			state.Working = false
			return
		}
		reducer.closeWait("task:" + task.ID)
	case "tool_call", "tool_call_update":
		var call struct {
			ID       string `json:"toolCallId"`
			Title    string `json:"title"`
			Status   string `json:"status"`
			RawInput struct {
				DelaySeconds float64 `json:"delaySeconds"`
				Stop         bool    `json:"stop"`
				SessionID    string  `json:"session_id"`
			} `json:"rawInput"`
			Meta struct {
				Claude struct {
					ToolName string `json:"toolName"`
				} `json:"claudeCode"`
			} `json:"_meta"`
		}
		if json.Unmarshal(entry.Payload, &call) != nil || call.ID == "" {
			return
		}
		// Claude names the tool only on the first tool_call; its input arrives on a later update.
		name := call.Meta.Claude.ToolName
		if name == "" {
			name = call.Title
		}
		if name == "ScheduleWakeup" || isAskTool(name) {
			if reducer.waitTools == nil {
				reducer.waitTools = map[string]string{}
			}
			reducer.waitTools[call.ID] = name
		} else if known := reducer.waitTools[call.ID]; known != "" {
			name = known
		}
		key := "tool:" + call.ID
		switch {
		case call.Status == "failed":
			reducer.closeWait(key)
		case name == "ScheduleWakeup" && call.RawInput.Stop:
			state.Waiting = slices.DeleteFunc(state.Waiting, func(w WaitItem) bool { return w.Kind == "wake" })
		case name == "ScheduleWakeup" && call.RawInput.DelaySeconds > 0:
			// Claude keeps one pending wake-up: a new schedule replaces the earlier one.
			state.Waiting = slices.DeleteFunc(state.Waiting, func(w WaitItem) bool { return w.Kind == "wake" && w.key != key })
			reducer.openWait(WaitItem{Kind: "wake", Until: entry.TS + int64(call.RawInput.DelaySeconds*1000) + wakeGrace, key: key})
		case isAskTool(name) && call.RawInput.SessionID != "":
			reducer.openWait(WaitItem{Kind: "reply", Until: entry.TS + replyLimit, Peer: call.RawInput.SessionID, key: key})
		}
	case "prompt":
		var prompt struct {
			From *PromptFrom `json:"from"`
		}
		if json.Unmarshal(entry.Payload, &prompt) == nil && prompt.From != nil && prompt.From.Kind == "reply" {
			for i, w := range state.Waiting {
				if w.Kind == "reply" && w.Peer == prompt.From.Session {
					state.Waiting = slices.Delete(state.Waiting, i, i+1)
					break
				}
			}
		}
	case "agent_exit":
		state.Waiting = nil
		reducer.turnTask = ""
	}
}

func (reducer *ThreadReducer) openWait(item WaitItem) {
	for i, w := range reducer.state.Waiting {
		if w.key == item.key {
			reducer.state.Waiting[i] = item
			return
		}
	}
	reducer.state.Waiting = append(reducer.state.Waiting, item)
}

func (reducer *ThreadReducer) closeWait(key string) {
	reducer.state.Waiting = slices.DeleteFunc(reducer.state.Waiting, func(w WaitItem) bool { return w.key == key })
}

// isAskTool matches the agentpack ask tool as Claude (mcp__agentpack__ask) and title-only adapters name it.
func isAskTool(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "agentpack") && (strings.HasSuffix(name, "__ask") || strings.HasSuffix(name, ".ask") || strings.HasSuffix(name, "/ask") || strings.HasSuffix(name, ": ask"))
}

// taskKind names an AIR task type without its description, which can carry a command.
func taskKind(taskType string) string {
	switch t := strings.ToLower(taskType); {
	case strings.Contains(t, "monitor"):
		return "monitor"
	case strings.Contains(t, "agent"):
		return "agent"
	case strings.Contains(t, "bash"), strings.Contains(t, "shell"), strings.Contains(t, "terminal"), strings.Contains(t, "exec"):
		return "shell"
	}
	return "task"
}
