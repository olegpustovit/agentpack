package network

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
)

type fakeTurn struct {
	entries []acp.LogEntry
	stop    string
	advance time.Duration // moves the fake clock during the turn
}

// fakeAgent is an ACP agent the test drives one turn at a time.
type fakeAgent struct {
	log     *acp.EventLog
	prompts chan acp.PromptInput
	turns   chan fakeTurn
	cancels chan struct{}
	goalSeq int64 // goalUntil has read goal updates up to here
	mu      sync.Mutex
	now     time.Time
}

func newFakeAgentSession(t *testing.T, history ...acp.LogEntry) (*ConversationSession, *fakeAgent) {
	t.Helper()
	log := acp.NewEventLog(0)
	for _, entry := range history {
		log.Append(entry.Kind, entry.TS, entry.Payload)
	}
	agent := &fakeAgent{log: log, prompts: make(chan acp.PromptInput, 16), turns: make(chan fakeTurn, 16),
		cancels: make(chan struct{}, 16), now: time.UnixMilli(1_000_000)}
	broker := acp.NewBroker(func(_ context.Context, input acp.PromptInput) error {
		log.Append("prompt", 1, promptLogPayload(input))
		agent.prompts <- input
		turn := <-agent.turns
		agent.mu.Lock()
		agent.now = agent.now.Add(turn.advance)
		agent.mu.Unlock()
		for _, entry := range turn.entries {
			log.Append(entry.Kind, 1, entry.Payload)
		}
		log.Append("turn_end", 1, mustRawJSON(map[string]string{"stopReason": cmp.Or(turn.stop, "end_turn")}))
		return nil
	}, func(context.Context) error {
		agent.cancels <- struct{}{}
		return nil
	}, nil, nil)
	session := NewConversationSession(log, broker)
	session.now = func() time.Time {
		agent.mu.Lock()
		defer agent.mu.Unlock()
		return agent.now
	}
	return session, agent
}

func (agent *fakeAgent) nextPrompt(t *testing.T) acp.PromptInput {
	t.Helper()
	select {
	case input := <-agent.prompts:
		return input
	case <-time.After(2 * time.Second):
		t.Fatal("no prompt was sent")
		return acp.PromptInput{}
	}
}

// goalUntil returns the first logged goal for which done is true; the row's goal is the last one (conversationFold).
func (agent *fakeAgent) goalUntil(t *testing.T, done func(*Goal) bool) *Goal {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		changed := agent.log.Changed()
		entries, _, _ := agent.log.ReadSince(agent.goalSeq)
		for _, entry := range entries {
			agent.goalSeq = entry.Seq
			if entry.Kind != "goal_update" {
				continue
			}
			goal := &Goal{}
			if err := json.Unmarshal(entry.Payload, goal); err != nil {
				t.Fatal(err)
			}
			if goal.State == "cleared" {
				goal = nil
			}
			if done(goal) {
				return goal
			}
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("the goal never reached the expected state")
			return nil
		}
	}
}

func goalIn(state string) func(*Goal) bool {
	return func(goal *Goal) bool { return goal != nil && goal.State == state }
}

func entry(kind string, payload any) acp.LogEntry {
	return acp.LogEntry{Kind: kind, TS: 1, Payload: mustRawJSON(payload)}
}

func reply(text string, tool bool) fakeTurn {
	turn := fakeTurn{entries: []acp.LogEntry{entry("agent_message_chunk", map[string]any{"text": map[string]string{"text": text}})}}
	if tool {
		turn.entries = append(turn.entries, entry("tool_call", map[string]string{"toolCallId": "t"}))
	}
	return turn
}

func logKinds(log *acp.EventLog) []string {
	var kinds []string
	for _, entry := range log.Snapshot() {
		kinds = append(kinds, entry.Kind)
	}
	return kinds
}

func TestQueueSendsInOrderSteerJumpsAheadAndEditsApply(t *testing.T) {
	session, agent := newFakeAgentSession(t)
	if queued, first, err := session.Submit(t.Context(), acp.PromptInput{Text: "A"}, false); err != nil || queued != "" || first == nil {
		t.Fatalf("idle prompt was not sent: %q %v", queued, err)
	}
	agent.nextPrompt(t)
	b, _, err := session.Submit(t.Context(), acp.PromptInput{Text: "B"}, false)
	if err != nil || b == "" {
		t.Fatalf("busy prompt was not queued: %q %v", b, err)
	}
	c, _, _ := session.Submit(t.Context(), acp.PromptInput{Text: "C"}, true)
	select {
	case <-agent.cancels:
	case <-time.After(2 * time.Second):
		t.Fatal("steer did not interrupt the turn")
	}
	d, _, _ := session.Submit(t.Context(), acp.PromptInput{Text: "D"}, false)
	e, _, _ := session.Submit(t.Context(), acp.PromptInput{Text: "E"}, false)
	if err := session.EditQueued(d, "D2"); err != nil {
		t.Fatal(err)
	}
	if err := session.RemoveQueued(e); err != nil {
		t.Fatal(err)
	}
	if err := session.RemoveQueued(e); !errors.Is(err, errNotQueued) {
		t.Fatalf("removing twice = %v", err)
	}
	agent.turns <- fakeTurn{stop: "cancelled"}
	for _, want := range []string{"C", "B", "D2"} {
		if got := agent.nextPrompt(t); got.Text != want {
			t.Fatalf("sent %q, want %q", got.Text, want)
		}
		agent.turns <- fakeTurn{}
	}
	entries := agent.log.Snapshot()
	var steered, notice bool
	for i, item := range entries {
		var payload map[string]any
		_ = json.Unmarshal(item.Payload, &payload)
		switch item.Kind {
		case "queued_prompt":
			steered = steered || payload["queueId"] == c && payload["steer"] == true
		case "notice":
			notice = notice || payload["title"] == "Interrupted to steer"
		case "dequeued_prompt":
			if payload["reason"] == "sent" && (i+1 >= len(entries) || entries[i+1].Kind != "prompt") {
				t.Fatalf("sent dequeue is not followed by its prompt: %v", logKinds(agent.log))
			}
			if payload["queueId"] == e && payload["reason"] != "removed" {
				t.Fatalf("removed prompt logged as %v", payload)
			}
		}
	}
	if !steered || !notice {
		t.Fatalf("steer not logged: %v", logKinds(agent.log))
	}
}

func TestQueuedPromptsSurviveAWorkerRestart(t *testing.T) {
	images := acp.PromptInput{Text: "look", Images: []acp.PromptImage{{Sha: strings.Repeat("a", 64), Name: "x.png", Kind: "image", Width: 3, Height: 4}}}
	decoded, err := promptInputFromLog(queuedPromptPayload(queuedPrompt{ID: "q", Input: images}))
	if err != nil || decoded.Text != "look" || len(decoded.Images) != 1 || decoded.Images[0].Sha != images.Images[0].Sha || decoded.Images[0].Width != 3 {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}
	history := []acp.LogEntry{
		{Kind: "queued_prompt", TS: 1, Payload: queuedPromptPayload(queuedPrompt{ID: "one", Input: acp.PromptInput{Text: "one"}})},
		{Kind: "queued_prompt", TS: 1, Payload: queuedPromptPayload(queuedPrompt{ID: "two", Input: acp.PromptInput{Text: "two"}})},
		{Kind: "dequeued_prompt", TS: 1, Payload: mustRawJSON(map[string]string{"queueId": "one", "reason": "sent"})},
	}
	session, agent := newFakeAgentSession(t, history...)
	session.ResumeTurns()
	if got := agent.nextPrompt(t); got.Text != "two" {
		t.Fatalf("restarted worker sent %q", got.Text)
	}
	agent.turns <- fakeTurn{}
}

func TestQueueRoutes(t *testing.T) {
	previous := promptAcceptWait
	promptAcceptWait = time.Millisecond // the first turn is still running when the request returns
	t.Cleanup(func() { promptAcceptWait = previous })
	session, agent := newFakeAgentSession(t)
	worker := &WorkerHTTP{conversation: session}
	mux := http.NewServeMux()
	worker.registerConversation(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
		return recorder
	}
	if response := call("POST", "/conversation/prompt", `{"text":"first"}`); response.Code != 200 || strings.Contains(response.Body.String(), "queued") {
		t.Fatalf("first prompt = %d %s", response.Code, response.Body)
	}
	agent.nextPrompt(t)
	response := call("POST", "/conversation/prompt", `{"text":"second"}`)
	var accepted struct {
		Queued string `json:"queued"`
	}
	if json.Unmarshal(response.Body.Bytes(), &accepted); accepted.Queued == "" {
		t.Fatalf("second prompt = %d %s", response.Code, response.Body)
	}
	if response := call("PATCH", "/conversation/queue/"+accepted.Queued, `{"text":"edited"}`); response.Code != 200 {
		t.Fatalf("edit = %d", response.Code)
	}
	if response := call("POST", "/conversation/queue/"+accepted.Queued+"/steer", ``); response.Code != 200 {
		t.Fatalf("steer = %d", response.Code)
	}
	if response := call("DELETE", "/conversation/queue/missing", ``); response.Code != 404 {
		t.Fatalf("delete missing = %d", response.Code)
	}
	if response := call("PATCH", "/conversation/goal", `{"action":"pause"}`); response.Code != 404 {
		t.Fatalf("pause without goal = %d", response.Code)
	}
	agent.turns <- fakeTurn{stop: "cancelled"}
	if got := agent.nextPrompt(t); got.Text != "edited" {
		t.Fatalf("steered prompt = %q", got.Text)
	}
	agent.turns <- fakeTurn{}
}

func TestClientPromptIDIsAcceptedOnceAcrossReplay(t *testing.T) {
	input := acp.PromptInput{Text: "one message", QueueID: "eab4f2c19b624b48ae06bbbc5e107d17"}
	session, agent := newFakeAgentSession(t)
	for range 2 {
		id, _, err := session.Submit(t.Context(), input, false)
		if err != nil || id != input.QueueID {
			t.Fatalf("submit = %q, %v", id, err)
		}
	}
	if got := agent.nextPrompt(t); got.Text != input.Text || got.QueueID != input.QueueID {
		t.Fatalf("prompt = %+v, want %+v", got, input)
	}
	agent.turns <- fakeTurn{}
	restarted, next := newFakeAgentSession(t, agent.log.Snapshot()...)
	if id, _, err := restarted.Submit(t.Context(), input, false); err != nil || id != input.QueueID {
		t.Fatalf("retry = %q, %v", id, err)
	}
	if len(restarted.queue) != 0 || len(next.prompts) != 0 {
		t.Fatal("replayed prompt was queued or sent twice")
	}
}

func TestClientPromptLookupSurvivesADroppedReplayTail(t *testing.T) {
	store := testSessionStore(t, t.TempDir())
	id := "session-stored-intent"
	if err := upsertSession(t.Context(), store.DB, SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	input := acp.PromptInput{Text: "first message", QueueID: "5a6057ea65564edfb5222d0c394e0b43"}
	logged := entry("prompt", json.RawMessage(promptLogPayload(input)))
	logged.Seq = 1
	if err := persistConversationChangesSince(t.Context(), store.DB, id, acp.LogChanges{Entries: []acp.LogEntry{logged}, LastSeq: 1}, 0, false); err != nil {
		t.Fatal(err)
	}
	session, agent := newFakeAgentSession(t)
	session.promptStored = func(ctx context.Context, key string) (bool, error) {
		return storedPromptID(ctx, store, id, key)
	}
	if id, _, err := session.Submit(t.Context(), input, false); err != nil || id != input.QueueID || len(agent.log.Snapshot()) != 0 {
		t.Fatalf("retry of an old prompt wrote a new event: id=%q err=%v log=%v", id, err, logKinds(agent.log))
	}
}

// A restart for when the chat is idle waits until the queue has run.
func TestIdleRestartWaitsForTheQueue(t *testing.T) {
	session, agent := newFakeAgentSession(t)
	ctx := context.Background()
	if _, _, err := session.Submit(ctx, acp.PromptInput{Text: "first"}, false); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	if _, _, err := session.Submit(ctx, acp.PromptInput{QueueID: "q1", Text: "second"}, false); err != nil {
		t.Fatal(err)
	}
	restarted := make(chan struct{}, 1)
	if !session.whenIdle(func() { restarted <- struct{}{} }) {
		t.Fatal("the chat is working")
	}
	agent.turns <- fakeTurn{}
	if input := agent.nextPrompt(t); input.Text != "second" {
		t.Fatalf("next = %q", input.Text)
	}
	select {
	case <-restarted:
		t.Fatal("the restart ran between the turn and the queued prompt")
	default:
	}
	agent.turns <- fakeTurn{}
	select {
	case <-restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("the restart never ran")
	}
}
