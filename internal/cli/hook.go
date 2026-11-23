package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/hooks"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func (runner Runner) runHook(ctx context.Context, invocation Invocation) (int, error) {
	if len(invocation.Args) == 0 {
		return 2, fmt.Errorf("hook-exec requires a kind")
	}
	// Arguments after -- are a user notify command, which Codex follows with its payload.
	args, then := invocation.Args, []string(nil)
	if dash := invocation.Flags.ArgsLenAtDash(); dash >= 0 {
		args, then = args[:dash], args[dash:]
	}
	kind, args := args[0], args[1:]
	required := func(name string) (string, error) {
		if !invocation.Flags.Changed(name) {
			return "", fmt.Errorf("hook-exec requires --%s", name)
		}
		return stringFlag(invocation, name), nil
	}
	targetText, err := required("target")
	if err != nil {
		return 2, err
	}
	target, err := base.ParseTarget(targetText)
	if err != nil {
		return 2, err
	}
	entry, err := registry.ByTarget(target)
	if err != nil {
		return 2, err
	}
	noRenderer := fmt.Errorf("%s has no agentpack hooks", target)
	stdin, err := io.ReadAll(runner.Stdin)
	if err != nil {
		return 1, err
	}
	if kind == "dispatch" {
		eventText, err := required("event")
		if err != nil {
			return 2, err
		}
		directory, err := required("specs-dir")
		if err != nil {
			return 2, err
		}
		if err := noArgs(args); err != nil {
			return 2, err
		}
		event, ok := hooks.ParseEvent(eventText)
		if !ok {
			return 2, fmt.Errorf("unknown hook event %q", eventText)
		}
		if entry.Hooks == nil {
			return 2, noRenderer
		}
		outcome, err := hooks.Dispatch(ctx, hooks.DispatchArgs{Renderer: entry.Hooks, Event: event, SpecsDirectory: directory, Stdin: stdin})
		if err != nil {
			return 1, err
		}
		if err := writeJSON(runner.Stdout, outcome.JSON); err != nil {
			return 1, err
		}
		return outcome.ExitCode, nil
	}
	if kind == "inject-guidance" {
		eventText, err := required("event")
		if err != nil {
			return 2, err
		}
		file, err := required("file")
		if err != nil {
			return 2, err
		}
		if err := noArgs(args); err != nil {
			return 2, err
		}
		body, err := os.ReadFile(file)
		if err != nil {
			return 1, err
		}
		event, ok := hooks.ParseEvent(eventText)
		if !ok {
			return 2, fmt.Errorf("unknown hook event %q", eventText)
		}
		// Only Claude stages the inject-guidance hook.
		return 0, writeJSON(runner.Stdout, hooks.GuidanceHookSpecific(string(body), event))
	}
	if kind == "session-state" {
		id, err := required("session")
		if err != nil {
			return 2, err
		}
		state, err := required("state")
		if err != nil {
			return 2, err
		}
		// Codex's notify appends its JSON payload as the last argument; a notify the user configured
		// follows -- and receives that payload too.
		payload := ""
		if len(then) > 0 {
			payload = then[len(then)-1]
		} else if target == base.Codex && len(args) == 1 {
			payload, args = args[0], nil
		}
		if err := noArgs(args); err != nil {
			return 2, err
		}
		var notification struct {
			Type   string `json:"type"`
			Thread string `json:"thread-id"`
		}
		// Claude's hook input names its session; Codex's notify names its thread. Either lets a restart resume it.
		var hookInput struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(stdin, &hookInput)
		if payload == "" || json.Unmarshal([]byte(payload), &notification) != nil || notification.Type == "agent-turn-complete" {
			// Reporting is best effort: a hook that fails must never stop the agent's turn.
			home, err := paths.UserAgentpackHome()
			if err == nil {
				reportCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				err = network.ReportAgentState(reportCtx, home, id, state, cmp.Or(hookInput.SessionID, notification.Thread))
				cancel()
			}
			if err != nil {
				_, event := obs.Start(ctx, "hook", "session_state")
				event.Set("session_id", id)
				event.Fail(err)
				event.End()
			}
		}
		if len(then) > 0 {
			command := exec.Command(then[0], then[1:]...)
			if err := command.Start(); err != nil {
				_, event := obs.Start(ctx, "hook", "session_state_notify")
				event.Set("session_id", id)
				event.Fail(err)
				event.End()
			} else {
				_ = command.Process.Release()
			}
		}
		return 0, nil
	}
	specPath, err := required("spec")
	if err != nil {
		return 2, err
	}
	if err := noArgs(args); err != nil {
		return 2, err
	}
	spec, err := hooks.LoadExecutionSpec(specPath)
	if err != nil {
		return 1, err
	}
	if kind == "command" {
		output, err := hooks.RunCommand(ctx, spec, stdin)
		if err != nil {
			return 1, err
		}
		_, _ = runner.Stdout.Write(output.Stdout)
		_, _ = runner.Stderr.Write(output.Stderr)
		return output.ExitCode, nil
	}
	var result hooks.Result
	switch kind {
	case "http":
		result, err = hooks.RunHTTP(ctx, http.DefaultClient, spec, stdin)
	case "prompt":
		result, err = hooks.RunPrompt(ctx, spec, stdin)
	case "agent":
		result, err = hooks.RunAgent(ctx, spec, stdin)
	default:
		return 2, fmt.Errorf("unknown hook-exec kind %q", kind)
	}
	if err != nil {
		return 1, err
	}
	if entry.Hooks == nil {
		return 2, noRenderer
	}
	return 0, writeJSON(runner.Stdout, entry.Hooks.Output(spec.Event, result))
}
