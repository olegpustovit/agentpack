package network

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/agentpack/internal/acp"
)

type receivedPrompt struct {
	method, path, text, key string
	from                    *acp.PromptFrom
}

// fakeChatWorker points a chat fixture at a worker that records each request it is sent.
func fakeChatWorker(t *testing.T, h *oauthHarness, id string) func() []receivedPrompt {
	t.Helper()
	var mu sync.Mutex
	var got []receivedPrompt
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, receivedPrompt{r.Method, r.URL.Path, body.Text, r.Header.Get("Idempotency-Key"), promptFromHeader(r)})
		mu.Unlock()
		writePromptAccepted(w, "")
	}))
	t.Cleanup(server.Close)
	data, _ := json.Marshal(WorkerRecord{ID: id, Address: strings.TrimPrefix(server.URL, "http://"), Secret: strings.Repeat("s", 64)})
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
		if _, err := h.gateway.store.DB.Exec("INSERT INTO conversation_events(session_id,seq,body) VALUES (?,?,?)", session, entry.Seq, string(mustRawJSON(entry))); err != nil {
			t.Fatal(err)
		}
		if _, err := h.gateway.store.DB.Exec("UPDATE sessions SET last_seq=? WHERE id=?", entry.Seq, session); err != nil {
			t.Fatal(err)
		}
	}
}

func taskPrompt(seq int64, task string) acp.LogEntry {
	return acp.LogEntry{Seq: seq, Kind: "prompt", Payload: promptLogPayload(acp.PromptInput{Text: "x", From: &acp.PromptFrom{Session: "s", Task: task, Kind: "ask"}})}
}

func message(seq int64, text string) acp.LogEntry {
	return acp.LogEntry{Seq: seq, Kind: "agent_message_chunk", Payload: mustRawJSON(map[string]any{"text": map[string]string{"text": text}})}
}

func turnEnd(seq int64) acp.LogEntry {
	return acp.LogEntry{Seq: seq, Kind: "turn_end", Payload: json.RawMessage(`{"stopReason":"end_turn"}`)}
}

func TestAgentsAskReplyAndWakeTheSender(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA, tokenB := chatFixture(t, h, "session-a", ""), chatFixture(t, h, "session-b", "")
	toA, toB := fakeChatWorker(t, h, "session-a"), fakeChatWorker(t, h, "session-b")

	// A asks B, a separate top-level chat of the same account.
	asked, text, failed := structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Review auth.go"})
	if failed || asked.TaskID == "" || asked.State != taskSubmitted || asked.ContextID != asked.TaskID {
		t.Fatalf("ask: %s %+v", text, asked)
	}
	prompts := toB()
	if len(prompts) != 1 || prompts[0].from == nil || prompts[0].from.Session != "session-a" || prompts[0].from.Task != asked.TaskID || prompts[0].from.Kind != "ask" ||
		!strings.HasPrefix(prompts[0].text, "[agentpack message · task "+asked.TaskID) || !strings.HasSuffix(prompts[0].text, envelopeSeparator+"Review auth.go") || !validOperationID(prompts[0].key) {
		t.Fatalf("B received %+v", prompts)
	}
	if state, _ := taskRow(t, h, asked.TaskID, "in"); state != "submitted" {
		t.Fatal("in row", state)
	}

	// B replies; the answer wakes A with a prompt naming B.
	replied, text, failed := structured[mcpReplied](t, h, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "Line 4 leaks the token."})
	if failed || !replied.Delivered || replied.State != taskCompleted {
		t.Fatalf("reply: %s %+v", text, replied)
	}
	prompts = toA()
	if len(prompts) != 1 || prompts[0].from == nil || prompts[0].from.Session != "session-b" || prompts[0].from.Kind != "reply" || prompts[0].from.State != "completed" ||
		!strings.Contains(prompts[0].text, "[agentpack reply · task "+asked.TaskID+" · completed") || !strings.HasSuffix(prompts[0].text, "Line 4 leaks the token.") {
		t.Fatalf("A received %+v", prompts)
	}
	if state, result := taskRow(t, h, asked.TaskID, "out"); state != "completed" || result != "Line 4 leaks the token." {
		t.Fatal("out row", state, result)
	}
	if _, text, failed := structured[mcpReplied](t, h, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "again"}); !failed || !strings.Contains(text, "not waiting") {
		t.Fatal("second reply accepted:", text)
	}
	list, _, _ := structured[struct{ Tasks []mcpTask }](t, h, tokenA, "tasks", map[string]any{})
	if len(list.Tasks) != 1 || list.Tasks[0].Direction != "sent" || list.Tasks[0].SessionID != "session-b" || list.Tasks[0].Result != "Line 4 leaks the token." {
		t.Fatalf("A's tasks %+v", list.Tasks)
	}

	// B asks A a question first; A answers it; the task goes back to B.
	asked, _, _ = structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Fix the bug"})
	if _, text, failed := structured[mcpReplied](t, h, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "Which bug?", "state": "input_required"}); failed {
		t.Fatal(text)
	}
	if last := lastPrompt(t, toA); !strings.HasPrefix(last.text, "[agentpack question · task "+asked.TaskID) {
		t.Fatalf("question %+v", last)
	}
	if replied, text, failed := structured[mcpReplied](t, h, tokenA, "reply", map[string]any{"task_id": asked.TaskID, "text": "The token leak"}); failed || replied.State != taskSubmitted {
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
	if err := h.gateway.agentTasksPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if in, _ := taskRow(t, h, asked.TaskID, "in"); in != "working" {
		t.Fatal("in row while working:", in)
	}
	if out, _ := taskRow(t, h, asked.TaskID, "out"); out != "working" {
		t.Fatal("out row while working:", out)
	}
	appendEvents(t, h, "session-b", turnEnd(4))
	if err := h.gateway.agentTasksPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if last := lastPrompt(t, toA); last.from.Task != asked.TaskID || !strings.HasSuffix(last.text, "Fixed in abc123.") {
		t.Fatalf("fallback answer %+v", last)
	}
	if state, result := taskRow(t, h, asked.TaskID, "out"); state != "completed" || result != "Fixed in abc123." {
		t.Fatal("out row after fallback", state, result)
	}

	// B declines.
	asked, _, _ = structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Delete prod"})
	if _, text, failed := structured[mcpReplied](t, h, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "No.", "state": "rejected"}); failed {
		t.Fatal(text)
	}
	if state, _ := taskRow(t, h, asked.TaskID, "out"); state != "rejected" || lastPrompt(t, toA).from.State != "rejected" {
		t.Fatal("rejected", state)
	}

	// A chat that stops before answering fails the task.
	chatFixture(t, h, "session-c", "")
	fakeChatWorker(t, h, "session-c")
	asked, _, _ = structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-c", "text": "Anything"})
	if err := endSession(t.Context(), h.gateway.store.DB, "session-c", TerminalState{}); err != nil {
		t.Fatal(err)
	}
	if err := h.gateway.agentTasksPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, result := taskRow(t, h, asked.TaskID, "out"); state != "failed" || !strings.Contains(result, "stopped") {
		t.Fatal("stopped chat", state, result)
	}

	// An asking chat that stopped is not woken, but its tasks still get the answer.
	asked, _, _ = structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "Last one"})
	if err := endSession(t.Context(), h.gateway.store.DB, "session-a", TerminalState{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.gateway.options.Home, "network", "sessions", "session-a", "worker.json")); err != nil {
		t.Fatal(err) // a stopped chat's worker is gone
	}
	woken := len(toA())
	if replied, text, failed := structured[mcpReplied](t, h, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "Done."}); failed || !replied.Delivered || len(toA()) != woken {
		t.Fatalf("reply to a stopped chat: %s %+v", text, replied)
	}
	if state, result := taskRow(t, h, asked.TaskID, "out"); state != "completed" || result != "Done." {
		t.Fatal("stopped asker's task", state, result)
	}
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
		writePromptAccepted(w, "")
	}))
	defer server.Close()
	data, _ := json.Marshal(WorkerRecord{ID: "session-b", Address: strings.TrimPrefix(server.URL, "http://"), Secret: strings.Repeat("s", 64)})
	if err := os.WriteFile(filepath.Join(h.gateway.options.Home, "network", "sessions", "session-b", "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		return calls[len(calls)-1]
	}
	// Still queued: the queued prompt is removed.
	queued, _, _ := structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "one"})
	var operation string
	if err := h.gateway.store.DB.QueryRow("SELECT operation FROM agent_tasks WHERE id=? AND role='in'", queued.TaskID).Scan(&operation); err != nil {
		t.Fatal(err)
	}
	if out, text, failed := structured[struct{ State taskState }](t, h, tokenA, "cancel_task", map[string]any{"task_id": queued.TaskID}); failed || out.State != taskCanceled {
		t.Fatal("cancel:", text)
	}
	if got := last(); got != "DELETE /conversation/queue/"+operation {
		t.Fatal("queued prompt not removed:", got)
	}
	// Working: the turn it started is cancelled.
	working, _, _ := structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "two"})
	appendEvents(t, h, "session-b", taskPrompt(1, working.TaskID))
	if _, text, failed := structured[struct{ State taskState }](t, h, tokenA, "cancel_task", map[string]any{"task_id": working.TaskID}); failed {
		t.Fatal(text)
	}
	if got := last(); got != "POST /conversation/cancel" {
		t.Fatal("turn not cancelled:", got)
	}
	for _, task := range []string{queued.TaskID, working.TaskID} {
		if in, _ := taskRow(t, h, task, "in"); in != "canceled" {
			t.Fatal("in row", in)
		}
		if _, text, failed := structured[mcpReplied](t, h, tokenB, "reply", map[string]any{"task_id": task, "text": "late"}); !failed {
			t.Fatal("answer to a canceled task accepted:", text)
		}
	}
	if _, text, failed := structured[struct{ State taskState }](t, h, tokenA, "cancel_task", map[string]any{"task_id": working.TaskID}); !failed || !strings.Contains(text, "already ended") {
		t.Fatal("second cancel:", text)
	}
}

func TestAskLimitsAndContexts(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA, tokenB := chatFixture(t, h, "session-a", ""), chatFixture(t, h, "session-b", "")
	chatFixture(t, h, "session-c", "")
	fakeChatWorker(t, h, "session-a")
	fakeChatWorker(t, h, "session-b")
	fakeChatWorker(t, h, "session-c")
	if _, text, failed := structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-a", "text": "hi"}); !failed || !strings.Contains(text, "itself") {
		t.Fatal("asked itself:", text)
	}
	// A chat of another account cannot be asked.
	other := browserActor("other@example.com")
	if err := upsertSession(t.Context(), h.gateway.store.DB, SessionInfo{ID: "session-other", Harness: "claude", Transport: "acp", StartedBy: &other, ConversationState: "idle", State: TerminalState{Running: true}}); err != nil {
		t.Fatal(err)
	}
	if _, text, failed := structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-other", "text": "hi"}); !failed {
		t.Fatal("asked another account's chat:", text)
	}
	if n := countRows(t, h, "SELECT count(*) FROM agent_tasks"); n != 0 {
		t.Fatal("a refused ask left rows:", n)
	}
	// An ask made while answering one continues its context, one hop further.
	first, _, _ := structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "hi", "context_id": "ctx-1"})
	second, _, _ := structured[mcpAsked](t, h, tokenB, "ask", map[string]any{"session_id": "session-c", "text": "sub"})
	if first.ContextID != "ctx-1" || second.ContextID != "ctx-1" || countRows(t, h, "SELECT hops FROM agent_tasks WHERE id='"+second.TaskID+"' AND role='in'") != 1 {
		t.Fatalf("context %+v %+v", first, second)
	}
	list, _, _ := structured[struct{ Tasks []mcpTask }](t, h, tokenB, "tasks", map[string]any{"context_id": "ctx-1"})
	if len(list.Tasks) != 2 {
		t.Fatalf("B's ctx-1 tasks %+v", list.Tasks)
	}
	// A request that already passed through maxTaskHops chats is answered, not forwarded.
	if _, err := h.gateway.store.DB.Exec("UPDATE agent_tasks SET hops=? WHERE id=?", maxTaskHops, first.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, text, failed := structured[mcpAsked](t, h, tokenB, "ask", map[string]any{"session_id": "session-c", "text": "hi"}); !failed || !strings.Contains(text, "passed through") {
		t.Fatal("hop limit:", text)
	}
	for range maxAsksPerMinute - 1 {
		if _, text, failed := structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-c", "text": "hi"}); failed {
			t.Fatal(text)
		}
	}
	if _, text, failed := structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-c", "text": "hi"}); !failed || !strings.Contains(text, "last minute") {
		t.Fatal("rate limit:", text)
	}
	// A person's MCP client has no chat to be woken.
	if _, err := h.gateway.mcp.ask(t.Context(), "", "session-b", "hi", ""); err != errNotAChat {
		t.Fatal("person asked:", err)
	}
}

func TestWaitReturnsWhenATaskIsAnswered(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA, tokenB := chatFixture(t, h, "session-a", ""), chatFixture(t, h, "session-b", "")
	fakeChatWorker(t, h, "session-a")
	fakeChatWorker(t, h, "session-b")
	poll := mcpWaitPoll
	mcpWaitPoll = time.Millisecond
	t.Cleanup(func() { mcpWaitPoll = poll })
	asked, _, _ := structured[mcpAsked](t, h, tokenA, "ask", map[string]any{"session_id": "session-b", "text": "hi"})
	type waited struct {
		Reached bool
		Tasks   []mcpTask
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
	structured[mcpReplied](t, h, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "hello"})
	select {
	case out := <-done:
		if !out.Reached || len(out.Tasks) != 1 || out.Tasks[0].State != taskCompleted || out.Tasks[0].Result != "hello" {
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
		from.gateway.mcp.peer = func(node string) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if node != toID {
					http.Error(w, "node is unavailable or not allowed", http.StatusBadGateway)
					return
				}
				actor, _ := actorFrom(r.Context())
				r = r.WithContext(withActor(r.Context(), Actor{Kind: "peer", Account: true, User: actor.User, StableNodeID: fromID}))
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
	asked, text, failed := structured[mcpAsked](t, one, tokenA, "ask", map[string]any{"session_id": "session-b", "node": idTwo, "text": "Run the GPU benchmark"})
	if failed {
		t.Fatal("ask across machines:", text)
	}
	if last := lastPrompt(t, toB); last.from.Node != idOne || last.from.Task != asked.TaskID {
		t.Fatalf("B received %+v", last)
	}
	var fromNode string
	if err := two.gateway.store.DB.QueryRow("SELECT from_node FROM agent_tasks WHERE id=? AND role='in'", asked.TaskID).Scan(&fromNode); err != nil || fromNode != idOne {
		t.Fatal("in row on the other machine:", fromNode, err)
	}
	// Working and the answer travel back to the asking machine.
	appendEvents(t, two, "session-b", taskPrompt(1, asked.TaskID))
	if err := two.gateway.agentTasksPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, _ := taskRow(t, one, asked.TaskID, "out"); state != "working" {
		t.Fatal("status across machines:", state)
	}
	if replied, text, failed := structured[mcpReplied](t, two, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "42 it/s"}); failed || !replied.Delivered {
		t.Fatal("reply across machines:", text)
	}
	if last := lastPrompt(t, toA); last.from.Node != idTwo || !strings.HasSuffix(last.text, "42 it/s") {
		t.Fatalf("A received %+v", last)
	}
	list, _, _ := structured[struct{ Tasks []mcpTask }](t, one, tokenA, "tasks", map[string]any{})
	if len(list.Tasks) != 1 || list.Tasks[0].Node != idTwo || list.Tasks[0].State != taskCompleted {
		t.Fatalf("A's tasks %+v", list.Tasks)
	}
	// Cancel across machines.
	asked, _, _ = structured[mcpAsked](t, one, tokenA, "ask", map[string]any{"session_id": "session-b", "node": idTwo, "text": "Another"})
	if _, text, failed := structured[struct{ State taskState }](t, one, tokenA, "cancel_task", map[string]any{"task_id": asked.TaskID}); failed {
		t.Fatal(text)
	}
	if state, _ := taskRow(t, two, asked.TaskID, "in"); state != "canceled" {
		t.Fatal("cancel across machines:", state)
	}
	// A stopped asking chat on the other machine still gets its answer in tasks.
	asked, _, _ = structured[mcpAsked](t, one, tokenA, "ask", map[string]any{"session_id": "session-b", "node": idTwo, "text": "Last"})
	if err := endSession(t.Context(), one.gateway.store.DB, "session-a", TerminalState{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(one.gateway.options.Home, "network", "sessions", "session-a", "worker.json")); err != nil {
		t.Fatal(err)
	}
	if replied, text, failed := structured[mcpReplied](t, two, tokenB, "reply", map[string]any{"task_id": asked.TaskID, "text": "Done remotely."}); failed || !replied.Delivered {
		t.Fatal(text)
	}
	if state, result := taskRow(t, one, asked.TaskID, "out"); state != "completed" || result != "Done remotely." {
		t.Fatal("stopped asker across machines:", state, result)
	}
	// An older machine without the route says so.
	one.gateway.mcp.peer = func(string) http.Handler { return http.NotFoundHandler() }
	if _, text, failed := structured[mcpAsked](t, one, tokenA, "ask", map[string]any{"session_id": "session-b", "node": idTwo, "text": "hi"}); !failed || !strings.Contains(text, "update it") {
		t.Fatal("old peer:", text)
	}
}

func TestA2AClientsAndAgents(t *testing.T) {
	h := newOAuthHarness(t)
	tokenA, tokenB := chatFixture(t, h, "session-a", ""), chatFixture(t, h, "session-b", "")
	toA, toB := fakeChatWorker(t, h, "session-a"), fakeChatWorker(t, h, "session-b")
	if _, err := h.gateway.store.DB.Exec("UPDATE sessions SET name='Reviewer' WHERE id='session-b'"); err != nil {
		t.Fatal(err)
	}
	access := h.exchange(t, h.codeForm(h.code(t)), 200).Access
	base := h.server.URL + "/a2a/session-b"

	// An A2A client: the card, then a task the chat answers.
	client, card, err := a2aClient(t.Context(), base, access)
	if err != nil || card.Name != "Reviewer" || card.SupportedInterfaces[0].URL != base {
		t.Fatalf("card %+v %v", card, err)
	}
	if _, _, err := a2aClient(t.Context(), base, "wrong"); err == nil {
		t.Fatal("card served without a token")
	}
	result, err := client.SendMessage(t.Context(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Is auth.go safe?")), Config: &a2a.SendMessageConfig{ReturnImmediately: true}})
	task, ok := result.(*a2a.Task)
	if err != nil || !ok || task.Status.State != a2a.TaskStateSubmitted {
		t.Fatalf("send %+v %v", result, err)
	}
	if last := lastPrompt(t, toB); last.from.Agent != a2aNode || last.from.Task != string(task.ID) || !strings.Contains(last.text, "A2A agent") || !strings.HasSuffix(last.text, "Is auth.go safe?") {
		t.Fatalf("B received %+v", last)
	}
	if _, text, failed := structured[mcpReplied](t, h, tokenB, "reply", map[string]any{"task_id": string(task.ID), "text": "Which version?", "state": "input_required"}); failed {
		t.Fatal(text)
	}
	got, err := client.GetTask(t.Context(), &a2a.GetTaskRequest{ID: task.ID})
	if err != nil || got.Status.State != a2a.TaskStateInputRequired || messageText(got.Status.Message) != "Which version?" {
		t.Fatalf("get %+v %v", got, err)
	}
	answer := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("main"))
	answer.TaskID = task.ID
	if _, err := client.SendMessage(t.Context(), &a2a.SendMessageRequest{Message: answer, Config: &a2a.SendMessageConfig{ReturnImmediately: true}}); err != nil {
		t.Fatal(err)
	}
	if last := lastPrompt(t, toB); !strings.HasSuffix(last.text, "main") {
		t.Fatalf("answer %+v", last)
	}
	structured[mcpReplied](t, h, tokenB, "reply", map[string]any{"task_id": string(task.ID), "text": "Safe."})
	if got, err = client.GetTask(t.Context(), &a2a.GetTaskRequest{ID: task.ID}); err != nil || got.Status.State != a2a.TaskStateCompleted || messageText(got.Status.Message) != "Safe." {
		t.Fatalf("completed %+v %v", got, err)
	}
	list, err := client.ListTasks(t.Context(), &a2a.ListTasksRequest{})
	if err != nil || len(list.Tasks) != 1 {
		t.Fatalf("list %+v %v", list, err)
	}
	if _, err := client.CancelTask(t.Context(), &a2a.CancelTaskRequest{ID: task.ID}); !errors.Is(err, a2a.ErrTaskNotCancelable) {
		t.Fatal("cancel a completed task:", err)
	}
	if _, err := client.GetTask(t.Context(), &a2a.GetTaskRequest{ID: "t_missing00000000"}); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatal("missing task:", err)
	}

	// A chat calls an A2A agent (here the gateway's own endpoint for chat B); the answer wakes it.
	called, text, failed := structured[mcpAsked](t, h, tokenA, "call_agent", map[string]any{"agent_url": base, "text": "Second opinion?", "token": access})
	if failed || called.State != taskSubmitted {
		t.Fatalf("call_agent: %s %+v", text, called)
	}
	remote := string(lastPrompt(t, toB).from.Task)
	structured[mcpReplied](t, h, tokenB, "reply", map[string]any{"task_id": remote, "text": "Looks fine."})
	if err := h.gateway.agentTasksPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if last := lastPrompt(t, toA); last.from.Agent != a2aNode || last.from.Task != called.TaskID || !strings.Contains(last.text, `A2A agent "Reviewer"`) || !strings.HasSuffix(last.text, "Looks fine.") {
		t.Fatalf("A woken by %+v", last)
	}
	tasks, _, _ := structured[struct{ Tasks []mcpTask }](t, h, tokenA, "tasks", map[string]any{})
	if len(tasks.Tasks) != 1 || tasks.Tasks[0].AgentURL != base || tasks.Tasks[0].State != taskCompleted || tasks.Tasks[0].SessionID != "" {
		t.Fatalf("A's tasks %+v", tasks.Tasks)
	}
	// Cancelling a call cancels the agent's task.
	called, _, _ = structured[mcpAsked](t, h, tokenA, "call_agent", map[string]any{"agent_url": base, "text": "Never mind", "token": access})
	remote = string(lastPrompt(t, toB).from.Task)
	if _, text, failed := structured[struct{ State taskState }](t, h, tokenA, "cancel_task", map[string]any{"task_id": called.TaskID}); failed {
		t.Fatal(text)
	}
	if state, _ := taskRow(t, h, remote, "in"); state != "canceled" {
		t.Fatal("remote task", state)
	}
	if _, text, failed := structured[mcpAsked](t, h, tokenA, "call_agent", map[string]any{"agent_url": "file:///etc/passwd", "text": "hi"}); !failed {
		t.Fatal("non-http agent URL accepted:", text)
	}
}

func TestPromptSenderSurvivesTheQueueAndNamesTheChat(t *testing.T) {
	from := &acp.PromptFrom{Session: "session-a", Task: "t_0123456789abcdef", Kind: "ask"}
	input, err := promptInputFromLog(queuedPromptPayload(queuedPrompt{ID: "q", Input: acp.PromptInput{Text: "hi", From: from}}))
	if err != nil || input.From == nil || *input.From != *from {
		t.Fatalf("%+v %v", input.From, err)
	}
	message := agentMessage{Task: from.Task, Kind: "ask", FromSession: "session-a", FromAgent: "claude", Text: "Review   auth.go"}
	if title := titleFromPrompt(envelope(message)); title != "Review auth.go" {
		t.Fatal(title)
	}
	// A person cannot set a sender: the worker reads it only from the gateway's header.
	request := httptest.NewRequest("POST", "/conversation/prompt", strings.NewReader(`{"text":"hi","from":{"session":"x","task":"t"}}`))
	if prompt, _, ok := readPromptRequest(httptest.NewRecorder(), request); ok && prompt.From != nil {
		t.Fatalf("sender from the body: %+v", prompt.From)
	}
}
