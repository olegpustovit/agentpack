package cursor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/hooks"
)

func TestRendererWritesEverySpecButDeduplicatesBlanketDispatcher(t *testing.T) {
	t.Parallel()
	origin := hooks.Origin{Module: "module", PackageKey: "pkg", SourceFile: "hooks.json"}
	bundle := hooks.Bundle{Hooks: []hooks.Hook{
		{Event: hooks.PreToolUse, Matcher: "Bash", Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "a"}, Origin: origin},
		{Event: hooks.PreToolUse, Matcher: "Write", Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "b"}, Origin: hooks.Origin{Module: "module", PackageKey: "pkg", SourceFile: "hooks.json", HookIndex: 1}},
		{Event: hooks.Notification, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "c"}, Origin: origin},
	}}
	output, err := (HookRenderer{}).Render(bundle, hooks.RenderContext{TargetRoot: t.TempDir(), StagedPackages: map[string]string{"pkg": t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Files) != 3 || output.Summary.Native != 2 || output.Summary.Omitted != 1 {
		t.Fatalf("output = %#v", output)
	}
	root := output.Files[len(output.Files)-1].JSON.(map[string]any)
	events := root["hooks"].(map[string]any)
	if len(events["preToolUse"].([]any)) != 1 {
		t.Fatalf("events = %#v", events)
	}
}

func TestRendererKeepsCursorHooksNativeAndMapsSharedEvents(t *testing.T) {
	t.Parallel()
	origin := func(index int) hooks.Origin {
		return hooks.Origin{Module: "module", PackageKey: "pkg", SourceFile: "hooks.json", EventIndex: index}
	}
	timeout := uint64(30)
	bundle := hooks.Bundle{Hooks: []hooks.Hook{
		{Event: "afterAgentResponse", Dialect: hooks.CursorDialect, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: `bash "${CURSOR_PLUGIN_ROOT}/hooks/capture.sh"`}, Origin: origin(0)},
		{Event: hooks.SubagentStop, Dialect: hooks.CursorDialect, Matcher: "^advisor-subagent$", Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "record"}, Origin: origin(1)},
		{Event: hooks.Stop, Dialect: hooks.CursorDialect, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "stop", Timeout: &timeout}, RawExtra: map[string]any{"loop_limit": float64(3)}, Origin: origin(2)},
		{Event: hooks.PostToolUseFailure, Dialect: hooks.ClaudeDialect, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "failure"}, Origin: origin(3)},
		{Event: "StopFailure", Dialect: hooks.ClaudeDialect, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "never"}, Origin: origin(4)},
	}}
	output, err := (HookRenderer{}).Render(bundle, hooks.RenderContext{TargetRoot: "/staged", StagedPackages: map[string]string{"pkg": "/staged/pkg"}})
	if err != nil {
		t.Fatal(err)
	}
	events := output.Files[len(output.Files)-1].JSON.(map[string]any)["hooks"].(map[string]any)
	entry := func(event string) map[string]any {
		entries, _ := events[event].([]any)
		if len(entries) != 1 {
			t.Fatalf("%s = %#v", event, events[event])
		}
		return entries[0].(map[string]any)
	}
	if command := entry("afterAgentResponse")["command"].(string); !strings.HasPrefix(command, "agentpack hook-exec command --target cursor --spec ") {
		t.Fatalf("afterAgentResponse = %s", command)
	}
	if entry("subagentStop")["matcher"] != "^advisor-subagent$" || entry("stop")["loop_limit"] != float64(3) || entry("stop")["timeout"] != uint64(30) {
		t.Fatalf("native options lost: %#v", events)
	}
	if command := entry("postToolUseFailure")["command"].(string); !strings.Contains(command, "hook-exec dispatch --target cursor --event PostToolUseFailure") {
		t.Fatalf("postToolUseFailure = %s", command)
	}
	if len(events) != 4 || output.Summary.Omitted != 1 || !strings.Contains(output.Diagnostics[len(output.Diagnostics)-1].Message, "skipping StopFailure hook") {
		t.Fatalf("events = %#v\ndiagnostics = %#v", events, output.Diagnostics)
	}
}
func TestDispatchFiltersSpecsAndDenyWins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixture")
	}
	root := t.TempDir()
	writeSpec := func(name string, spec hooks.ExecutionSpec) {
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSpec("allow.json", hooks.ExecutionSpec{Target: harness.Cursor, Event: hooks.PreToolUse, Matcher: "Bash", Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: `printf '{"decision":"allow","message":"ok"}'`}, WorkingDir: root})
	writeSpec("deny.json", hooks.ExecutionSpec{Target: harness.Cursor, Event: hooks.PreToolUse, Matcher: "Shell", Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: `printf '{"decision":"deny","message":"no"}'; exit 2`}, WorkingDir: root})
	writeSpec("other.json", hooks.ExecutionSpec{Target: harness.Cursor, Event: hooks.PostToolUse, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "exit 2"}, WorkingDir: root})
	outcome, err := hooks.Dispatch(context.Background(), hooks.DispatchArgs{Renderer: HookRenderer{}, Event: hooks.PreToolUse, SpecsDirectory: root, Stdin: []byte(`{"tool_name":"Shell"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.ExitCode != 2 {
		t.Fatalf("outcome = %#v", outcome)
	}
	value := outcome.JSON.(map[string]any)
	if value["permission"] != hooks.Deny || value["user_message"] != "ok\nno" {
		t.Fatalf("JSON = %#v", value)
	}
}

func TestHookOutputShapes(t *testing.T) {
	t.Parallel()
	result := hooks.Result{Decision: hooks.Ask, Message: "why", AdditionalContext: "ctx", UpdatedInput: 1, UpdatedToolOutput: 2}
	if output := (HookRenderer{}).Output(hooks.PreToolUse, result).(map[string]any); output["permission"] != hooks.Ask {
		t.Fatalf("output = %#v", output)
	}
}
