package network

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func replyTurn(text string) fakeTurn {
	return fakeTurn{entries: []acp.LogEntry{entry("agent_message_chunk", map[string]any{"text": map[string]string{"text": text}})}}
}

// A turn that ends on the agent's connection error is retried after a delay; a reply that only
// mentions networking is not, and a turn sent meanwhile drops the pending retry.
func TestNetworkDropRetriesTheTurn(t *testing.T) {
	session, agent := newFakeAgentSession(t)
	scheduled := make(chan func(), 4)
	session.after = func(_ time.Duration, run func()) { scheduled <- run }
	if _, _, err := session.Submit(context.Background(), acp.PromptInput{Text: "fix it"}, false); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	agent.turns <- replyTurn("Working on it.\nAPI Error: Connection error.")
	retry := <-scheduled
	retry()
	if input := agent.nextPrompt(t); input.Text != networkRetryPrompt {
		t.Fatalf("retried with %q", input.Text)
	}
	agent.turns <- replyTurn("Fixed the ECONNRESET handling in client.go.")
	select {
	case <-scheduled:
		t.Fatal("a reply about networking was retried")
	case <-time.After(50 * time.Millisecond):
	}
	// A drop, then the person's own message before the retry is due.
	if _, _, err := session.Submit(context.Background(), acp.PromptInput{Text: "again"}, false); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	agent.turns <- replyTurn("stream disconnected before completion: error sending request")
	stale := <-scheduled
	if _, _, err := session.Submit(context.Background(), acp.PromptInput{Text: "mine"}, false); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	agent.turns <- replyTurn("done")
	stale()
	select {
	case input := <-agent.prompts:
		t.Fatalf("stale retry sent %q", input.Text)
	case <-time.After(50 * time.Millisecond):
	}
	var notices int
	for _, logged := range session.log.Snapshot() {
		if logged.Kind == "notice" && strings.Contains(string(logged.Payload), "Lost the connection") {
			notices++
		}
	}
	if notices != 2 {
		t.Fatalf("%d retry notices", notices)
	}
}

// A subagent's reply, usage and turn end belong to the subagent: its connection error neither
// retries the chat's turn nor changes the chat's usage or stop reason.
func TestSubagentOutputDoesNotDriveTheChat(t *testing.T) {
	session, agent := newFakeAgentSession(t)
	scheduled := make(chan func(), 4)
	session.after = func(_ time.Duration, run func()) { scheduled <- run }
	if _, _, err := session.Submit(context.Background(), acp.PromptInput{Text: "fix it"}, false); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	child := func(kind string, fields map[string]any) acp.LogEntry {
		fields["_subagent"] = "child-1"
		return entry(kind, fields)
	}
	agent.turns <- fakeTurn{entries: []acp.LogEntry{
		entry("agent_message_chunk", map[string]any{"text": map[string]string{"text": "Done."}}),
		child("agent_message_chunk", map[string]any{"text": map[string]string{"text": "\nAPI Error: Connection error."}}),
		child("usage_update", map[string]any{"used": 99, "cost": map[string]any{"amount": 9.0, "currency": "USD"}}),
	}}
	waitConversationState(t, session, "idle")
	select {
	case <-scheduled:
		t.Fatal("a subagent's connection error retried the chat's turn")
	case <-time.After(50 * time.Millisecond):
	}
	session.mu.Lock()
	used, cost, text := session.usageUsed, session.usageCost, string(session.turnText)
	session.mu.Unlock()
	if used != 0 || cost != 0 || text != "Done." {
		t.Fatalf("chat usage %d/%v and reply %q took the subagent's", used, cost, text)
	}
}
