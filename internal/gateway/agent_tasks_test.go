package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gatewaymcp "github.com/olegpustovit/agentpack/internal/gateway/mcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
)

type receivedPrompt struct {
	method, path, text, key string
	from                    *acp.PromptFrom
	steer                   bool
}

// fakeChatWorker points a chat fixture at a worker that records each request it is sent.
// Task tool results as an MCP client decodes them.
type (
	taskAsked struct {
		TaskID    string `json:"task_id"`
		ContextID string `json:"context_id"`
		State     string `json:"state"`
	}
	taskReplied struct {
		State     string `json:"state"`
		Delivered bool   `json:"delivered"`
	}
	taskView struct {
		TaskID    string    `json:"task_id"`
		ContextID string    `json:"context_id"`
		Direction string    `json:"direction"`
		SessionID string    `json:"session_id"`
		Node      string    `json:"node"`
		State     string    `json:"state"`
		Request   string    `json:"request"`
		Result    string    `json:"result"`
		UpdatedAt time.Time `json:"updated_at"`
		Chat      *struct {
			State      string
			Background []acp.WaitItem
		} `json:"chat"`
	}
)

func fakeChatWorker(t *testing.T, h *oauthHarness, id string) func() []receivedPrompt {
	t.Helper()
	var mu sync.Mutex
	var got []receivedPrompt
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text  string `json:"text"`
			Steer bool   `json:"steer"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, receivedPrompt{r.Method, r.URL.Path, body.Text, r.Header.Get("Idempotency-Key"), network.PromptFromHeader(r), body.Steer})
		mu.Unlock()
		network.WritePromptAccepted(w, "")
	}))
	t.Cleanup(server.Close)
	data, _ := json.Marshal(network.WorkerRecord{ID: id, Address: strings.TrimPrefix(server.URL, "http://"), Secret: strings.Repeat("s", 64)})
	if err := os.WriteFile(filepath.Join(h.gateway.options.Home, "network", "sessions", id, "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return func() []receivedPrompt {
		mu.Lock()
		defer mu.Unlock()
		var prompts []receivedPrompt
		for _, request := range got {
			if request.path == "/conversation/prompt" {
				prompts = append(prompts, request)
			}
		}
		return prompts
	}
}

func lastPrompt(t *testing.T, prompts func() []receivedPrompt) receivedPrompt {
	t.Helper()
	all := prompts()
	if len(all) == 0 {
		t.Fatal("no prompt")
	}
	return all[len(all)-1]
}

func structured[T any](t *testing.T, h *oauthHarness, token, tool string, args map[string]any) (T, string, bool) {
	t.Helper()
	result, text := callTool(t, mcpClient(t, h, token), tool, args)
	var out T
	if !result.IsError {
		data, _ := json.Marshal(result.StructuredContent)
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out, text, result.IsError
}

func taskRow(t *testing.T, h *oauthHarness, id, role string) (state, result string) {
	t.Helper()
	if err := h.gateway.store.DB.QueryRow("SELECT state,result FROM agent_tasks WHERE id=? AND role=?", id, role).Scan(&state, &result); err != nil {
		t.Fatal(id, role, err)
	}
	return state, result
}

func countRows(t *testing.T, h *oauthHarness, query string) int {
	t.Helper()
	var n int
	if err := h.gateway.store.DB.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// appendEvents writes a chat's conversation as its worker would.
func appendEvents(t *testing.T, h *oauthHarness, session string, entries ...acp.LogEntry) {
	t.Helper()
	for _, entry := range entries {
		if _, err := h.gateway.store.DB.Exec("INSERT INTO conversation_events(session_id,seq,body) VALUES (?,?,?)", session, entry.Seq, string(network.MustRawJSON(entry))); err != nil {
			t.Fatal(err)
		}
		if _, err := h.gateway.store.DB.Exec("UPDATE sessions SET last_seq=? WHERE id=?", entry.Seq, session); err != nil {
			t.Fatal(err)
		}
	}
}

func taskPrompt(seq int64, task string) acp.LogEntry {
	return acp.LogEntry{Seq: seq, Kind: "prompt", Payload: network.PromptLogPayload(acp.PromptInput{Text: "x", From: &acp.PromptFrom{Session: "s", Task: task, Kind: "ask"}})}
}

func message(seq int64, text string) acp.LogEntry {
	return acp.LogEntry{Seq: seq, Kind: "agent_message_chunk", Payload: network.MustRawJSON(map[string]any{"text": map[string]string{"text": text}})}
}

func turnEnd(seq int64) acp.LogEntry {
	return acp.LogEntry{Seq: seq, Kind: "turn_end", Payload: json.RawMessage(`{"stopReason":"end_turn"}`)}
}

func TestAgentsAskReplyAndWakeTheSender(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA, tokenB := chatFixture(t, h, "session-a", ""), chatFixture(t, h, "session-b", "")
	toA, toB := fakeChatWorker(t, h, "session-a"), fakeChatWorker(t, h, "session-b")

	// A asks B, a separate top-level chat of the same account.
	asked, text, failed := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Review auth.go"})
	if failed || asked.TaskID == "" || asked.State != "submitted" || asked.ContextID != asked.TaskID {
		t.Fatalf("ask: %s %+v", text, asked)
	}
	prompts := toB()
	if len(prompts) != 1 || prompts[0].steer || prompts[0].from == nil || prompts[0].from.Session != "session-a" || prompts[0].from.Task != asked.TaskID || prompts[0].from.Kind != "ask" ||
		!strings.HasPrefix(prompts[0].text, "[agentpack message · task "+asked.TaskID) || !strings.HasSuffix(prompts[0].text, network.EnvelopeSeparator+"Review auth.go") || !network.ValidOperationID(prompts[0].key) {
		t.Fatalf("B received %+v", prompts)
	}
	if state, _ := taskRow(t, h, asked.TaskID, "in"); state != "submitted" {
		t.Fatal("in row", state)
	}

	// B replies; the answer wakes A with a prompt naming B.
	replied, text, failed := structured[taskReplied](t, h, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "Line 4 leaks the token."})
	if failed || !replied.Delivered || replied.State != "completed" {
		t.Fatalf("reply: %s %+v", text, replied)
	}
	prompts = toA()
	if len(prompts) != 1 || prompts[0].steer || prompts[0].from == nil || prompts[0].from.Session != "session-b" || prompts[0].from.Kind != "reply" || prompts[0].from.State != "completed" ||
		!strings.Contains(prompts[0].text, "[agentpack reply · task "+asked.TaskID+" · completed") || !strings.HasSuffix(prompts[0].text, "Line 4 leaks the token.") {
		t.Fatalf("A received %+v", prompts)
	}
	if state, result := taskRow(t, h, asked.TaskID, "out"); state != "completed" || result != "Line 4 leaks the token." {
		t.Fatal("out row", state, result)
	}
	if _, text, failed := structured[taskReplied](t, h, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "again"}); !failed || !strings.Contains(text, "not waiting") {
		t.Fatal("second reply accepted:", text)
	}
	list, _, _ := structured[struct{ Tasks []taskView }](t, h, tokenA, "tasks", map[string]any{})
	if len(list.Tasks) != 1 || list.Tasks[0].Direction != "sent" || list.Tasks[0].SessionID != "session-b" || list.Tasks[0].Result != "Line 4 leaks the token." {
		t.Fatalf("A's tasks %+v", list.Tasks)
	}

	// B asks A a question first; A answers it; the task goes back to B.
	asked, _, _ = structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Fix the bug"})
	if _, text, failed := structured[taskReplied](t, h, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "Which bug?", "state": "input_required"}); failed {
		t.Fatal(text)
	}
	if last := lastPrompt(t, toA); !strings.HasPrefix(last.text, "[agentpack question · task "+asked.TaskID) {
		t.Fatalf("question %+v", last)
	}
	if replied, text, failed := structured[taskReplied](t, h, tokenA, "reply", map[string]any{"task_id": asked.TaskID, "text": "The token leak"}); failed || replied.State != "submitted" {
		t.Fatal("answering the question:", text)
	}
	if last := lastPrompt(t, toB); last.from.Kind != "ask" || last.from.Task != asked.TaskID || !strings.HasSuffix(last.text, "The token leak") {
		t.Fatalf("answer to the question %+v", last)
	}
	if state, _ := taskRow(t, h, asked.TaskID, "out"); state != "submitted" {
		t.Fatal("out row after answer", state)
	}

	// B never calls reply: its turn starting marks the task working on both sides, and the turn's last message answers it.
	appendEvents(t, h, "session-b", taskPrompt(1, asked.TaskID), message(2, "Fixed in "), message(3, "abc123."))
	if err := h.gateway.mcp.TaskPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if in, _ := taskRow(t, h, asked.TaskID, "in"); in != "working" {
		t.Fatal("in row while working:", in)
	}
	if out, _ := taskRow(t, h, asked.TaskID, "out"); out != "working" {
		t.Fatal("out row while working:", out)
	}
	appendEvents(t, h, "session-b", turnEnd(4))
	if err := h.gateway.mcp.TaskPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if last := lastPrompt(t, toA); last.from.Task != asked.TaskID || !strings.HasSuffix(last.text, "Fixed in abc123.") {
		t.Fatalf("fallback answer %+v", last)
	}
	if state, result := taskRow(t, h, asked.TaskID, "out"); state != "completed" || result != "Fixed in abc123." {
		t.Fatal("out row after fallback", state, result)
	}

	// B declines.
	asked, _, _ = structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Delete prod"})
	if _, text, failed := structured[taskReplied](t, h, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "No.", "state": "rejected"}); failed {
		t.Fatal(text)
	}
	if state, _ := taskRow(t, h, asked.TaskID, "out"); state != "rejected" || lastPrompt(t, toA).from.State != "rejected" {
		t.Fatal("rejected", state)
	}

	// A chat that stops before answering keeps the task open while it may resume by itself, then
	// fails it and wakes the asker.
	chatFixture(t, h, "session-c", "")
	fakeChatWorker(t, h, "session-c")
	asked, _, _ = structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-c", "text": "Anything"})
	if err := session.EndSession(t.Context(), h.gateway.store.DB, "session-c", session.TerminalState{}); err != nil {
		t.Fatal(err)
	}
	if err := h.gateway.mcp.TaskPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, _ := taskRow(t, h, asked.TaskID, "in"); state != "submitted" {
		t.Fatal("failed before the chat could resume:", state)
	}
	if _, err := h.gateway.store.DB.Exec("UPDATE sessions SET ended_at=? WHERE id='session-c'", time.Now().Add(-2*time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := h.gateway.mcp.TaskPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, result := taskRow(t, h, asked.TaskID, "out"); state != "failed" || !strings.Contains(result, "stopped") || !strings.Contains(result, "restart_session") {
		t.Fatal("stopped chat", state, result)
	}
	if last := lastPrompt(t, toA); last.from.Task != asked.TaskID || last.from.State != "failed" {
		t.Fatalf("asker not woken by the failure: %+v", last)
	}

	// An asking chat that stopped gets the answer stored, and is woken with it once it runs again.
	asked, _, _ = structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Last one"})
	if err := session.EndSession(t.Context(), h.gateway.store.DB, "session-a", session.TerminalState{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.gateway.options.Home, "network", "sessions", "session-a", "worker.json")); err != nil {
		t.Fatal(err) // a stopped chat's worker is gone
	}
	woken := len(toA())
	if replied, text, failed := structured[taskReplied](t, h, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "Done."}); failed || replied.Delivered || len(toA()) != woken {
		t.Fatalf("reply to a stopped chat: %s %+v", text, replied)
	}
	if state, result := taskRow(t, h, asked.TaskID, "out"); state != "completed" || result != "Done." {
		t.Fatal("stopped asker's task", state, result)
	}
	if _, err := h.gateway.store.DB.Exec("UPDATE sessions SET ended_at=0 WHERE id='session-a'"); err != nil {
		t.Fatal(err)
	}
	toA = fakeChatWorker(t, h, "session-a")
	if err := h.gateway.mcp.TaskPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if last := lastPrompt(t, toA); last.from.Task != asked.TaskID || !strings.HasSuffix(last.text, "Done.") {
		t.Fatalf("resumed asker not woken: %+v", last)
	}
}

// A turn that ends while its own background work runs (a deploy, a render) is progress, not the
// answer: the task stays working until a later turn ends with nothing left running.
func TestTaskWaitsForTheChatsBackgroundWork(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA := chatFixture(t, h, "session-a", "")
	chatFixture(t, h, "session-b", "")
	toA := fakeChatWorker(t, h, "session-a")
	fakeChatWorker(t, h, "session-b")
	pass := func() {
		t.Helper()
		if err := h.gateway.mcp.TaskPass(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	waiting := func(value, state string) {
		t.Helper()
		if _, err := h.gateway.store.DB.Exec("UPDATE sessions SET waiting=?,conversation_state=? WHERE id='session-b'", value, state); err != nil {
			t.Fatal(err)
		}
	}
	update := func(seq int64, kind, payload string) acp.LogEntry {
		return acp.LogEntry{Seq: seq, Kind: kind, Payload: json.RawMessage(payload)}
	}

	asked, _, _ := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Deploy and report"})
	waiting(`[{"kind":"shell"}]`, "idle")
	appendEvents(t, h, "session-b", taskPrompt(1, asked.TaskID), message(2, "Waiting for the deploy."), turnEnd(3))
	pass()
	if state, result := taskRow(t, h, asked.TaskID, "out"); state != "working" || result != "Waiting for the deploy." {
		t.Fatal("interim message answered the task:", state, result)
	}
	if len(toA()) != 0 {
		t.Fatalf("asker woken by progress: %+v", toA())
	}
	list, _, _ := structured[struct{ Tasks []taskView }](t, h, tokenA, "tasks", map[string]any{})
	if len(list.Tasks) != 1 || list.Tasks[0].State != "working" || list.Tasks[0].Chat == nil || list.Tasks[0].Chat.State != "idle" ||
		len(list.Tasks[0].Chat.Background) != 1 || list.Tasks[0].Chat.Background[0].Kind != "shell" {
		t.Fatalf("tasks %+v", list.Tasks)
	}

	// The deploy finishes and the chat wakes by itself, as Claude reports it: the job's task closes,
	// a "turn" task opens and closes. Until that turn ends, nothing is running in the background, yet
	// the chat works: still progress.
	waiting("", "working")
	appendEvents(t, h, "session-b", update(4, "async_task_state_update", `{"asyncTaskId":"job","state":"completed"}`),
		update(5, "async_task_spawned", `{"asyncTaskId":"autonomous-turn-1","taskType":"turn"}`))
	pass()
	if state, _ := taskRow(t, h, asked.TaskID, "out"); state != "working" || len(toA()) != 0 {
		t.Fatal("answered while the woken turn works:", state)
	}
	waiting("", "idle")
	appendEvents(t, h, "session-b", message(6, "Deployed; all checks pass."), update(7, "async_task_state_update", `{"asyncTaskId":"autonomous-turn-1","state":"completed"}`))
	pass()
	if state, result := taskRow(t, h, asked.TaskID, "out"); state != "completed" || result != "Deployed; all checks pass." {
		t.Fatal("answer", state, result)
	}
	if last := lastPrompt(t, toA); !strings.HasSuffix(last.text, "Deployed; all checks pass.") {
		t.Fatalf("A received %+v", last)
	}

	// A held task whose chat stops fails, naming the running work and keeping its last message.
	asked, _, _ = structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Render the PDF"})
	waiting(`[{"kind":"shell"}]`, "idle")
	appendEvents(t, h, "session-b", taskPrompt(8, asked.TaskID), message(9, "Rendering."), turnEnd(10))
	pass()
	if err := session.EndSession(t.Context(), h.gateway.store.DB, "session-b", session.TerminalState{}); err != nil {
		t.Fatal(err)
	}
	pass()
	if state, result := taskRow(t, h, asked.TaskID, "out"); state != "failed" || !strings.Contains(result, "background work") || !strings.HasSuffix(result, "Rendering.") {
		t.Fatal("held task of a stopped chat", state, result)
	}
}

// A turn cut off by a dead worker does not answer; the chat resumes and the resumed turn does.
func TestTaskSurvivesAChatThatResumesAfterItsWorkerDied(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA := chatFixture(t, h, "session-a", "")
	chatFixture(t, h, "session-b", "")
	toA := fakeChatWorker(t, h, "session-a")
	fakeChatWorker(t, h, "session-b")
	asked, _, _ := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Check the links"})
	appendEvents(t, h, "session-b", taskPrompt(1, asked.TaskID), message(2, "Checking."))
	// A stopping agent ends its turn in an error after it exits; that is no answer.
	appendEvents(t, h, "session-b", acp.LogEntry{Seq: 3, Kind: "agent_exit", Payload: json.RawMessage(`{}`)},
		acp.LogEntry{Seq: 4, Kind: "turn_end", Payload: json.RawMessage(`{"stopReason":"error"}`)})
	if err := session.EndSession(t.Context(), h.gateway.store.DB, "session-b", session.TerminalState{ExitCode: -1}); err != nil {
		t.Fatal(err)
	}
	if err := h.gateway.mcp.TaskPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, _ := taskRow(t, h, asked.TaskID, "in"); state != "submitted" && state != "working" {
		t.Fatal("the stopping turn answered the task:", state)
	}
	// The reaper records the stop and queues a resume. A restart that takes longer than the grace
	// (after a reboot the agent may fail to start at first) still finds the task open.
	resume := network.PromptLogPayload(acp.PromptInput{Text: gatewaymcp.AutoResumePrompt})
	appendEvents(t, h, "session-b", acp.LogEntry{Seq: 5, Kind: "notice", Payload: json.RawMessage(`{}`)}, acp.LogEntry{Seq: 6, Kind: "agent_exit", Payload: json.RawMessage(`{}`)},
		acp.LogEntry{Seq: 7, TS: time.Now().UnixMilli(), Kind: "queued_prompt", Payload: resume})
	if _, err := h.gateway.store.DB.Exec("UPDATE sessions SET ended_at=? WHERE id='session-b'", time.Now().Add(-5*time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := h.gateway.mcp.TaskPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, _ := taskRow(t, h, asked.TaskID, "in"); state != "submitted" && state != "working" {
		t.Fatal("failed while a resume was pending:", state)
	}
	if _, err := h.gateway.store.DB.Exec("UPDATE sessions SET ended_at=0 WHERE id='session-b'"); err != nil {
		t.Fatal(err)
	}
	appendEvents(t, h, "session-b", acp.LogEntry{Seq: 8, Kind: "prompt", Payload: resume}, message(9, "All 32 links resolve."), turnEnd(10))
	if err := h.gateway.mcp.TaskPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, result := taskRow(t, h, asked.TaskID, "out"); state != "completed" || result != "All 32 links resolve." {
		t.Fatal("resumed answer", state, result)
	}
	if last := lastPrompt(t, toA); !strings.HasSuffix(last.text, "All 32 links resolve.") {
		t.Fatalf("A received %+v", last)
	}
}

// A resume waits for sign-ins to sync before it queues anything, and the chat may have stopped long
// before the gateway came back: the task stays open meanwhile.
func TestAgentTaskStaysOpenThroughADelayedResume(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA := chatFixture(t, h, "session-a", "")
	chatFixture(t, h, "session-b", "")
	fakeChatWorker(t, h, "session-a")
	fakeChatWorker(t, h, "session-b")
	asked, _, _ := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Check the links"})
	appendEvents(t, h, "session-b", taskPrompt(1, asked.TaskID), message(2, "Checking."), acp.LogEntry{Seq: 3, Kind: "agent_exit", Payload: json.RawMessage(`{}`)})
	stoppedFor := func(ago time.Duration, want ...string) {
		t.Helper()
		if _, err := h.gateway.store.DB.Exec("UPDATE sessions SET ended_at=? WHERE id='session-b'", time.Now().Add(-ago).UnixNano()); err != nil {
			t.Fatal(err)
		}
		if err := h.gateway.mcp.TaskPass(t.Context()); err != nil {
			t.Fatal(err)
		}
		if state, _ := taskRow(t, h, asked.TaskID, "in"); !slices.Contains(want, state) {
			t.Fatal("stopped for", ago, "state", state)
		}
	}
	stoppedFor(2*time.Minute, "submitted", "working") // longer than the 75 s wait
	h.gateway.resumePending.Store("session-b", struct{}{})
	stoppedFor(time.Hour, "submitted", "working")
	h.gateway.resumePending.Delete("session-b")
	stoppedFor(time.Hour, "failed")
}

func TestCancelTaskStopsTheWorkAndRefusesTheAnswer(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA, tokenB := chatFixture(t, h, "session-a", ""), chatFixture(t, h, "session-b", "")
	fakeChatWorker(t, h, "session-a")
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		network.WritePromptAccepted(w, "")
	}))
	defer server.Close()
	data, _ := json.Marshal(network.WorkerRecord{ID: "session-b", Address: strings.TrimPrefix(server.URL, "http://"), Secret: strings.Repeat("s", 64)})
	if err := os.WriteFile(filepath.Join(h.gateway.options.Home, "network", "sessions", "session-b", "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		return calls[len(calls)-1]
	}
	// Still queued: the queued prompt is removed.
	queued, _, _ := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "one"})
	var operation string
	if err := h.gateway.store.DB.QueryRow("SELECT operation FROM agent_tasks WHERE id=? AND role='in'", queued.TaskID).Scan(&operation); err != nil {
		t.Fatal(err)
	}
	if out, text, failed := structured[struct{ State string }](t, h, tokenA, "cancel_task", map[string]any{"task_id": queued.TaskID}); failed || out.State != "canceled" {
		t.Fatal("cancel:", text)
	}
	if got := last(); got != "DELETE /conversation/queue/"+operation {
		t.Fatal("queued prompt not removed:", got)
	}
	// Working: the turn it started is cancelled.
	working, _, _ := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "two"})
	appendEvents(t, h, "session-b", taskPrompt(1, working.TaskID))
	if _, text, failed := structured[struct{ State string }](t, h, tokenA, "cancel_task", map[string]any{"task_id": working.TaskID}); failed {
		t.Fatal(text)
	}
	if got := last(); got != "POST /conversation/cancel" {
		t.Fatal("turn not cancelled:", got)
	}
	for _, task := range []string{queued.TaskID, working.TaskID} {
		if in, _ := taskRow(t, h, task, "in"); in != "canceled" {
			t.Fatal("in row", in)
		}
		if _, text, failed := structured[taskReplied](t, h, tokenB, "reply", map[string]any{"task_id": task, "text": "late"}); !failed {
			t.Fatal("answer to a canceled task accepted:", text)
		}
	}
	if _, text, failed := structured[struct{ State string }](t, h, tokenA, "cancel_task", map[string]any{"task_id": working.TaskID}); !failed || !strings.Contains(text, "already ended") {
		t.Fatal("second cancel:", text)
	}
}

func TestCancelRemovesAPromptQueuedWhileTheChatStarts(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA := chatFixture(t, h, "session-a", "")
	fakeChatWorker(t, h, "session-a")
	owner := session.BrowserActor(testBrowserEmail)
	if err := session.UpsertSession(t.Context(), h.gateway.store.DB, session.SessionInfo{ID: "session-new", Harness: "claude", Transport: "acp", StartedBy: &owner, ConversationState: "starting", State: session.TerminalState{Running: true}}); err != nil {
		t.Fatal(err)
	}
	asked, text, failed := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-new", "text": "hi"})
	if failed {
		t.Fatal(text)
	}
	if _, text, failed := structured[struct{ State string }](t, h, tokenA, "cancel_task", map[string]any{"task_id": asked.TaskID}); failed {
		t.Fatal(text)
	}
	// The worker that starts replays the log: the prompt was queued, then removed, so it is never sent.
	session := network.NewConversationSession(nil, nil)
	rows, err := h.gateway.store.DB.Query("SELECT body FROM conversation_events WHERE session_id='session-new' ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	kinds := []string{}
	for rows.Next() {
		var body string
		var entry acp.LogEntry
		if err := rows.Scan(&body); err != nil || json.Unmarshal([]byte(body), &entry) != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, entry.Kind)
		session.ReplayEntry(entry)
	}
	if strings.Join(kinds, ",") != "queued_prompt,dequeued_prompt" || len(session.Queue) != 0 {
		t.Fatalf("log %v queue %+v", kinds, session.Queue)
	}
}

func TestAskLimitsAndContexts(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA, tokenB := chatFixture(t, h, "session-a", ""), chatFixture(t, h, "session-b", "")
	chatFixture(t, h, "session-c", "")
	fakeChatWorker(t, h, "session-a")
	fakeChatWorker(t, h, "session-b")
	fakeChatWorker(t, h, "session-c")
	if _, text, failed := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-a", "text": "hi"}); !failed || !strings.Contains(text, "itself") {
		t.Fatal("asked itself:", text)
	}
	// A chat of another account cannot be asked.
	other := session.BrowserActor("other@example.com")
	if err := session.UpsertSession(t.Context(), h.gateway.store.DB, session.SessionInfo{ID: "session-other", Harness: "claude", Transport: "acp", StartedBy: &other, ConversationState: "idle", State: session.TerminalState{Running: true}}); err != nil {
		t.Fatal(err)
	}
	if _, text, failed := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-other", "text": "hi"}); !failed {
		t.Fatal("asked another account's chat:", text)
	}
	if n := countRows(t, h, "SELECT count(*) FROM agent_tasks"); n != 0 {
		t.Fatal("a refused ask left rows:", n)
	}
	// An ask made while answering one continues its context, one hop further.
	first, _, _ := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "hi", "context_id": "ctx-1"})
	second, _, _ := structured[taskAsked](t, h, tokenB, "ask", map[string]any{"session_id": "session-c", "text": "sub"})
	if first.ContextID != "ctx-1" || second.ContextID != "ctx-1" || countRows(t, h, "SELECT hops FROM agent_tasks WHERE id='"+second.TaskID+"' AND role='in'") != 1 {
		t.Fatalf("context %+v %+v", first, second)
	}
	list, _, _ := structured[struct{ Tasks []taskView }](t, h, tokenB, "tasks", map[string]any{"context_id": "ctx-1"})
	if len(list.Tasks) != 2 {
		t.Fatalf("B's ctx-1 tasks %+v", list.Tasks)
	}
	// A request that already passed through maxTaskHops chats is answered, not forwarded.
	if _, err := h.gateway.store.DB.Exec("UPDATE agent_tasks SET hops=? WHERE id=?", 4, first.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, text, failed := structured[taskAsked](t, h, tokenB, "ask", map[string]any{"session_id": "session-c", "text": "hi"}); !failed || !strings.Contains(text, "passed through") {
		t.Fatal("hop limit:", text)
	}
	for range 20 - 1 { // 20 asks a minute per chat
		if _, text, failed := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-c", "text": "hi"}); failed {
			t.Fatal(text)
		}
	}
	if _, text, failed := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-c", "text": "hi"}); !failed || !strings.Contains(text, "last minute") {
		t.Fatal("rate limit:", text)
	}
	// A person's MCP client has no chat to be woken.
	person := h.exchange(t, h.codeForm(h.code(t)), 200).Access
	if _, text, failed := structured[taskAsked](t, h, person, "ask", map[string]any{"session_id": "session-b", "text": "hi"}); !failed || !strings.Contains(text, "are for a chat's agent") {
		t.Fatal("person asked:", text)
	}
}

func TestWaitReturnsWhenATaskIsAnswered(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA, tokenB := chatFixture(t, h, "session-a", ""), chatFixture(t, h, "session-b", "")
	fakeChatWorker(t, h, "session-a")
	fakeChatWorker(t, h, "session-b")
	poll := gatewaymcp.WaitPoll
	gatewaymcp.WaitPoll = time.Millisecond
	t.Cleanup(func() { gatewaymcp.WaitPoll = poll })
	asked, _, _ := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "hi"})
	type waited struct {
		Reached bool
		Tasks   []taskView
	}
	done := make(chan waited, 1)
	waiting := mcpClient(t, h, tokenA)
	go func() {
		result, err := waiting.CallTool(context.Background(), &mcp.CallToolParams{Name: "wait", Arguments: map[string]any{"task_ids": []string{asked.TaskID}, "timeout_seconds": 10}})
		var out waited
		if err == nil {
			data, _ := json.Marshal(result.StructuredContent)
			_ = json.Unmarshal(data, &out)
		}
		done <- out
	}()
	structured[taskReplied](t, h, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "hello"})
	select {
	case out := <-done:
		if !out.Reached || len(out.Tasks) != 1 || out.Tasks[0].State != "completed" || out.Tasks[0].Result != "hello" {
			t.Fatalf("wait %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
	}
}

// meshPair stands two gateways in for two machines: each one's MCP calls for the other's node reach
// the other's handler as the forwarded account, as the tailnet proxy delivers them.
func meshPair(t *testing.T) (*oauthHarness, *oauthHarness, string, string) {
	t.Helper()
	one, two := newOAuthHarness(t), newOAuthHarness(t)
	idOne, _ := one.gateway.store.NodeID(t.Context())
	idTwo, _ := two.gateway.store.NodeID(t.Context())
	link := func(from, to *oauthHarness, fromID, toID string) {
		from.gateway.mcpNodes = func(node string) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if node != toID {
					http.Error(w, "node is unavailable or not allowed", http.StatusBadGateway)
					return
				}
				actor, _ := session.ActorFrom(r.Context())
				r = r.WithContext(session.WithActor(r.Context(), session.Actor{Kind: "peer", Account: true, User: actor.User, Agent: actor.Agent, StableNodeID: fromID}))
				r.Header.Set("Authorization", "Bearer "+to.gateway.local.Secret)
				to.gateway.Handler().ServeHTTP(w, r)
			})
		}
	}
	link(one, two, idOne, idTwo)
	link(two, one, idTwo, idOne)
	return one, two, idOne, idTwo
}

func TestAgentsMessageAcrossMachines(t *testing.T) {
	one, two, idOne, idTwo := meshPair(t)
	tokenA, tokenB := chatFixture(t, one, "session-a", ""), chatFixture(t, two, "session-b", "")
	toA, toB := fakeChatWorker(t, one, "session-a"), fakeChatWorker(t, two, "session-b")
	asked, text, failed := structured[taskAsked](t, one, tokenA, "ask", map[string]any{"session_id": "session-b", "node": idTwo, "text": "Change course before the next GPU benchmark", "send_now": true})
	if failed {
		t.Fatal("ask across machines:", text)
	}
	if last := lastPrompt(t, toB); !last.steer || last.from.Node != idOne || last.from.Task != asked.TaskID {
		t.Fatalf("B received %+v", last)
	}
	var fromNode string
	if err := two.gateway.store.DB.QueryRow("SELECT from_node FROM agent_tasks WHERE id=? AND role='in'", asked.TaskID).Scan(&fromNode); err != nil || fromNode != idOne {
		t.Fatal("in row on the other machine:", fromNode, err)
	}
	// Working and the answer travel back to the asking machine.
	appendEvents(t, two, "session-b", taskPrompt(1, asked.TaskID))
	if err := two.gateway.mcp.TaskPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, _ := taskRow(t, one, asked.TaskID, "out"); state != "working" {
		t.Fatal("status across machines:", state)
	}
	if replied, text, failed := structured[taskReplied](t, two, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "42 it/s"}); failed || !replied.Delivered {
		t.Fatal("reply across machines:", text)
	}
	if last := lastPrompt(t, toA); last.from.Node != idTwo || !strings.HasSuffix(last.text, "42 it/s") {
		t.Fatalf("A received %+v", last)
	}
	list, _, _ := structured[struct{ Tasks []taskView }](t, one, tokenA, "tasks", map[string]any{})
	if len(list.Tasks) != 1 || list.Tasks[0].Node != idTwo || list.Tasks[0].State != "completed" {
		t.Fatalf("A's tasks %+v", list.Tasks)
	}
	// Cancel across machines.
	asked, _, _ = structured[taskAsked](t, one, tokenA, "ask", map[string]any{"session_id": "session-b", "node": idTwo, "text": "Another"})
	if _, text, failed := structured[struct{ State string }](t, one, tokenA, "cancel_task", map[string]any{"task_id": asked.TaskID}); failed {
		t.Fatal(text)
	}
	if state, _ := taskRow(t, two, asked.TaskID, "in"); state != "canceled" {
		t.Fatal("cancel across machines:", state)
	}
	// A stopped asking chat on the other machine gets its answer in tasks; the wake waits for it to run.
	asked, _, _ = structured[taskAsked](t, one, tokenA, "ask", map[string]any{"session_id": "session-b", "node": idTwo, "text": "Last"})
	if err := session.EndSession(t.Context(), one.gateway.store.DB, "session-a", session.TerminalState{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(one.gateway.options.Home, "network", "sessions", "session-a", "worker.json")); err != nil {
		t.Fatal(err)
	}
	if replied, text, failed := structured[taskReplied](t, two, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "Done remotely."}); failed || replied.Delivered {
		t.Fatal(text)
	}
	if state, result := taskRow(t, one, asked.TaskID, "out"); state != "completed" || result != "Done remotely." {
		t.Fatal("stopped asker across machines:", state, result)
	}
	// An older machine without the route says so.
	one.gateway.mcpNodes = func(string) http.Handler { return http.NotFoundHandler() }
	if _, text, failed := structured[taskAsked](t, one, tokenA, "ask", map[string]any{"session_id": "session-b", "node": idTwo, "text": "hi"}); !failed || !strings.Contains(text, "update it") {
		t.Fatal("old peer:", text)
	}
}

func TestAgentsStartHelpersOnAnotherMachine(t *testing.T) {
	one, two, _, idTwo := meshPair(t)
	token := chatFixture(t, one, "session-a", "")
	directory := filepath.Join(two.gateway.options.Home, "project")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := remember(two.gateway.options.Home, session.SessionInfo{Directory: directory, Harness: "codex"}); err != nil {
		t.Fatal(err)
	}
	var started atomic.Pointer[session.SessionInfo]
	two.gateway.options.Start = func(_ context.Context, info session.SessionInfo, _ string) (string, error) {
		started.Store(&info)
		return info.ID, nil
	}
	result, text := callTool(t, mcpClient(t, one, token), "start_session", map[string]any{"directory": directory, "agent": "codex", "node": idTwo})
	two.gateway.starting.Wait()
	info := started.Load()
	if result.IsError || info == nil || info.ParentID != "session-a" || info.NodeID != idTwo || info.StartedBy.Agent != "claude" {
		t.Fatalf("remote helper: %s %+v", text, info)
	}
	// Replication brings the helper's row back, which is what lets its parent stop it.
	row := *info
	if err := session.UpsertSession(t.Context(), one.gateway.store.DB, row); err != nil {
		t.Fatal(err)
	}
	if _, text := callTool(t, mcpClient(t, one, token), "cancel", map[string]any{"session_id": info.ID, "node": idTwo}); text == agentScopeRefusal {
		t.Fatal("an agent may change its own remote helper:", text)
	}
}

// A message another agent sends mid-turn (Claude delivers queued prompts inside a running turn)
// shares the turn's end with the task already working: that end answers both.
func TestMessageDeliveredMidTurnSharesTheAnswer(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA := chatFixture(t, h, "session-a", "")
	chatFixture(t, h, "session-b", "")
	fakeChatWorker(t, h, "session-a")
	fakeChatWorker(t, h, "session-b")
	first, _, _ := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Rewrite section 3"})
	second, _, _ := structured[taskAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Also keep it under 6 pages"})
	appendEvents(t, h, "session-b", taskPrompt(1, first.TaskID), message(2, "Rewriting."), taskPrompt(3, second.TaskID), message(4, "Section 3 rewritten; 6 pages."), turnEnd(5))
	if err := h.gateway.mcp.TaskPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.TaskID, second.TaskID} {
		if state, result := taskRow(t, h, id, "out"); state != "completed" || !strings.HasSuffix(result, "Section 3 rewritten; 6 pages.") {
			t.Fatal(id, state, result)
		}
	}
}
