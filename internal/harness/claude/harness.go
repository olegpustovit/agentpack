package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func New() base.Harness {
	return base.Harness{Target: base.Claude, UserRoots: nativeUserRoots, Root: stagedRoot, Reset: resetPaths, Setup: prepare, ProjectMCP: planMCP, ProjectLSP: planLSP, Guidance: injectGuidance, AfterStage: finalize, Check: verify, Launch: launch, ACPAdapter: claudeACPPackage, ACPStageAtSession: true, LaunchACP: acpLaunch, Capability: Capability, Inheritance: base.Inheritance{
		Note:  "Claude still reads real ~/.claude.json and ~/.claude/settings.json; agentpack only adds --plugin-dir/--settings",
		Paths: func(string) []string { return base.HomeFiles(".claude.json", ".claude/settings.json") },
	}}
}

func nativeUserRoots(home string) []string {
	return []string{filepath.Join(home, ".claude")}
}

func launch(ctx base.LaunchContext) (*exec.Cmd, func() error, error) {
	binary, err := base.ResolveBinary(base.Claude.Binary())
	if err != nil {
		return nil, nil, err
	}
	arguments := append([]string(nil), ctx.Arguments...)
	if ctx.Yolo {
		arguments = base.PrependOnce(arguments, "--dangerously-skip-permissions")
	}
	command := exec.Command(binary)
	settings, err := SettingsPath(base.StageContext{ProjectRoot: ctx.ProjectRoot, WorkspaceRoot: ctx.WorkspaceRoot, Mode: ctx.Mode})
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(settings)
	if err != nil {
		return nil, nil, fmt.Errorf("read staged Claude settings: %w", err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, nil, fmt.Errorf("parse staged Claude settings: %w", err)
	}
	if ctx.SessionID != "" {
		// Per launch, never staged: the session ID would otherwise enter the cached bundle.
		if executable, err := os.Executable(); err == nil {
			addSessionStateHooks(value, executable, ctx.SessionID)
			if data, err = json.Marshal(value); err != nil {
				return nil, nil, err
			}
		}
	}
	// Inline settings avoid a project-dependent config filename in Claude's credential namespace.
	command.Args = append(command.Args, "--settings", string(data))
	plugins, err := paths.StagingPluginsDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return nil, nil, err
	}
	entries, _ := os.ReadDir(plugins)
	for _, entry := range entries {
		path := filepath.Join(plugins, entry.Name())
		if _, err := os.Stat(filepath.Join(path, ".claude-plugin", "plugin.json")); err == nil {
			command.Args = append(command.Args, "--plugin-dir", path)
		}
	}
	command.Args = append(command.Args, arguments...)
	return command, base.Done, nil
}

// addSessionStateHooks makes a terminal Claude report its turns to the agentpack worker running it:
// a prompt or a finished tool means working, a permission prompt means it needs you, Stop means idle.
// The hook prints nothing, so it never adds to the prompt, and it always exits 0.
func addSessionStateHooks(settings map[string]any, executable, sessionID string) {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}
	report := func(event, matcher, state string) {
		command := shellQuote(executable) + " hook-exec session-state --target claude --session " + shellQuote(sessionID) + " --state " + state
		entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}}}
		if matcher != "" {
			entry["matcher"] = matcher
		}
		list, _ := hooks[event].([]any)
		hooks[event] = append(list, entry)
	}
	report("UserPromptSubmit", "", "working")
	report("PostToolUse", "*", "working")
	report("Notification", "permission_prompt", "needs_you")
	report("Stop", "", "idle")
}

func stagedRoot(ctx base.StageContext) (string, error) {
	plugins, err := paths.StagingPluginsDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return "", err
	}
	return filepath.Join(plugins, paths.StagedAgentpackBundleName), nil
}
func resetPaths(ctx base.StageContext) ([]string, error) {
	plugins, err := paths.StagingPluginsDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return nil, err
	}
	return []string{plugins}, nil
}

func prepare(ctx base.StageContext) error {
	bundle, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	manifestDirectory := filepath.Join(bundle, ".claude-plugin")
	if err := os.MkdirAll(manifestDirectory, 0o755); err != nil {
		return err
	}
	manifest := `{"name":"agentpack-bundle","version":"1.0.0","description":"Merged pack.lock plugins/skills; optional user settings.json and .claude.json"}`
	if err := os.WriteFile(filepath.Join(manifestDirectory, "plugin.json"), []byte(manifest), 0o644); err != nil {
		return err
	}
	return MaterializeSettings(ctx)
}

func planMCP(entries mcp.Entries, ctx base.StageContext) (base.MCPProjection, error) {
	root, err := stagedRoot(ctx)
	if err != nil {
		return base.MCPProjection{}, err
	}
	data, warnings, err := RenderMCP(entries)
	if err != nil {
		return base.MCPProjection{}, err
	}
	servers := base.ProjectedMCPServers(entries)
	base.MarkMCPWarnings(servers, warnings)
	digest, err := base.MCPJSONScopeDigest(data, "mcpServers")
	if err != nil {
		return base.MCPProjection{}, err
	}
	return base.MCPProjection{Files: []base.MCPProjectedFile{{Path: filepath.Join(root, ".mcp.json"), Contents: data, Scope: "mcpServers", ScopeDigest: digest, MergeOrder: []string{"plugin definitions", "agentpack.toml", ".agents/mcp.json"}}}, Servers: servers, Warnings: warnings}, nil
}
func injectGuidance(blob string, ctx base.StageContext) error {
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	return InjectGuidance(root, blob)
}
func finalize(entries mcp.Entries, ctx base.StageContext) error {
	names := make([]string, 0, len(entries))
	for _, name := range entries.Names() {
		disabled := entries[name].Server.Disabled
		if disabled == nil || !*disabled {
			names = append(names, name)
		}
	}
	data, err := SettingsContents(names, KeepAttribution())
	if err != nil {
		return err
	}
	return WriteSettingsContents(ctx, data)
}
func verify(ctx base.StageContext) error {
	bundle, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(bundle, ".claude-plugin", "plugin.json")); err != nil {
		return fmt.Errorf("bundle missing manifest %s: %w", bundle, err)
	}
	{
		overlay, err := SettingsPath(ctx)
		if err != nil {
			return err
		}
		if _, err := os.Stat(overlay); err != nil {
			return fmt.Errorf("claude settings overlay missing %s: %w", overlay, err)
		}
	}
	return nil
}
