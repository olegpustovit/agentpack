package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReduceLogWorkingDuringAgentStream(t *testing.T) {
	entries := []LogEntry{
		{Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"hi"}}`)},
	}
	state := ReduceLog(entries)
	if !state.Working {
		t.Fatal("agent stream should mark working")
	}
}

func TestReduceLogElicitationResult(t *testing.T) {
	entries := []LogEntry{
		{Kind: "elicitation", Payload: json.RawMessage(`{"elicitationId":"e1","choices":["Direct"]}`)},
		{Kind: "elicitation_result", Payload: json.RawMessage(`{"elicitationId":"e1","value":"Direct"}`)},
	}
	state := ReduceLog(entries)
	var item struct {
		ElicitationID string          `json:"elicitationId"`
		Resolved      bool            `json:"resolved"`
		Result        json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(state.Items[0].Data, &item); err != nil {
		t.Fatal(err)
	}
	if item.ElicitationID != "e1" || !item.Resolved || string(item.Result) != `{"elicitationId":"e1","value":"Direct"}` || state.NeedsYou {
		t.Fatalf("item=%+v needs_you=%v", item, state.NeedsYou)
	}
}

func TestReduceLogFixture(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "acp", "full.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []LogEntry
	for line := range splitLines(data) {
		if line == "" {
			continue
		}
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	state := ReduceLog(entries)
	if len(state.Items) < 8 {
		t.Fatalf("expected rich thread, got %d items", len(state.Items))
	}
	if state.ContextLimit != 200000 {
		t.Fatalf("usage not reduced: %+v", state)
	}
	if state.ContextTokens != 120000 || state.CostUSD != 0.42 {
		t.Fatalf("usage = %+v", state)
	}
	foundPerm := false
	for _, item := range state.Items {
		if item.Kind == "permission" {
			foundPerm = true
		}
	}
	if state.PlanKind != "Publish the branch" {
		t.Fatalf("plan_kind = %q", state.PlanKind)
	}
	if !foundPerm {
		t.Fatal("missing permission item")
	}
}
func TestReduceLogUsageUpdateCurrencyAndLegacyShape(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		used    int
		size    int
		costUSD float64
	}{
		{
			name:    "raw USD",
			payload: `{"used":8,"size":20,"cost":{"amount":0.42,"currency":"USD"}}`,
			used:    8, size: 20, costUSD: 0.42,
		},
		{
			name:    "raw non-USD",
			payload: `{"used":9,"size":30,"cost":{"amount":0.5,"currency":"EUR"}}`,
			used:    9, size: 30,
		},
		{
			name:    "legacy",
			payload: `{"contextTokens":10,"contextLimit":40,"costUsd":0.75}`,
			used:    10, size: 40, costUSD: 0.75,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := ReduceLog([]LogEntry{{Kind: "usage_update", Payload: json.RawMessage(test.payload)}})
			if state.ContextTokens != test.used || state.ContextLimit != test.size || state.CostUSD != test.costUSD {
				t.Fatalf("usage state = %+v", state)
			}
		})
	}
}

func TestThreadReducerNoticeAndCompactionSummary(t *testing.T) {
	entries := []LogEntry{
		{Kind: "notice", Payload: json.RawMessage(`{"sessionUpdate":"notice","severity":"warning","title":"MCP server unavailable","description":"Continuing without it."}`)},
		{Kind: "compaction_update", Payload: json.RawMessage(`{"sessionUpdate":"compaction_update","compactionId":"cmp_1","status":"in_progress","error":"stale"}`)},
		{Kind: "compaction_summary_chunk", Payload: json.RawMessage(`{"sessionUpdate":"compaction_summary_chunk","compactionId":"cmp_1","content":{"type":"text","text":"old "}}`)},
		{Kind: "compaction_summary_chunk", Payload: json.RawMessage(`{"sessionUpdate":"compaction_summary_chunk","compactionId":"cmp_1","content":{"type":"text","text":"summary"}}`)},
	}
	var reducer ThreadReducer
	for _, entry := range entries {
		reducer.Apply(entry)
	}
	var compaction compactionView
	if err := json.Unmarshal(reducer.state.Items[1].Data, &compaction); err != nil {
		t.Fatal(err)
	}
	if compaction.Summary != "old summary" || compaction.Error != "stale" {
		t.Fatalf("streamed compaction = %+v", compaction)
	}

	state := reducer.Apply(LogEntry{Kind: "compaction_update", Payload: json.RawMessage(`{"sessionUpdate":"compaction_update","compactionId":"cmp_1","status":"completed","summary":[{"type":"text","text":"Retained summary"}],"error":null}`)})
	var notice struct {
		Message  string `json:"message"`
		Severity string `json:"severity"`
	}
	if err := json.Unmarshal(state.Items[0].Data, &notice); err != nil {
		t.Fatal(err)
	}
	if notice.Message != "MCP server unavailable · Continuing without it." || notice.Severity != "warning" {
		t.Fatalf("notice view = %+v", notice)
	}
	if err := json.Unmarshal(state.Items[1].Data, &compaction); err != nil {
		t.Fatal(err)
	}
	if compaction.Status != "completed" || compaction.Summary != "Retained summary" || compaction.Error != "" {
		t.Fatalf("compaction patch = %+v", compaction)
	}
}
func splitLines(data []byte) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		start := 0
		for i, b := range data {
			if b != '\n' {
				continue
			}
			if !yield(string(data[start:i])) {
				return
			}
			start = i + 1
		}
		if start < len(data) {
			yield(string(data[start:]))
		}
	}
}

func TestReduceLogAgentExit(t *testing.T) {
	entries := []LogEntry{
		{Kind: "prompt", Payload: json.RawMessage(`{"text":"hi"}`)},
		{Kind: "permission_request", Payload: json.RawMessage(`{"requestId":"p1"}`)},
		{Kind: "elicitation", Payload: json.RawMessage(`{"elicitationId":"e1"}`)},
		{Kind: "agent_exit", Payload: json.RawMessage(`{}`)},
	}
	state := ReduceLog(entries)
	if state.Working || state.NeedsYou {
		t.Fatalf("agent_exit should clear working and needs_you: %+v", state)
	}
	requestCount := 0
	for _, item := range state.Items {
		if item.Kind != "permission" && item.Kind != "elicitation" {
			continue
		}
		requestCount++
		var status struct {
			Resolved bool            `json:"resolved"`
			Inactive bool            `json:"inactive"`
			Result   json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(item.Data, &status); err != nil {
			t.Fatal(err)
		}
		if status.Resolved || !status.Inactive || len(status.Result) != 0 {
			t.Fatalf("agent_exit status = %+v", status)
		}
	}
	if requestCount != 2 {
		t.Fatalf("agent_exit preserved %d request items", requestCount)
	}
}

func TestReduceLogPermissionResult(t *testing.T) {
	entries := []LogEntry{
		{Kind: "permission_request", Payload: json.RawMessage(`{"requestId":"p1","tool":"Bash"}`)},
		{Kind: "permission_request", Payload: json.RawMessage(`{"requestId":"p2","tool":"Read"}`)},
		{Kind: "permission_result", Payload: json.RawMessage(`{"requestId":"p1","optionId":"allow"}`)},
	}
	state := ReduceLog(entries)
	if !state.NeedsYou {
		t.Fatal("second permission should remain pending")
	}
	var first, second struct {
		RequestID string          `json:"requestId"`
		Resolved  bool            `json:"resolved"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(state.Items[0].Data, &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(state.Items[1].Data, &second); err != nil {
		t.Fatal(err)
	}
	if !first.Resolved || string(first.Result) != `{"requestId":"p1","optionId":"allow"}` || second.Resolved {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestReduceLogTurnEndKeepsPendingRequest(t *testing.T) {
	state := ReduceLog([]LogEntry{
		{Kind: "prompt", Payload: json.RawMessage(`{"text":"hi"}`)},
		{Kind: "elicitation", Payload: json.RawMessage(`{"elicitationId":"e1"}`)},
		{Kind: "turn_end", Payload: json.RawMessage(`{}`)},
	})
	if state.Working || !state.NeedsYou {
		t.Fatalf("turn_end state = %+v", state)
	}
	var status struct {
		Resolved bool `json:"resolved"`
		Inactive bool `json:"inactive"`
	}
	if err := json.Unmarshal(state.Items[0].Data, &status); err != nil {
		t.Fatal(err)
	}
	if status.Resolved || status.Inactive {
		t.Fatalf("turn_end changed pending request status: %+v", status)
	}
}

func TestThreadReducerAppliesUpdatesIncrementally(t *testing.T) {
	var reducer ThreadReducer
	if state := reducer.Apply(LogEntry{Kind: "prompt", Payload: json.RawMessage(`{"text":"hello"}`)}); !state.Working {
		t.Fatal("prompt did not mark the thread working")
	}
	state := reducer.Apply(LogEntry{Kind: "permission_request", Payload: json.RawMessage(`{"requestId":"p1"}`)})
	if !state.NeedsYou {
		t.Fatal("permission request did not mark the thread needs-you")
	}
	state = reducer.Apply(LogEntry{Kind: "permission_result", Payload: json.RawMessage(`{"requestId":"p1","optionId":"allow"}`)})
	if state.NeedsYou {
		t.Fatal("permission result did not clear needs-you")
	}
}
