package network

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestWantedModelAppliesBeforeQueuedPrompt(t *testing.T) {
	ctx := t.Context()
	store := testSessionStore(t, t.TempDir())
	info := SessionInfo{ID: "session-saved-model", Harness: "claude", Directory: "/project", Transport: "acp", ConversationState: "starting"}
	if err := upsertSession(ctx, store.DB, info); err != nil {
		t.Fatal(err)
	}
	if err := saveWantedConfig(ctx, store.DB, info.ID, configSelection{Key: "model", Value: json.RawMessage(`"opus"`)}, true); err != nil {
		t.Fatal(err)
	}
	if err := saveWantedConfig(ctx, store.DB, info.ID, configSelection{Key: "effort", Value: json.RawMessage(`"missing"`)}, true); err != nil {
		t.Fatal(err)
	}
	log := acp.NewEventLog(0)
	log.Append("queued_prompt", 1, queuedPromptPayload(queuedPrompt{ID: "queue", Input: acp.PromptInput{Text: "check the budgets"}}))
	applied := make(chan string, 2)
	broker := acp.NewBroker(func(_ context.Context, input acp.PromptInput) error {
		applied <- input.Text
		return nil
	}, nil, nil, func(_ context.Context, key, value string) error {
		applied <- key + "=" + value
		return nil
	})
	conv := NewConversationSession(log, broker)
	conv.SetConfigOptions(json.RawMessage(`[
		{"id":"model","type":"select","currentValue":"sonnet","options":[{"value":"sonnet"},{"value":"opus"}]},
		{"id":"effort","type":"select","currentValue":"low","options":[{"value":"low"}]}
	]`))
	wanted, err := savedConfig(ctx, store, info)
	if err != nil {
		t.Fatal(err)
	}
	conv.applyWantedConfig(ctx, wanted)
	conv.ResumeTurns()
	for _, want := range []string{`model="opus"`, "check the budgets"} {
		select {
		case got := <-applied:
			if got != want {
				t.Fatalf("startup action %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if !slices.Contains(logKinds(log), "notice") {
		t.Fatal("missing effort level did not leave a notice")
	}
}
