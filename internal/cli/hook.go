package cli

import (
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

func (runner Runner) runHook(ctx context.Context, arguments []string) (int, error) {
	if len(arguments) == 0 {
		return 2, fmt.Errorf("hook-exec requires a kind")
	}
	kind, args := arguments[0], arguments[1:]
	targetText, args, found, err := takeFlag(args, "--target")
	if err != nil {
		return 2, err
	}
	if !found {
		return 2, fmt.Errorf("hook-exec requires --target")
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
		eventText, args, found, err := takeFlag(args, "--event")
		if err != nil || !found {
			return 2, flagError(err, "--event")
		}
		directory, args, found, err := takeFlag(args, "--specs-dir")
		if err != nil || !found {
			return 2, flagError(err, "--specs-dir")
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
		eventText, args, found, err := takeFlag(args, "--event")
		if err != nil || !found {
			return 2, flagError(err, "--event")
		}
		file, args, found, err := takeFlag(args, "--file")
		if err != nil || !found {
			return 2, flagError(err, "--file")
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
		id, args, found, err := takeFlag(args, "--session")
		if err != nil || !found {
			return 2, flagError(err, "--session")
		}
		state, args, found, err := takeFlag(args, "--state")
		if err != nil || !found {
			return 2, flagError(err, "--state")
		}
		// Codex's notify appends its JSON payload as the last argument; a notify the user configured
		// follows --then and receives that payload too.
		var then []string
		if len(args) > 0 && args[0] == "--then" {
			then, args = args[1:], nil
		}
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
			Type string `json:"type"`
		}
		if payload == "" || json.Unmarshal([]byte(payload), &notification) != nil || notification.Type == "agent-turn-complete" {
			// Reporting is best effort: a hook that fails must never stop the agent's turn.
			home, err := paths.UserAgentpackHome()
			if err == nil {
				reportCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				err = network.ReportAgentState(reportCtx, home, id, state)
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
	specPath, args, found, err := takeFlag(args, "--spec")
	if err != nil || !found {
		return 2, flagError(err, "--spec")
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

func flagError(err error, name string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("hook-exec requires %s", name)
}
