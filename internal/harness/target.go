package harness

import (
	"cmp"
	"fmt"
	"slices"
)

type Target string

const (
	Claude   Target = "claude"
	Cursor   Target = "cursor"
	Codex    Target = "codex"
	OpenCode Target = "opencode"
	Grok     Target = "grok"
	Agy      Target = "agy"
	OMP      Target = "omp"
)

type targetSpec struct {
	target                       Target
	binaryEnv, binary, launcher  string
	displayName                  string
	externalStatus, externalNote string
	rawPluginSubdirs             []string
	limitsWithoutCLI             bool
}

const workspaceWrite = "workspace-write"

var claudePluginSubdirs = []string{"hooks", "matchers", "core", "examples", "utils", "commands", "agents", "rules", "skills"}

var targetSpecs = [...]targetSpec{
	{
		target: Claude, binaryEnv: "CLAUDE_CODE_PATH", displayName: "Claude Code",
		externalStatus: "external", externalNote: "uses --plugin-dir and --settings; no workspace overlay",
		rawPluginSubdirs: claudePluginSubdirs,
	},
	{
		target: Cursor, binaryEnv: "CURSOR_AGENT_PATH", binary: "cursor-agent", launcher: "agent", displayName: "Cursor",
		externalStatus: workspaceWrite, externalNote: "writes .cursor/agents when agent artifacts exist",
		rawPluginSubdirs: []string{"hooks", "assets", "scripts", "commands", "agents", "rules", "skills"},
		limitsWithoutCLI: true,
	},
	{
		target: Codex, binaryEnv: "CODEX_PATH", displayName: "Codex",
		externalStatus: "external-home", externalNote: "uses CODEX_HOME; native project instructions may still apply",
	},
	{
		target: OpenCode, binaryEnv: "OPENCODE_PATH", displayName: "OpenCode",
		externalStatus: "external", externalNote: "uses OPENCODE_CONFIG_DIR; no workspace overlay",
	},
	{
		target: Grok, binaryEnv: "GROK_PATH", displayName: "Grok",
		externalStatus: "external-home", externalNote: "uses GROK_HOME; also reads real ~/.claude and ~/.grok",
		rawPluginSubdirs: claudePluginSubdirs,
	},
	{
		target: Agy, binaryEnv: "AGY_PATH", displayName: "Antigravity",
		externalStatus: workspaceWrite, externalNote: "writes .agents/plugins/agentpack-bundle",
		// Antigravity's hook renderer writes hooks.json at the plugin root.
		rawPluginSubdirs: []string{"commands", "agents", "rules", "skills"},
	},
	{
		target: OMP, binaryEnv: "OMP_PATH", displayName: "Oh My Pi",
		externalStatus: "external", externalNote: "uses --plugin-dir; native profile, settings and credentials remain in place",
		rawPluginSubdirs: []string{"hooks", "matchers", "core", "examples", "utils", "assets", "scripts", "tools", "commands", "agents", "rules", "skills"},
	},
}

func (target Target) spec() targetSpec {
	for i := range targetSpecs {
		if targetSpecs[i].target == target {
			return targetSpecs[i]
		}
	}
	return targetSpec{}
}

func AllTargets() []Target {
	targets := make([]Target, len(targetSpecs))
	for i := range targetSpecs {
		targets[i] = targetSpecs[i].target
	}
	return targets
}

func ParseTarget(value string) (Target, error) {
	target := Target(value)
	if target.spec().target == "" {
		return "", fmt.Errorf("unknown harness target %q", value)
	}
	return target, nil
}

// Binary names the environment override and default program for the agent's CLI.
func (target Target) Binary() (environment, program string) {
	spec := target.spec()
	return spec.binaryEnv, cmp.Or(spec.binary, string(target))
}

// LauncherName is the agentpack command that launches the target.
func (target Target) LauncherName() string { return cmp.Or(target.spec().launcher, string(target)) }

// ParseLauncher resolves an agentpack launcher command to its target.
func ParseLauncher(command string) (Target, bool) {
	for _, spec := range targetSpecs {
		if command == cmp.Or(spec.launcher, string(spec.target)) {
			return spec.target, true
		}
	}
	return "", false
}

func (target Target) UsesWorkspaceOverlay() bool {
	return target.spec().externalStatus == workspaceWrite
}

// ExternalLaunch describes the target's checkout writes and native configuration.
func (target Target) ExternalLaunch() (status, note string) {
	spec := target.spec()
	return spec.externalStatus, spec.externalNote
}

func (target Target) RawPluginSubdirs() []string {
	return slices.Clone(target.spec().rawPluginSubdirs)
}

// DisplayName is the agent's product name, as messages name it.
func DisplayName(target Target) string {
	return cmp.Or(target.spec().displayName, string(target))
}
