package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

const codexACPPackage = "@agentclientprotocol/codex-acp@2.8.0"

func acpLaunch(ctx base.LaunchContext) (base.ACPLaunch, error) {
	binary, err := base.EnsureACPAdapterExecutable(context.Background(), codexACPPackage)
	if err != nil {
		return base.ACPLaunch{}, err
	}
	home, release, err := LeaseHome(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return base.ACPLaunch{}, err
	}
	keepLease := false
	defer func() {
		if !keepLease {
			_ = release()
		}
	}()
	if needsShortHome(home) {
		if shortDir, err := shortStagingCodexHome(ctx.ProjectRoot, ctx.Mode.Name()); err == nil {
			home = shortDir
		}
	}
	command := base.ACPAdapterCommand(binary)
	command.Env = append(os.Environ(), "CODEX_HOME="+home)
	if ctx.AgentpackTools {
		config, err := withoutSubagents(os.Getenv("CODEX_CONFIG"))
		if err != nil {
			return base.ACPLaunch{}, err
		}
		command.Env = append(command.Env, "CODEX_CONFIG="+config)
	}
	keepLease = true
	// codex-acp serializes session/prompt behind the turn, but its _session/steering joins it.
	return base.ACPLaunch{Cmd: command, Rewrite: repairOnLoad(home), Steers: true, Finish: func() error { return finishLaunch(ctx, home, "", release) }}, nil
}

// withoutSubagents adds to codex-acp's session config (CODEX_CONFIG, JSON of dotted keys) the
// switches that remove Codex's own subagent tools; agentpack tools start helpers instead.
func withoutSubagents(existing string) (string, error) {
	config := map[string]any{}
	if existing != "" {
		if err := json.Unmarshal([]byte(existing), &config); err != nil {
			return "", fmt.Errorf("CODEX_CONFIG is not a JSON object: %w", err)
		}
	}
	config["agents.enabled"] = false // wins over the subagent version a model selects
	data, err := json.Marshal(config)
	return string(data), err
}
