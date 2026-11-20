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
