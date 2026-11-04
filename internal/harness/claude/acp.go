package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/paths"
)

const claudeACPPackage = "@agentclientprotocol/claude-agent-acp@0.100.0"

func acpLaunch(ctx base.LaunchContext) (base.ACPLaunch, error) {
	binary, err := base.EnsureACPAdapterExecutable(context.Background(), claudeACPPackage)
	if err != nil {
		return base.ACPLaunch{}, err
	}
	command := exec.Command(binary)
	claudeBinary, err := claudeCodeExecutable()
	if err != nil {
		return base.ACPLaunch{}, err
	}
	command.Env = append(os.Environ(), "CLAUDE_CODE_EXECUTABLE="+claudeBinary)
	plugins, err := stagedPluginDirs(ctx)
	if err != nil {
		return base.ACPLaunch{}, err
	}
	pluginRoot, err := paths.StagingPluginsDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return base.ACPLaunch{}, err
	}
	settingsPath := filepath.Join(pluginRoot, "agentpack-bundle", "agentpack-settings.json")
	settingsInfo, err := os.Lstat(settingsPath)
	if err == nil && !settingsInfo.Mode().IsRegular() {
		return base.ACPLaunch{}, fmt.Errorf("staged Claude settings are not a regular file: %s", settingsPath)
	}
	if err != nil && !os.IsNotExist(err) {
		return base.ACPLaunch{}, err
	}
	if os.IsNotExist(err) {
		settingsPath = ""
	}
	var rewrite func([]byte) []byte
	if len(plugins) > 0 || settingsPath != "" {
		rewrite = rewriteSessionPlugins(plugins, settingsPath)
	}
	return base.ACPLaunch{Cmd: command, Rewrite: rewrite}, nil
}
func claudeCodeExecutable() (string, error) {
	if strings.TrimSpace(os.Getenv("CLAUDE_CODE_EXECUTABLE")) != "" {
		return base.ResolveBinary("CLAUDE_CODE_EXECUTABLE", "claude")
	}
	return base.ResolveBinary("CLAUDE_CODE_PATH", "claude")
}

func stagedPluginDirs(ctx base.LaunchContext) ([]string, error) {
	plugins, err := paths.StagingPluginsDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(plugins)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var dirs []string
	for _, entry := range entries {
		path := filepath.Join(plugins, entry.Name())
		if _, err := os.Stat(filepath.Join(path, ".claude-plugin", "plugin.json")); err == nil {
			dirs = append(dirs, path)
		}
	}
	return dirs, nil
}

func rewriteSessionPlugins(pluginDirs []string, settingsPath string) func([]byte) []byte {
	return func(line []byte) []byte {
		var envelope struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return line
		}
		switch envelope.Method {
		case "session/new", "session/load", "session/resume":
		default:
			return line
		}
		params := map[string]any{}
		if len(envelope.Params) > 0 {
			if err := json.Unmarshal(envelope.Params, &params); err != nil {
				return line
			}
		}
		meta, _ := params["_meta"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		claudeCode, _ := meta["claudeCode"].(map[string]any)
		if claudeCode == nil {
			claudeCode = map[string]any{}
		}
		options, _ := claudeCode["options"].(map[string]any)
		if options == nil {
			options = map[string]any{}
		}
		seen := map[string]bool{}
		var merged []any
		if existing, ok := options["plugins"].([]any); ok {
			for _, item := range existing {
				if path, ok := item.(string); ok && path != "" && !seen[path] {
					seen[path] = true
					merged = append(merged, path)
				}
			}
		}
		for _, path := range pluginDirs {
			if !seen[path] {
				seen[path] = true
				merged = append(merged, path)
			}
		}
		if len(merged) > 0 {
			options["plugins"] = merged
		}
		if settingsPath != "" {
			options["settings"] = settingsPath
		}
		claudeCode["options"] = options
		meta["claudeCode"] = claudeCode
		params["_meta"] = meta
		encoded, err := json.Marshal(params)
		if err != nil {
			return line
		}
		var out map[string]any
		if err := json.Unmarshal(line, &out); err != nil {
			return line
		}
		out["params"] = json.RawMessage(encoded)
		rewritten, err := json.Marshal(out)
		if err != nil {
			return line
		}
		return rewritten
	}
}
