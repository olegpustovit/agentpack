package omp

import (
	_ "embed"
	"path/filepath"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/hooks"
)

//go:embed hooks.js
var hookSource string

type HookRenderer struct{}

func (HookRenderer) Target() base.Target { return base.OMP }

func mappedEvent(event hooks.Event) string {
	switch event {
	case hooks.PreToolUse, hooks.PermissionRequest:
		return "tool_call"
	case hooks.PostToolUse, hooks.PostToolUseFailure:
		return "tool_result"
	case hooks.UserPromptSubmit:
		return "input"
	case hooks.Stop:
		return "session_stop"
	case hooks.SessionStart:
		return "session_start"
	case hooks.SessionEnd:
		return "session_shutdown"
	case hooks.PreCompact:
		return "session.compacting"
	}
	return ""
}

func Support(event hooks.Event, handler hooks.Handler) hooks.Support {
	if mappedEvent(event) == "" {
		return hooks.Support{Kind: hooks.Unsupported, Reason: "OMP has no equivalent cancellable lifecycle hook"}
	}
	switch event {
	case hooks.PermissionRequest:
		return hooks.Support{Kind: hooks.Degraded, Reason: "OMP runs the permission hook before every tool call; allow preserves OMP's native approval policy and ask blocks the call"}
	case hooks.PreCompact:
		return hooks.Support{Kind: hooks.Degraded, Reason: "OMP compaction hooks add summary context; decisions cannot cancel compaction"}
	case hooks.SessionStart, hooks.SessionEnd:
		return hooks.Support{Kind: hooks.Degraded, Reason: "OMP session lifecycle hooks are notifications; decisions cannot cancel startup or shutdown"}
	}
	return hooks.Support{Kind: hooks.Emulated, Reason: "executed through an OMP extension and agentpack hook-exec"}
}

func (HookRenderer) Render(bundle hooks.Bundle, ctx hooks.RenderContext) (hooks.RenderOutput, error) {
	var output hooks.RenderOutput
	var entries []any
	for _, hook := range bundle.Hooks {
		support := Support(hook.Event, hook.Handler)
		if (hook.Event == hooks.PreCompact || hook.Event == hooks.SessionEnd) && hook.Matcher != "" && hook.Matcher != "*" {
			support = hooks.Support{Kind: hooks.Unsupported, Reason: "OMP does not expose the source/reason required by this lifecycle matcher"}
		}
		keep, err := hooks.CheckSupport(base.OMP, hook, support, &output, "", "executed through an OMP extension")
		if err != nil {
			return hooks.RenderOutput{}, err
		}
		if !keep {
			continue
		}
		spec, err := hooks.BuildExecutionSpec(base.OMP, hook, hook.Event, ctx, &output)
		if err != nil {
			return hooks.RenderOutput{}, err
		}
		timeout := uint64(25000)
		if hook.Handler.Timeout != nil {
			timeout = min(*hook.Handler.Timeout, 25) * 1000
		}
		entries = append(entries, map[string]any{"timeoutMs": timeout, "event": mappedEvent(hook.Event), "canonicalEvent": hook.Event, "kind": hook.Handler.Kind, "specPath": spec, "matcher": hook.Matcher})
	}
	if len(entries) != 0 {
		output.Files = append(output.Files,
			hooks.RenderedFile{Path: filepath.Join(ctx.TargetRoot, "hooks", "pre", "agentpack.js"), Text: hookSource},
			hooks.RenderedFile{Path: filepath.Join(ctx.TargetRoot, "hooks", "pre", "agentpack.json"), JSON: map[string]any{"agentpack": hooks.Executable(), "hooks": entries}},
		)
	}
	return output, nil
}
