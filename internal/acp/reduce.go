package acp

import (
	"encoding/json"
	"strings"
)

// ThreadItem is the reduced view shared by the gateway UI and ap hub TUI.
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
}

// ThreadReducer applies conversation entries without rescanning the full log.
type ThreadReducer struct {
	state       ThreadState
	tools       map[string]int
	compactions map[string]int
}

func (reducer *ThreadReducer) Apply(entry LogEntry) ThreadState {
	state := &reducer.state
	if reducer.tools == nil {
		reducer.tools = map[string]int{}
	}
	tools := reducer.tools
	switch entry.Kind {
	case "agent_message_chunk", "user_message_chunk", "agent_thought_chunk":
		state.appendChunk(entry)
		if entry.Kind != "user_message_chunk" && !state.NeedsYou {
			state.Working = true
		}
	case "tool_call":
		id := toolID(entry.Payload)
		item := ThreadItem{Kind: "tool", Data: entry.Payload}
		state.Items = append(state.Items, item)
		if id != "" {
			tools[id] = len(state.Items) - 1
		}
		if !state.NeedsYou {
			state.Working = true
		}
	case "tool_call_update":
		id := toolID(entry.Payload)
		if idx, ok := tools[id]; ok {
			state.Items[idx].Data = entry.Payload
		} else {
			state.Items = append(state.Items, ThreadItem{Kind: "tool", Data: entry.Payload})
		}
	case "plan":
		state.Items = append(state.Items, ThreadItem{Kind: "plan", Data: entry.Payload})
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
	case "notice":
		state.Items = append(state.Items, ThreadItem{Kind: "divider", Data: noticeViewPayload(entry.Payload)})
	case "compaction_update":
		reducer.updateCompaction(entry.Payload)
	case "compaction_summary_chunk":
		reducer.appendCompactionSummary(entry.Payload)
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
		}
		_ = json.Unmarshal(entry.Payload, &usage)
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
	case "turn_end":
		state.Working = false
	case "agent_exit":
		state.Working = false
		for i := range state.Items {
			if isOutstandingRequest(state.Items[i]) {
				setItemFlag(&state.Items[i], "inactive")
			}
		}
		state.NeedsYou = hasOutstandingRequests(state.Items)
	case "prompt":
		state.Working = true
	}
	return reducer.state
}

// ReduceLog folds append-only log entries into a thread view.
func ReduceLog(entries []LogEntry) ThreadState {
	var reducer ThreadReducer
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
		request["resolved"] = json.RawMessage("true")
		request["result"] = result
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

func toolID(payload json.RawMessage) string {
	var body struct {
		ToolCallID string `json:"toolCallId"`
	}
	_ = json.Unmarshal(payload, &body)
	return body.ToolCallID
}
