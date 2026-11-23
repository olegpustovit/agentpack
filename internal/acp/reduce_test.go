package acp

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	reducer := ThreadReducer{keepItems: true}
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
	compaction = compactionView{} // omitempty drops fields, so decode into a fresh value
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

func TestReduceLogCancelledTurnReleasesQuestion(t *testing.T) {
	// The stuck chat: an AskUserQuestion form, then the user interrupted the turn.
	entries := []LogEntry{
		{Kind: "prompt", Payload: json.RawMessage(`{"text":"hi"}`)},
		{Kind: "elicitation", Payload: json.RawMessage(`{"elicitationId":"e1","prompt":"Please answer the following questions."}`)},
		{Kind: "elicitation", Payload: json.RawMessage(`{"elicitationId":"e2","agentElicitationId":"oauth"}`)},
		{Kind: "turn_end", Payload: json.RawMessage(`{"stopReason":"cancelled"}`)},
	}
	state := ReduceLog(entries)
	if !strings.Contains(string(state.Items[1].Data), `"inactive":true`) || strings.Contains(string(state.Items[2].Data), "inactive") {
		t.Fatalf("form must end with the turn, a sign-in URL must outlive it: %+v", state.Items)
	}
	state = ReduceLog(append(entries, LogEntry{Kind: "elicitation_result", Payload: json.RawMessage(`{"elicitationId":"e2","cancelled":true}`)}))
	if state.NeedsYou {
		t.Fatalf("a withdrawn request still needs the user: %+v", state.Items)
	}
}

func TestThreadReducerAppliesUpdatesIncrementally(t *testing.T) {
	reducer := ThreadReducer{keepItems: true}
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

func TestPromptShowsOnceEvenWhenTheAgentEchoesIt(t *testing.T) {
	state := ReduceLog([]LogEntry{
		{Seq: 1, Kind: "prompt", Payload: json.RawMessage(`{"text":{"text":"hi"}}`)},
		{Seq: 2, Kind: "user_message_chunk", Payload: json.RawMessage(`{"text":{"text":"hi"}}`)},
		{Seq: 3, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"ok"}}`)},
	})
	if len(state.Items) != 2 || state.Items[0].Kind != "user_message_chunk" || string(state.Items[0].Data) != `{"text":{"text":"hi"}}` {
		t.Fatalf("items = %+v", state.Items)
	}
}

func TestReduceLogFailedTurnRetiresItsPermission(t *testing.T) {
	state := ReduceLog([]LogEntry{
		{Seq: 1, Kind: "permission_request", Payload: json.RawMessage(`{"requestId":"p1","options":[{"id":"allow","label":"Yes"}]}`)},
		{Seq: 2, Kind: "turn_end", Payload: json.RawMessage(`{"stopReason":"error"}`)},
	})
	if state.NeedsYou || isOutstandingRequest(state.Items[0]) {
		t.Fatalf("permission outlived its turn: %+v", state)
	}
}

func TestThreadReducerMatchesFullStateWithBoundedItems(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "acp", "full.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	full := ThreadReducer{keepItems: true}
	var lean ThreadReducer
	for line := range splitLines(data) {
		if line == "" {
			continue
		}
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		want, got := full.Apply(entry), lean.Apply(entry)
		if got.Working != want.Working || got.NeedsYou != want.NeedsYou {
			t.Fatalf("after %s: working %v/%v needs you %v/%v", entry.Kind, got.Working, want.Working, got.NeedsYou, want.NeedsYou)
		}
		for _, item := range got.Items {
			if !isOutstandingRequest(item) {
				t.Fatalf("state-only reducer kept %s", item.Kind)
			}
		}
	}
}

func TestThreadReducerAutonomousCycleEnds(t *testing.T) {
	origin := json.RawMessage(`{"_meta":{"_claude/origin":{"kind":"task-notification"}},"sessionUpdate":"usage_update","used":10}`)
	chunk := json.RawMessage(`{"content":{"type":"text","text":"summary"}}`)
	var reducer ThreadReducer
	for _, entry := range []LogEntry{{Kind: "prompt", Payload: json.RawMessage(`{}`)}, {Kind: "turn_end", Payload: json.RawMessage(`{}`)}, {Kind: "agent_message_chunk", Payload: chunk}} {
		reducer.Apply(entry)
	}
	if !reducer.Apply(LogEntry{Kind: "agent_message_chunk", Payload: chunk}).Working {
		t.Fatal("a follow-up cycle should show as working")
	}
	if reducer.Apply(LogEntry{Kind: "usage_update", Payload: origin}).Working {
		t.Fatal("an origin-stamped usage after turn_end should end the follow-up cycle")
	}
	reducer.Apply(LogEntry{Kind: "prompt", Payload: json.RawMessage(`{}`)})
	if !reducer.Apply(LogEntry{Kind: "usage_update", Payload: origin}).Working {
		t.Fatal("an origin-stamped usage inside a user turn must not clear working")
	}
	thread := func(status string) LogEntry {
		return LogEntry{Kind: "session_info_update", Payload: json.RawMessage(`{"_meta":{"codex":{"threadStatus":{"type":"` + status + `"}}}}`)}
	}
	if !reducer.Apply(thread("idle")).Working {
		t.Fatal("Codex's idle thread inside a user turn must not clear working")
	}
	reducer.Apply(LogEntry{Kind: "turn_end", Payload: json.RawMessage(`{}`)})
	if !reducer.Apply(thread("active")).Working || reducer.Apply(thread("idle")).Working {
		t.Fatal("Codex's thread status should bound its unprompted goal work")
	}
}

// Recorded chats with the AIR asyncTasks capability. Claude (claude-agent-acp 0.100): a background
// Bash, a Monitor and a ScheduleWakeup, the turn's end, self-started turns as the tasks finish, and
// a final ScheduleWakeup {stop:true}. Codex (codex-acp 2.8): one background terminal.
func TestThreadReducerWaitingFromRecordedLogs(t *testing.T) {
	for file, want := range map[string]string{"background-claude.jsonl": "shell,monitor,wake", "background-codex.jsonl": "shell"} {
		t.Run(file, func(t *testing.T) { checkRecordedWaiting(t, file, want) })
	}
}

func checkRecordedWaiting(t *testing.T, file, wantAfterTurn string) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "acp", file))
	if err != nil {
		t.Fatal(err)
	}
	kinds := func(state ThreadState) string {
		var out []string
		for _, w := range state.Waiting {
			out = append(out, w.Kind)
		}
		return strings.Join(out, ",")
	}
	var reducer ThreadReducer
	var afterTurn, final ThreadState
	sawSelfTurn := false
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		final = reducer.Apply(entry)
		switch {
		case entry.Kind == "turn_end":
			afterTurn = final
			afterTurn.Waiting = slices.Clone(final.Waiting)
		case entry.Kind == "async_task_spawned" && strings.Contains(line, `"taskType":"turn"`):
			sawSelfTurn = sawSelfTurn || final.Working
		}
	}
	if afterTurn.Working || kinds(afterTurn) != wantAfterTurn {
		t.Fatalf("after the turn: working=%v waiting=%q", afterTurn.Working, kinds(afterTurn))
	}
	// Only Claude starts turns of its own in these recordings.
	if !sawSelfTurn && strings.Contains(string(raw), `"taskType":"turn"`) {
		t.Fatal("a self-started turn did not mark the chat working")
	}
	if final.Working || len(final.Waiting) != 0 {
		t.Fatalf("at the end: working=%v waiting=%q", final.Working, kinds(final))
	}
}

func TestThreadReducerWaitsForAskedChatReply(t *testing.T) {
	var reducer ThreadReducer
	apply := func(kind, payload string) ThreadState {
		return reducer.Apply(LogEntry{TS: 1000, Kind: kind, Payload: json.RawMessage(payload)})
	}
	apply("tool_call", `{"toolCallId":"a1","title":"ask","_meta":{"claudeCode":{"toolName":"mcp__agentpack__ask"}}}`)
	state := apply("tool_call_update", `{"toolCallId":"a1","rawInput":{"session_id":"session-b","text":"secret request"}}`)
	if len(state.Waiting) != 1 || state.Waiting[0].Kind != "reply" || state.Waiting[0].Peer != "session-b" || state.Waiting[0].Until != 1000+replyLimit {
		t.Fatalf("ask: %+v", state.Waiting)
	}
	if body, _ := json.Marshal(state.Waiting); strings.Contains(string(body), "secret") {
		t.Fatal("waiting stored tool input", string(body))
	}
	if state = apply("prompt", `{"text":{"text":"x"},"from":{"session":"session-c","kind":"reply"}}`); len(state.Waiting) != 1 {
		t.Fatal("a reply from another chat closed the wait")
	}
	if state = apply("prompt", `{"text":{"text":"x"},"from":{"session":"session-b","kind":"reply","task":"t1"}}`); len(state.Waiting) != 0 {
		t.Fatalf("reply did not close the wait: %+v", state.Waiting)
	}
	apply("async_task_spawned", `{"asyncTaskId":"b1","taskType":"shell"}`)
	if state = apply("agent_exit", `{}`); len(state.Waiting) != 0 {
		t.Fatal("agent exit kept background work", state.Waiting)
	}
}

var updateReduced = flag.Bool("update", false, "rewrite testdata/acp/*.reduced.json")

// reducedStep is one entry's effect on the derived chat state. The UI reducer test deep-equals the
// fields it computes (everything but waiting, which the browser reads from the session row).
type reducedStep struct {
	Seq           int64      `json:"seq"`
	Working       bool       `json:"working"`
	NeedsYou      bool       `json:"needs_you"`
	ContextTokens int        `json:"context_tokens,omitempty"`
	ContextLimit  int        `json:"context_limit,omitempty"`
	CostUSD       float64    `json:"cost_usd,omitempty"`
	Waiting       []WaitItem `json:"waiting,omitempty"`
}

// TestReducedTraces pins the Go fold of every fixture, one step per entry. Go is authoritative:
// run `go test ./internal/acp -run TestReducedTraces -update` after a deliberate change.
func TestReducedTraces(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "acp", "*.jsonl"))
	if err != nil || len(paths) == 0 {
		t.Fatal(paths, err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var reducer ThreadReducer
		steps := []reducedStep{}
		for line := range splitLines(data) {
			if line == "" {
				continue
			}
			var entry LogEntry
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatal(path, err)
			}
			state := reducer.Apply(entry)
			steps = append(steps, reducedStep{Seq: entry.Seq, Working: state.Working, NeedsYou: state.NeedsYou,
				ContextTokens: state.ContextTokens, ContextLimit: state.ContextLimit, CostUSD: state.CostUSD, Waiting: state.Waiting})
		}
		want, err := json.MarshalIndent(steps, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, '\n')
		golden := strings.TrimSuffix(path, ".jsonl") + ".reduced.json"
		if *updateReduced {
			if err := os.WriteFile(golden, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(golden)
		if err != nil || string(got) != string(want) {
			t.Errorf("%s is stale (err %v); rerun with -update", golden, err)
		}
	}
}
