package network

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
	"github.com/olegpustovit/agentpack/internal/state/statetest"
)

func TestWantedModelAppliesBeforeQueuedPrompt(t *testing.T) {
	ctx := t.Context()
	store := statetest.Open(t, t.TempDir())
	info := session.SessionInfo{ID: "session-saved-model", Harness: "claude", Directory: "/project", Transport: "acp", ConversationState: "starting"}
	if err := session.UpsertSession(ctx, store.DB, info); err != nil {
		t.Fatal(err)
	}
	if err := SaveWantedConfig(ctx, store.DB, info.ID, ConfigSelection{Key: "model", Value: json.RawMessage(`"opus"`)}, true); err != nil {
		t.Fatal(err)
	}
	if err := SaveWantedConfig(ctx, store.DB, info.ID, ConfigSelection{Key: "effort", Value: json.RawMessage(`"missing"`)}, true); err != nil {
		t.Fatal(err)
	}
	log := acp.NewEventLog(0)
	log.Append("queued_prompt", 1, QueuedPromptPayload(QueuedPrompt{ID: "queue", Input: acp.PromptInput{Text: "check the budgets"}}))
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
	wanted, err := savedConfig(ctx, store, info, "")
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

func TestStartOptionsApplyBeforePromptUsingLiveOptionIDs(t *testing.T) {
	ctx := t.Context()
	store := statetest.Open(t, t.TempDir())
	info := session.SessionInfo{ID: "session-start-options", Harness: "codex", Directory: "/project", Transport: "acp", ConversationState: "starting"}
	if err := session.UpsertSession(ctx, store.DB, info); err != nil {
		t.Fatal(err)
	}
	if err := state.Transaction(ctx, store.DB, func(tx *sql.Tx) error {
		return SaveChatStartOptions(ctx, tx, info.ID, ChatStartOptions{Model: "sol", Effort: "max"})
	}); err != nil {
		t.Fatal(err)
	}

	log := acp.NewEventLog(0)
	log.Append("queued_prompt", 1, QueuedPromptPayload(QueuedPrompt{ID: "queue", Input: acp.PromptInput{Text: "go"}}))
	applied := make(chan string, 4)
	conv := NewConversationSession(log, acp.NewBroker(func(_ context.Context, input acp.PromptInput) error {
		applied <- input.Text
		return nil
	}, nil, nil, func(_ context.Context, key, value string) error {
		applied <- key + "=" + value
		if key == "model_choice" {
			log.Append("config_option_update", 2, json.RawMessage(`{"configOptions":[
				{"id":"model_choice","category":"model","type":"select","currentValue":"sol","options":[{"value":"sol"}]},
				{"id":"thinking","category":"thought_level","type":"select","currentValue":"high",
					"options":[{"group":"Available","options":[{"value":"high"},{"value":"max"}]}]}
			]}`))
		}
		return nil
	}))
	record := WorkerRecord{ID: info.ID, Session: info}
	directory := filepath.Join(t.TempDir(), info.ID)
	conv.starting = true
	if err := wireWorkerConversation(ctx, store, record, directory, nil, conv); err == nil || !strings.Contains(err.Error(), "selected model") {
		t.Fatalf("missing model accepted: %v", err)
	}
	other := info
	other.ID = "session-another-launch"
	if err := session.UpsertSession(ctx, store.DB, other); err != nil {
		t.Fatal(err)
	}
	if wanted, err := savedConfig(ctx, store, other, ""); err != nil || len(wanted) != 0 {
		t.Fatalf("another launch inherited the pending selection: %s, %v", wanted, err)
	}
	conv.SetConfigOptions(json.RawMessage(`[
		{"id":"model_choice","category":"model","type":"select","currentValue":"other","options":[{"value":"sol"}]}
	]`))
	if err := wireWorkerConversation(ctx, store, record, directory, nil, conv); err != nil {
		t.Fatal(err)
	}
	conv.ResumeTurns()
	for _, want := range []string{`model_choice="sol"`, `thinking="max"`, "go"} {
		select {
		case got := <-applied:
			if got != want {
				t.Fatalf("startup action %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	saved, err := savedConfig(ctx, store, info, "")
	if err != nil || len(saved) != 2 || string(saved["model_choice"]) != `"sol"` || string(saved["thinking"]) != `"max"` {
		t.Fatalf("saved options %s, %v", saved, err)
	}
}
