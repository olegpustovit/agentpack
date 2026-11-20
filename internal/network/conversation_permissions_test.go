package network

import (
	"context"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/state"
)

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
	first.permissionChange = func(mode string) {
		if _, err := store.DB.Exec("INSERT INTO mesh_settings(key,value) VALUES('permission_mode.claude',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", mode); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := first.SetConfigOption(ctx, "model", []byte(`"sonnet"`)); err != nil {
		t.Fatal(err)
	}
	if err := first.SetConfigOption(ctx, "mode", []byte(`"bypassPermissions"`)); err != nil {
		t.Fatal(err)
	}
	next := NewConversationSession(acp.NewEventLog(0), broker)
	next.SetConfigOptions(options)
	next.applyRememberedPermissions(ctx, store, "permission_mode.claude")
	next.applyRememberedPermissions(ctx, store, "permission_mode.codex") // nothing remembered for codex
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
	var remembered string
	session.permissionChange = func(mode string) { remembered = mode }
	if err := session.SetConfigOption(context.Background(), "mode", []byte(`"bypassPermissions"`)); err != nil {
		t.Fatal(err)
	}
	if !session.BypassingPermissions() || remembered != "bypassPermissions" {
		t.Fatalf("bypassing %v, remembered %q", session.BypassingPermissions(), remembered)
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
			var remembered string
			session.permissionChange = func(mode string) { remembered = mode }
			if err := session.SetConfigOption(ctx, key, []byte(`"bypassPermissions"`)); err != nil {
				t.Fatal(err)
			}
			if !session.BypassingPermissions() || remembered != "bypassPermissions" || len(calls) != 0 ||
				!strings.Contains(string(session.ConfigJSON()), `"currentValue":"bypassPermissions"`) {
				t.Fatalf("bypass config %s, requests %q, remembered %q", session.ConfigJSON(), calls, remembered)
			}
			if err := session.SetConfigOption(ctx, key, []byte(`"plan"`)); err != nil {
				t.Fatal(err)
			}
			if session.BypassingPermissions() || remembered != "plan" || len(calls) != 1 || calls[0] != key+`="plan"` {
				t.Fatalf("bypassing %v, requests %q, remembered %q", session.BypassingPermissions(), calls, remembered)
			}
		})
	}
}
