package opencode

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/hooks"
	"github.com/tailscale/hujson"
)

//go:embed hooks_plugin.js
var pluginSource string

type HookRenderer struct{}

func (HookRenderer) Target() harness.Target { return harness.OpenCode }

func (HookRenderer) Render(bundle hooks.Bundle, context hooks.RenderContext) (hooks.RenderOutput, error) {
	var output hooks.RenderOutput
	if len(bundle.Hooks) == 0 {
		return output, nil
	}
	var entries []any
	for _, hook := range bundle.Hooks {
		event, mapped := mappedEvent(hook.Event)
		if !mapped {
			if hook.Strict() {
				return hooks.RenderOutput{}, hooks.StrictMappingError(harness.OpenCode, hook, "OpenCode has no equivalent lifecycle hook")
			}
			hooks.PushDiagnostic(&output, "omitted", hook, "OpenCode has no equivalent lifecycle hook")
			continue
		}
		keep, err := hooks.CheckSupport(harness.OpenCode, hook, Support(hook.Event, hook.Handler), &output, "rendered into generated OpenCode plugin", "wrapped into generated OpenCode plugin")
		if err != nil {
			return hooks.RenderOutput{}, err
		}
		if !keep {
			continue
		}
		specPath, err := hooks.BuildExecutionSpec(harness.OpenCode, hook, hook.Event, context, &output)
		if err != nil {
			return hooks.RenderOutput{}, err
		}
		entry := map[string]any{"event": event, "kind": string(hook.Handler.Kind), "specPath": specPath, "strict": hook.Strict()}
		if hook.Matcher != "" {
			entry["matcher"] = hook.Matcher
		} else {
			entry["matcher"] = nil
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return output, nil
	}
	pluginRoot := filepath.Join(context.TargetRoot, "agentpack-hooks")
	reference := "./agentpack-hooks/index.js"
	if nativeMajor() == 2 {
		reference = "./agentpack-hooks"
	}
	config, err := mergedConfig(context.TargetRoot, reference)
	if err != nil {
		return hooks.RenderOutput{}, err
	}
	output.Files = append(output.Files,
		hooks.RenderedFile{Path: filepath.Join(pluginRoot, "config.json"), JSON: map[string]any{"agentpack": hooks.Executable(), "hooks": entries}},
		hooks.RenderedFile{Path: filepath.Join(pluginRoot, "index.js"), Text: pluginSource},
		hooks.RenderedFile{Path: packConfigPath(context.TargetRoot), JSON: config},
	)
	return output, nil
}

func Support(event hooks.Event, handler hooks.Handler) hooks.Support {
	switch event {
	case hooks.PreToolUse, hooks.PostToolUse, hooks.PermissionRequest, hooks.PreCompact:
	case hooks.UserPromptSubmit:
		return hooks.Support{Kind: hooks.Degraded, Reason: "OpenCode exposes chat.message after receipt rather than Claude's submit hook"}
	default:
		return hooks.Support{Kind: hooks.Unsupported, Reason: "OpenCode has no direct lifecycle hook for this Claude event"}
	}
	if handler.Kind == hooks.CommandHandler {
		return hooks.Support{Kind: hooks.Native}
	}
	return hooks.Support{Kind: hooks.Emulated}
}

func mappedEvent(event hooks.Event) (string, bool) {
	switch event {
	case hooks.PreToolUse:
		return "tool.execute.before", true
	case hooks.PostToolUse:
		return "tool.execute.after", true
	case hooks.UserPromptSubmit:
		return "chat.message", true
	case hooks.PermissionRequest:
		return "permission.ask", true
	case hooks.PreCompact:
		return "experimental.session.compacting", true
	default:
		return "", false
	}
}

func mergedConfig(root, reference string) (map[string]any, error) {
	path := packConfigPath(root)
	config := map[string]any{"$schema": "https://opencode.ai/config.json"}
	if data, err := os.ReadFile(path); err == nil {
		standard, err := hujson.Standardize(append(data, '\n'))
		if err != nil {
			return nil, fmt.Errorf("parse opencode.json: %w", err)
		}
		if err := json.Unmarshal(standard, &config); err != nil {
			return nil, fmt.Errorf("parse opencode.json: %w", err)
		}
		if config == nil {
			return nil, fmt.Errorf("opencode.json must be an object")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	plugins, found := config["plugin"].([]any)
	if config["plugin"] != nil && !found {
		return nil, fmt.Errorf("opencode.json plugin must be an array")
	}
	for _, plugin := range plugins {
		if plugin == reference {
			return config, nil
		}
	}
	config["plugin"] = append(plugins, reference)
	return config, nil
}
