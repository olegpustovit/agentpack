package network

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
)

// cyclingAgent keeps one turn open across model cycles, as Claude Code does under a native /goal.
// Like claude-agent-acp it accepts prompts during that turn and answers each one; the chat's
// turn ends with the last answer.
type cyclingAgent struct {
	log      *acp.EventLog
	prompts  chan string
	release  chan struct{}
	mu       sync.Mutex
	inFlight int
}

func newCyclingSession(t *testing.T, queuesPrompts bool) (*ConversationSession, *cyclingAgent) {
	t.Helper()
	log := acp.NewEventLog(0)
	agent := &cyclingAgent{log: log, prompts: make(chan string, 16), release: make(chan struct{})}
	broker := acp.NewBroker(func(ctx context.Context, input acp.PromptInput) error {
		log.Append("prompt", 1, PromptLogPayload(input))
		acp.Accepted(ctx)
		agent.mu.Lock()
		agent.inFlight++
		agent.mu.Unlock()
		agent.prompts <- input.Text
		<-agent.release
		agent.mu.Lock()
		agent.inFlight--
		last := agent.inFlight == 0
		agent.mu.Unlock()
		if last {
			log.Append("turn_end", 1, MustRawJSON(map[string]string{"stopReason": "end_turn"}))
		}
		return nil
	}, func(context.Context) error { return nil }, nil, nil)
	session := NewConversationSession(log, broker)
	session.queuesPrompts = queuesPrompts
	return session, agent
}

func (agent *cyclingAgent) next(t *testing.T) string {
	t.Helper()
	select {
	case text := <-agent.prompts:
		return text
	case <-time.After(2 * time.Second):
		t.Fatal("no prompt reached the agent")
		return ""
	}
}

func (agent *cyclingAgent) cycleEnds() {
	agent.log.Append("usage_update", 1, MustRawJSON(map[string]any{"used": 10, "cost": map[string]any{"amount": 0.1, "currency": "USD"}}))
}

func queuedIDs(session *ConversationSession) []string {
	session.mu.Lock()
	defer session.mu.Unlock()
	var ids []string
	for _, item := range session.Queue {
		ids = append(ids, item.ID)
	}
	return ids
}

func messageFromAgent(id, text string) acp.PromptInput {
	return acp.PromptInput{QueueID: id, Text: text, From: &acp.PromptFrom{Session: "other", Task: "t1", Kind: "ask"}}
}

func TestPromptsReachAnAgentDuringItsLongTurn(t *testing.T) {
	session, agent := newCyclingSession(t, true)
	ctx := context.Background()
	if _, _, err := session.Submit(ctx, acp.PromptInput{Text: "/goal ship it"}, false); err != nil {
		t.Fatal(err)
	}
	if got := agent.next(t); got != "/goal ship it" {
		t.Fatalf("first prompt = %q", got)
	}
	// An agent message goes in at once, while the goal's turn still runs.
	if _, _, err := session.Submit(ctx, messageFromAgent("ask-1", "question from another chat"), false); err != nil {
		t.Fatal(err)
	}
	if got := agent.next(t); got != "question from another chat" {
		t.Fatalf("agent message = %q", got)
	}
	// A user prompt waits for the end of the model cycle and stays editable until then.
	for _, id := range []string{"user-1", "user-2"} {
		if _, _, err := session.Submit(ctx, acp.PromptInput{QueueID: id, Text: id}, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := queuedIDs(session); !slices.Equal(got, []string{"user-1", "user-2"}) {
		t.Fatalf("queue before the cycle ends = %v", got)
	}
	if err := session.EditQueued("user-1", "edited"); err != nil {
		t.Fatal(err)
	}
	agent.cycleEnds()
	if got := agent.next(t); got != "edited" {
		t.Fatalf("prompt at the cycle end = %q, want the edited text", got)
	}
	if got := queuedIDs(session); !slices.Equal(got, []string{"user-2"}) {
		t.Fatalf("one user prompt goes per cycle; queue = %v", got)
	}
	agent.cycleEnds()
	if got := agent.next(t); got != "user-2" {
		t.Fatalf("prompt at the second cycle end = %q", got)
	}
	close(agent.release)
	waitConversationState(t, session, "idle")
	turnEnds := 0
	for _, kind := range logKinds(agent.log) {
		if kind == "turn_end" {
			turnEnds++
		}
	}
	if turnEnds != 1 {
		t.Fatalf("turn_end entries = %d, want 1: %v", turnEnds, logKinds(agent.log))
	}
	for _, entry := range agent.log.Snapshot() {
		if entry.Kind == "dequeued_prompt" {
			t.Fatalf("a delivered prompt is dequeued by its prompt entry, not %s", entry.Payload)
		}
	}
}

func TestPromptsWaitForTheTurnEndWhenTheAgentCannotQueue(t *testing.T) {
	session, agent := newCyclingSession(t, false)
	ctx := context.Background()
	if _, _, err := session.Submit(ctx, acp.PromptInput{Text: "/goal ship it"}, false); err != nil {
		t.Fatal(err)
	}
	agent.next(t)
	if _, _, err := session.Submit(ctx, messageFromAgent("ask-1", "question"), false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.Submit(ctx, acp.PromptInput{QueueID: "user-1", Text: "next"}, false); err != nil {
		t.Fatal(err)
	}
	agent.cycleEnds()
	if got := queuedIDs(session); !slices.Equal(got, []string{"ask-1", "user-1"}) {
		t.Fatalf("queue during the turn = %v", got)
	}
	close(agent.release)
	if got := agent.next(t); got != "question" {
		t.Fatalf("after the turn = %q", got)
	}
	if got := agent.next(t); got != "next" {
		t.Fatalf("then = %q", got)
	}
}

func TestSteerHandsThePromptToTheRunningTurn(t *testing.T) {
	log := acp.NewEventLog(0)
	release, cancels := make(chan struct{}), 0
	broker := acp.NewBroker(func(ctx context.Context, input acp.PromptInput) error {
		log.Append("prompt", 1, PromptLogPayload(input))
		acp.Accepted(ctx)
		<-release
		log.Append("turn_end", 1, MustRawJSON(map[string]string{"stopReason": "end_turn"}))
		return nil
	}, func(context.Context) error { cancels++; return nil }, nil, nil)
	session := NewConversationSession(log, broker)
	var steered []string
	session.steer = func(_ context.Context, input acp.PromptInput) (bool, error) {
		steered = append(steered, input.Text)
		log.Append("prompt", 1, PromptLogPayload(input))
		return true, nil
	}
	ctx := context.Background()
	if _, _, err := session.Submit(ctx, acp.PromptInput{Text: "/goal ship it"}, false); err != nil {
		t.Fatal(err)
	}
	waitConversationState(t, session, "working")
	if _, _, err := session.Submit(ctx, acp.PromptInput{QueueID: "s1", Text: "look here"}, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.Submit(ctx, acp.PromptInput{QueueID: "q1", Text: "queued"}, false); err != nil {
		t.Fatal(err)
	}
	if err := session.SteerQueued(ctx, "q1"); err != nil {
		t.Fatal(err)
	}
	// An agent message steers in on its own, without waiting for the turn to end.
	if _, _, err := session.Submit(ctx, acp.PromptInput{QueueID: "a1", Text: "from agent", From: &acp.PromptFrom{Session: "session-a", Task: "t_0123456789abcdef", Kind: "ask"}}, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(steered, []string{"look here", "queued", "from agent"}) || cancels != 0 || len(queuedIDs(session)) != 0 {
		t.Fatalf("steered %v with %d cancels, queue %v", steered, cancels, queuedIDs(session))
	}
	close(release)
	waitConversationState(t, session, "idle")
}

func TestSteeringDoesNotWaitForHistorySave(t *testing.T) {
	conv, agent := newFakeAgentSession(t)
	steered := make(chan string, 1)
	conv.steer = func(_ context.Context, input acp.PromptInput) (bool, error) {
		agent.log.Append("prompt", 1, PromptLogPayload(input))
		steered <- input.Text
		return true, nil
	}
	if _, _, err := conv.Submit(t.Context(), acp.PromptInput{Text: "running"}, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.turns <- fakeTurn{} })
	agent.nextPrompt(t)

	blocked := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	var firstWrite sync.Once
	if err := agent.log.EnableSink(func(changes acp.LogChanges) error {
		for _, entry := range changes.Entries {
			if entry.Kind == "queued_prompt" {
				firstWrite.Do(func() { close(blocked) })
				<-release
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unblock()
		_ = agent.log.Close()
	})
	done := make(chan error, 1)
	go func() {
		_, _, err := conv.Submit(t.Context(), acp.PromptInput{QueueID: "steer-save", Text: "look here"}, true)
		done <- err
	}()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("history write did not start")
	}
	select {
	case got := <-steered:
		if got != "look here" {
			t.Fatalf("steered %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("steering waited for the blocked history write")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// While a permission or question is open nothing is steered into the turn: a plain prompt
// queues, a person's Send now cancels the turn and goes next, an agent's message waits.
func TestOpenRequestHoldsPrompts(t *testing.T) {
	for _, request := range []acp.LogEntry{
		entry("permission_request", map[string]string{"requestId": "p1"}),
		entry("elicitation", map[string]string{"elicitationId": "e1"}),
	} {
		for _, test := range []struct {
			name      string
			send      func(*testing.T, *ConversationSession) error
			dismisses bool
		}{
			{name: "enter", send: func(t *testing.T, conv *ConversationSession) error {
				_, _, err := conv.Submit(t.Context(), acp.PromptInput{QueueID: "q", Text: "look here"}, false)
				return err
			}},
			{name: "send now", dismisses: true, send: func(t *testing.T, conv *ConversationSession) error {
				_, _, err := conv.Submit(t.Context(), acp.PromptInput{QueueID: "q", Text: "look here"}, true)
				return err
			}},
			{name: "queued chip", dismisses: true, send: func(t *testing.T, conv *ConversationSession) error {
				if _, _, err := conv.Submit(t.Context(), acp.PromptInput{QueueID: "q", Text: "look here"}, false); err != nil {
					return err
				}
				return conv.SteerQueued(t.Context(), "q")
			}},
			{name: "agent message", send: func(t *testing.T, conv *ConversationSession) error {
				_, _, err := conv.Submit(t.Context(), messageFromAgent("q", "look here"), true)
				return err
			}},
			{name: "goal pause", send: func(t *testing.T, conv *ConversationSession) error {
				conv.pauseNativeGoal(t.Context())
				return nil
			}},
		} {
			t.Run(request.Kind+"/"+test.name, func(t *testing.T) {
				log := acp.NewEventLog(0)
				prompts, end := make(chan string, 4), make(chan string, 4)
				var cancels atomic.Int32
				broker := acp.NewBroker(func(ctx context.Context, input acp.PromptInput) error {
					log.Append("prompt", 1, PromptLogPayload(input))
					if input.Text == "first" {
						log.Append(request.Kind, 1, request.Payload)
					}
					acp.Accepted(ctx)
					prompts <- input.Text
					log.Append("turn_end", 1, MustRawJSON(map[string]string{"stopReason": <-end}))
					return nil
				}, func(context.Context) error { cancels.Add(1); end <- "cancelled"; return nil }, nil, nil)
				conv := NewConversationSession(log, broker)
				var steered atomic.Int32
				conv.steer = func(context.Context, acp.PromptInput) (bool, error) { steered.Add(1); return false, nil }
				next := func() string {
					select {
					case text := <-prompts:
						return text
					case <-time.After(2 * time.Second):
						t.Fatal("no prompt reached the agent")
						return ""
					}
				}
				if _, _, err := conv.Submit(t.Context(), acp.PromptInput{Text: "first"}, false); err != nil {
					t.Fatal(err)
				}
				next()
				waitConversationState(t, conv, "needs_you")
				if err := test.send(t, conv); err != nil {
					t.Fatal(err)
				}
				if steered.Load() != 0 {
					t.Fatal("a prompt was steered into a held turn")
				}
				if !test.dismisses {
					if cancels.Load() != 0 || len(queuedIDs(conv)) != 1 {
						t.Fatalf("cancels %d, queue %v", cancels.Load(), queuedIDs(conv))
					}
					end <- "cancelled" // let the agent go
				}
				if got := next(); got != "look here" && got != "/goal clear" {
					t.Fatalf("next prompt = %q", got)
				}
				end <- "end_turn"
				waitConversationState(t, conv, "idle")
				waitReleased(conv)
				if test.dismisses && cancels.Load() != 1 {
					t.Fatalf("cancels = %d", cancels.Load())
				}
				if len(prompts) != 0 || len(queuedIDs(conv)) != 0 {
					t.Fatalf("sent twice or stuck: %d more, queue %v", len(prompts), queuedIDs(conv))
				}
			})
		}
	}
}

// A request that outlives its turn (an MCP sign-in link) has no turn to cancel: messages queue
// until it is resolved, whatever key sent them.
func TestRequestLeftOpenAfterItsTurnQueuesPrompts(t *testing.T) {
	conv, agent := newFakeAgentSession(t)
	agent.log.Append("elicitation", 1, MustRawJSON(map[string]string{"elicitationId": "e1", "agentElicitationId": "a1"}))
	queued, first, err := conv.Submit(t.Context(), acp.PromptInput{Text: "go on"}, true)
	if err != nil || queued == "" || first != nil || len(agent.cancels) != 0 || len(agent.prompts) != 0 {
		t.Fatalf("queued %q, %d cancels, %v", queued, len(agent.cancels), err)
	}
	agent.log.Append("elicitation_result", 1, MustRawJSON(map[string]string{"elicitationId": "e1"}))
	if prompt := agent.nextPrompt(t); prompt.Text != "go on" {
		t.Fatalf("sent %q", prompt.Text)
	}
	agent.turns <- fakeTurn{}
}

// Claude takes agent messages in during its turn and holds them behind the open request itself.
func TestOpenRequestStillDeliversAgentMessagesToClaude(t *testing.T) {
	conv, agent := newCyclingSession(t, true)
	conv.steer = func(context.Context, acp.PromptInput) (bool, error) {
		t.Error("steered into a held turn")
		return false, nil
	}
	if _, _, err := conv.Submit(t.Context(), acp.PromptInput{Text: "work"}, false); err != nil {
		t.Fatal(err)
	}
	agent.next(t)
	agent.log.Append("permission_request", 1, MustRawJSON(map[string]string{"requestId": "p1"}))
	if _, _, err := conv.Submit(t.Context(), messageFromAgent("ask-1", "question"), true); err != nil {
		t.Fatal(err)
	}
	if got := agent.next(t); got != "question" {
		t.Fatalf("delivered %q", got)
	}
	agent.log.Append("permission_result", 1, MustRawJSON(map[string]string{"requestId": "p1"}))
	close(agent.release)
	waitConversationState(t, conv, "idle")
	waitReleased(conv)
	if len(agent.prompts) != 0 || len(queuedIDs(conv)) != 0 {
		t.Fatalf("delivered twice or stuck: queue %v", queuedIDs(conv))
	}
}
