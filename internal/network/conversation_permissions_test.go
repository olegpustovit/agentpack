package network

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
	"github.com/olegpustovit/agentpack/internal/state/statetest"
)

func TestWorkerPermissionDefaults(t *testing.T) {
	for _, test := range []struct {
		name, harness, parent, ownMode, wantMode string
		all, helpers                             bool
	}{
		{name: "helper policy skips top-level chats", harness: "agent", helpers: true, wantMode: "agent"},
		{name: "helpers ignore inherited planning mode", harness: "agent", parent: "session-parent", helpers: true, wantMode: "bypassPermissions"},
		{name: "explicit per-chat mode wins", harness: "agent", parent: "session-parent", all: true, ownMode: "plan", wantMode: "plan"},
		{name: "general default works for Cursor", harness: "agent", all: true, wantMode: "bypassPermissions"},
		{name: "Claude uses native bypass", harness: "claude", all: true, wantMode: "bypassPermissions"},
		{name: "Codex uses native full access", harness: "codex", all: true, wantMode: "agent-full-access"},
		{name: "helper without any mode uses client-side approval", harness: "grok", parent: "session-parent", helpers: true, wantMode: "bypassPermissions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			store := statetest.Open(t, t.TempDir())
			if _, err := store.DB.Exec(`INSERT INTO agent_defaults(key,value) VALUES ('bypass_permissions',?),('auto_approve_helpers',?)`, test.all, test.helpers); err != nil {
				t.Fatal(err)
			}
			remembered := "plan"
			bypass := test.all || test.helpers && test.parent != ""
			if !bypass {
				remembered = "agent"
			}
			if err := store.Mesh().SetSetting(ctx, permissionSettingKey(test.harness), remembered); err != nil {
				t.Fatal(err)
			}
			info := session.SessionInfo{ID: "session-current", Harness: test.harness, Directory: "/project", Transport: "acp", ParentID: test.parent}
			previous := info
			previous.ID = "session-previous"
			for _, sessionInfo := range []session.SessionInfo{info, previous} {
				if err := session.UpsertSession(ctx, store.DB, sessionInfo); err != nil {
					t.Fatal(err)
				}
			}
			if bypass {
				if err := SaveWantedConfig(ctx, store.DB, previous.ID, ConfigSelection{Key: "mode", Value: json.RawMessage(`"plan"`)}, false); err != nil {
					t.Fatal(err)
				}
			}
			if test.ownMode != "" {
				if err := SaveWantedConfig(ctx, store.DB, info.ID, ConfigSelection{Key: "mode", Value: MustRawJSON(test.ownMode)}, false); err != nil {
					t.Fatal(err)
				}
			}

			log := acp.NewEventLog(0)
			var nativeCalls []string
			conv := NewConversationSession(log, acp.NewBroker(nil, nil, nil, func(_ context.Context, key, value string) error {
				nativeCalls = append(nativeCalls, value)
				if key == "mode" {
					log.Append("current_mode_update", 1, MustRawJSON(map[string]json.RawMessage{"currentModeId": json.RawMessage(value)}))
				}
				return nil
			}))
			if test.harness != "grok" {
				choices := []map[string]string{{"value": "agent"}, {"value": "plan"}}
				switch test.harness {
				case "claude":
					choices = append(choices, map[string]string{"value": "bypassPermissions"})
				case "codex":
					choices = append(choices, map[string]string{"value": "agent-full-access"})
				}
				conv.SetConfigOptions(MustRawJSON([]map[string]any{{
					"id": "mode", "category": "mode", "type": "select", "currentValue": "agent", "options": choices,
				}}))
			}
			wireWorkerConversation(ctx, store, WorkerRecord{ID: info.ID, Session: info}, filepath.Join(t.TempDir(), info.ID), nil, conv)
			if _, got := conv.permissionMode(); got != test.wantMode {
				t.Fatalf("mode = %q, want %q (native mode requests: %q)", got, test.wantMode, nativeCalls)
			}
			if test.wantMode == "agent" || test.wantMode == "plan" {
				return
			}
			client := acp.NewHarnessClient("", log, conv.broker)
			client.AutoApprove = conv.BypassingPermissions
			requestCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancel()
			response, err := client.RequestPermission(requestCtx, sdkacp.RequestPermissionRequest{
				SessionId: "cursor-subagent",
				ToolCall:  sdkacp.ToolCallUpdate{ToolCallId: "tool"},
				Options:   []sdkacp.PermissionOption{{OptionId: "once", Kind: sdkacp.PermissionOptionKindAllowOnce}},
			})
			if err != nil || response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "once" || conv.ConversationState() == string(StateNeedsYou) {
				t.Fatalf("permission did not auto-approve: %+v, %v", response, err)
			}
		})
	}
}

func TestSwitchingToBypassApprovesAnAlreadyWaitingSubagent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	log := acp.NewEventLog(0)
	broker := acp.NewBroker(nil, nil, nil, nil)
	conv := NewConversationSession(log, broker)
	conv.clientMode = "default"
	client := acp.NewHarnessClient("", log, broker)
	client.AutoApprove = conv.BypassingPermissions
	acp.AppendRawSessionUpdate(log, []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"parent","update":{"sessionUpdate":"subagent_update","sessionId":"child"}}}`))
	ready := log.Changed()
	done := make(chan error, 1)
	go func() {
		response, err := client.RequestPermission(ctx, sdkacp.RequestPermissionRequest{
			SessionId: "child",
			ToolCall:  sdkacp.ToolCallUpdate{ToolCallId: "child_tool"},
			Options: []sdkacp.PermissionOption{
				{OptionId: "always", Kind: sdkacp.PermissionOptionKindAllowAlways},
				{OptionId: "once", Kind: sdkacp.PermissionOptionKindAllowOnce},
			},
		})
		if err == nil && (response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "once") {
			err = errors.New("expected the one-time permission to be approved")
		}
		done <- err
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := conv.SetConfigOption(ctx, "mode", json.RawMessage(`"bypassPermissions"`)); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if conv.ConversationState() == string(StateNeedsYou) {
		t.Fatal("the subagent still needs a permission answer")
	}
}

// rememberedPermissions wires conv to a state store and reads the mode it remembers for Claude.
func rememberedPermissions(t *testing.T, conv *ConversationSession) func() string {
	store := statetest.Open(t, t.TempDir())
	conv.wireStore(t.Context(), store, session.SessionInfo{Harness: "claude"}, t.TempDir())
	return func() string {
		mode, _ := store.Mesh().Setting(context.Background(), "permission_mode.claude")
		return mode
	}
}

func TestPermissionModeIsRememberedAndAppliedToTheNextChat(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var sets []string
	broker := acp.NewBroker(nil, nil, nil, func(_ context.Context, key, value string) error {
		sets = append(sets, key+"="+value)
		return nil
	})
	options := []byte(`[{"id":"mode","category":"mode","type":"select","currentValue":"default"},{"id":"model","category":"model","type":"select","currentValue":"opus"}]`)
	first := NewConversationSession(acp.NewEventLog(0), broker)
	first.SetConfigOptions(options)
	first.wireStore(t.Context(), store, session.SessionInfo{Harness: "claude"}, t.TempDir())
	ctx := context.Background()
	if err := first.SetConfigOption(ctx, "model", []byte(`"sonnet"`)); err != nil {
		t.Fatal(err)
	}
	if err := first.SetConfigOption(ctx, "mode", []byte(`"bypassPermissions"`)); err != nil {
		t.Fatal(err)
	}
	next := NewConversationSession(acp.NewEventLog(0), broker)
	next.SetConfigOptions(options)
	next.applyRememberedPermissions(ctx, store, "claude")
	next.applyRememberedPermissions(ctx, store, "codex") // nothing remembered for codex
	if want := []string{`model="sonnet"`, `mode="bypassPermissions"`, `mode="bypassPermissions"`}; len(sets) != 3 || sets[0] != want[0] || sets[1] != want[1] || sets[2] != want[2] {
		t.Fatalf("config sets %q", sets)
	}
}

func TestAgentWithoutModesGetsAgentpackPermissions(t *testing.T) {
	broker := acp.NewBroker(nil, nil, nil, func(context.Context, string, string) error {
		t.Fatal("agentpack's own mode reached the agent")
		return nil
	})
	session := NewConversationSession(acp.NewEventLog(0), broker)
	session.SetConfigOptions([]byte(`[{"id":"model","category":"model","type":"select","currentValue":"gemini"}]`))
	session.clientMode = "default"
	remembered := rememberedPermissions(t, session)
	if err := session.SetConfigOption(context.Background(), "mode", []byte(`"bypassPermissions"`)); err != nil {
		t.Fatal(err)
	}
	if !session.BypassingPermissions() || remembered() != "bypassPermissions" {
		t.Fatalf("bypassing %v, remembered %q", session.BypassingPermissions(), remembered())
	}
	if config := string(session.ConfigJSON()); !strings.Contains(config, `"model"`) || !strings.Contains(config, `"currentValue":"bypassPermissions"`) {
		t.Fatalf("config %s", config)
	}
	if err := session.SetConfigOption(context.Background(), "mode", []byte(`"plan"`)); err == nil {
		t.Fatal("accepted a mode agentpack does not offer")
	}
}

func TestAgentpackBypassExtendsNativePermissionModes(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "config options"
		if legacy {
			name = "legacy modes"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			var calls []string
			broker := acp.NewBroker(nil, nil, nil, func(_ context.Context, key, value string) error {
				calls = append(calls, key+"="+value)
				return nil
			})
			session := NewConversationSession(acp.NewEventLog(0), broker)
			session.clientMode = "default"
			key := "mode"
			if legacy {
				session.SetModes([]byte(`{"currentModeId":"auto","availableModes":[{"id":"auto","name":"Auto"},{"id":"plan","name":"Plan"}]}`))
			} else {
				key = "permissions"
				session.SetConfigOptions([]byte(`[{"id":"permissions","category":"mode","type":"select","currentValue":"auto","options":[{"value":"auto","name":"Auto"},{"value":"plan","name":"Plan"}]},{"id":"model","type":"select"}]`))
			}
			config := string(session.ConfigJSON())
			if session.BypassingPermissions() || !strings.Contains(config, `"currentValue":"auto"`) ||
				!strings.Contains(config, `"name":"Plan"`) || !strings.Contains(config, `"name":"Bypass Permissions"`) {
				t.Fatalf("initial config %s", config)
			}
			remembered := rememberedPermissions(t, session)
			if err := session.SetConfigOption(ctx, key, []byte(`"bypassPermissions"`)); err != nil {
				t.Fatal(err)
			}
			if !session.BypassingPermissions() || remembered() != "bypassPermissions" || len(calls) != 0 ||
				!strings.Contains(string(session.ConfigJSON()), `"currentValue":"bypassPermissions"`) {
				t.Fatalf("bypass config %s, requests %q, remembered %q", session.ConfigJSON(), calls, remembered())
			}
			if err := session.SetConfigOption(ctx, key, []byte(`"plan"`)); err != nil {
				t.Fatal(err)
			}
			if session.BypassingPermissions() || remembered() != "plan" || len(calls) != 1 || calls[0] != key+`="plan"` {
				t.Fatalf("bypassing %v, requests %q, remembered %q", session.BypassingPermissions(), calls, remembered())
			}
		})
	}
}
