package agy

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

// Antigravity reads <plugin>/hooks.json as {"<hook name>": {"<Event>": [...]}},
// with matcher groups for the tool events and flat handlers for the others.
func TestRendererWritesAntigravityHooksAtThePluginRoot(t *testing.T) {
	t.Parallel()
	origin := func(index int) hooks.Origin {
		return hooks.Origin{Module: "module", PackageKey: "pkg", SourceFile: "hooks.json", EventIndex: index}
	}
	timeout := uint64(10)
	command := func(text string) hooks.Handler {
		return hooks.Handler{Kind: hooks.CommandHandler, Command: text, Timeout: &timeout}
	}
	bundle := hooks.Bundle{Hooks: []hooks.Hook{
		{Event: hooks.PreToolUse, Matcher: "Bash", Handler: command("check"), Origin: origin(0)},
		{Event: hooks.PreToolUse, Matcher: "Write|Edit", Handler: command("check-write"), Origin: hooks.Origin{Module: "module", PackageKey: "pkg", SourceFile: "hooks.json", HookIndex: 1}},
		{Event: hooks.PostToolUse, Handler: command("after"), Origin: origin(1)},
		{Event: hooks.SessionStart, Matcher: "startup", Handler: command("start"), Origin: origin(2)},
		{Event: hooks.UserPromptSubmit, Handler: command("prompt"), Origin: origin(3)},
		{Event: hooks.Stop, Handler: command("stop"), Origin: origin(4)},
		{Event: hooks.PostToolUseFailure, Handler: command("never"), Origin: origin(5)},
		{Event: hooks.SessionEnd, Handler: command("never"), Origin: origin(6)},
	}}
	output, err := (HookRenderer{}).Render(bundle, hooks.RenderContext{TargetRoot: "/staged/bundle", StagedPackages: map[string]string{"pkg": "/staged/bundle/hooks/_packages/pkg/package"}})
	if err != nil {
		t.Fatal(err)
	}
	file := output.Files[len(output.Files)-1]
	if file.Path != filepath.Join("/staged/bundle", "hooks.json") {
		t.Fatalf("hooks file at %s", file.Path)
	}
	named := file.JSON.(map[string]any)["agentpack"].(map[string]any)
	if len(named) != 5 {
		t.Fatalf("events = %#v", named)
	}
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		groups := named[event].([]any)
		group := groups[0].(map[string]any)
		handler := group["hooks"].([]any)[0].(map[string]any)
		if len(groups) != 1 || group["matcher"] != "*" || handler["type"] != "command" || handler["timeout"] != uint64(10) || !strings.Contains(handler["command"].(string), "hook-exec dispatch --target agy --event "+event) {
			t.Fatalf("%s = %#v", event, groups)
		}
	}
	for event, source := range map[string]string{"SessionStart": "SessionStart", "PreInvocation": "UserPromptSubmit", "Stop": "Stop"} {
		handlers := named[event].([]any)
		handler := handlers[0].(map[string]any)
		if len(handlers) != 1 || handler["hooks"] != nil || !strings.Contains(handler["command"].(string), "--event "+source+" ") {
			t.Fatalf("%s = %#v", event, handlers)
		}
	}
	if output.Summary.Omitted != 2 || output.Summary.Degraded != 1 || output.Summary.Emulated != 5 {
		t.Fatalf("summary = %#v", output.Summary)
	}
	var skipped []string
	for _, diagnostic := range output.Diagnostics {
		if diagnostic.Level == "omitted" {
			skipped = append(skipped, diagnostic.Message)
		}
	}
	if strings.Join(skipped, "|") != "skipping PostToolUseFailure hook: Antigravity has no equivalent hook event|skipping SessionEnd hook: Antigravity has no equivalent hook event" {
		t.Fatalf("skipped = %v", skipped)
	}
}

// Antigravity sends {"toolCall": {"name": "run_command"}} and reads a
// decision from the JSON it gets back, not from the exit status.
func TestDispatchForAntigravityMatchesItsToolNamesAndAnswersInItsContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs sh")
	}
	t.Parallel()
	specs := t.TempDir()
	write := func(name string, spec hooks.ExecutionSpec) {
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(specs, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.json", hooks.ExecutionSpec{Target: harness.Agy, Event: hooks.PreToolUse, Matcher: "Bash", Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: `echo '{"decision":"block","message":"no shell"}'`}})
	write("b.json", hooks.ExecutionSpec{Target: harness.Agy, Event: hooks.UserPromptSubmit, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: `echo '{"additionalContext":"remember"}'`}})
	write("c.json", hooks.ExecutionSpec{Target: harness.Agy, Event: hooks.Stop, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: `echo '{"decision":"block","reason":"x","message":"not done"}'`}})
	run := func(event hooks.Event, stdin string) (map[string]any, int) {
		outcome, err := hooks.Dispatch(context.Background(), hooks.DispatchArgs{Renderer: HookRenderer{}, Event: event, SpecsDirectory: specs, Stdin: []byte(stdin)})
		if err != nil {
			t.Fatal(err)
		}
		object, _ := outcome.JSON.(map[string]any)
		return object, outcome.ExitCode
	}
	denied, code := run(hooks.PreToolUse, `{"toolCall":{"name":"run_command","args":{"CommandLine":"rm -rf /"}},"stepIdx":1}`)
	if denied["decision"] != hooks.Deny || denied["reason"] != "no shell" || code != 0 {
		t.Fatalf("hooks.PreToolUse = %#v, exit %d", denied, code)
	}
	// a hook that does not fire still answers: Antigravity denies a call whose hook gives no decision
	if other, _ := run(hooks.PreToolUse, `{"toolCall":{"name":"view_file"}}`); len(other) != 1 || other["decision"] != "allow" {
		t.Fatalf("a Bash matcher fired for view_file, or the answer has no decision: %#v", other)
	}
	injected, _ := run(hooks.UserPromptSubmit, `{"invocationNum":1}`)
	steps, _ := injected["injectSteps"].([]any)
	if len(steps) != 1 || steps[0].(map[string]any)["ephemeralMessage"] != "remember" {
		t.Fatalf("PreInvocation = %#v", injected)
	}
	if stop, code := run(hooks.Stop, `{"terminationReason":"NO_TOOL_CALL"}`); stop["decision"] != "continue" || stop["reason"] != "not done" || code != 0 {
		t.Fatalf("hooks.Stop = %#v", stop)
	}
}

// Antigravity requires a decision. "allow" hands the call on to the next hook
// or the default policy; "auto_approve" would skip the user's approval and is
// never sent for a hook that merely did not object.
func TestAntigravityPreToolUseAllowsWithoutAutoApproving(t *testing.T) {
	t.Parallel()
	if output := (HookRenderer{}).Output(hooks.PreToolUse, hooks.Result{Decision: hooks.Allow, Message: "fine"}).(map[string]any); output["decision"] != "allow" || len(output) != 1 {
		t.Fatalf("no objection = %#v", output)
	}
	if output := (HookRenderer{}).Output(hooks.PreToolUse, hooks.Result{}).(map[string]any); output["decision"] != "allow" {
		t.Fatalf("empty result = %#v", output)
	}
	if output := (HookRenderer{}).Output(hooks.PreToolUse, hooks.Result{Decision: hooks.Ask, Message: "sure?"}).(map[string]any); output["decision"] != hooks.Ask || output["reason"] != "sure?" {
		t.Fatalf("ask = %#v", output)
	}
}
