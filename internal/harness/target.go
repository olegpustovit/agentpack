package harness

import "fmt"

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

func AllTargets() []Target { return []Target{Claude, Cursor, Codex, OpenCode, Grok, Agy, OMP} }

func ParseTarget(value string) (Target, error) {
	for _, target := range AllTargets() {
		if string(target) == value {
			return target, nil
		}
	}
	return "", fmt.Errorf("unknown harness target %q", value)
}

// Binary names the environment override and default program for the agent's CLI.
func (target Target) Binary() (environment, program string) {
	switch target {
	case Claude:
		return "CLAUDE_CODE_PATH", "claude"
	case Cursor:
		return "CURSOR_AGENT_PATH", "cursor-agent"
	case Codex:
		return "CODEX_PATH", "codex"
	case OpenCode:
		return "OPENCODE_PATH", "opencode"
	case Grok:
		return "GROK_PATH", "grok"
	case Agy:
		return "AGY_PATH", "agy"
	case OMP:
		return "OMP_PATH", "omp"
	}
	return "", string(target)
}

func (target Target) UsesWorkspaceOverlay() bool { return target == Cursor || target == Agy }

func (target Target) RawPluginSubdirs() []string {
	switch target {
	case OMP:
		return []string{"hooks", "matchers", "core", "examples", "utils", "assets", "scripts", "tools", "commands", "agents", "rules", "skills"}
	case Claude, Grok:
		return []string{"hooks", "matchers", "core", "examples", "utils", "commands", "agents", "rules", "skills"}
	case Cursor:
		return []string{"hooks", "assets", "scripts", "commands", "agents", "rules", "skills"}
	case Agy:
		// No raw hooks directory: Antigravity reads hooks.json at the plugin
		// root, which the hook renderer writes in Antigravity's own shape.
		return []string{"commands", "agents", "rules", "skills"}
	default:
		return nil
	}
}

// DisplayName is the agent's product name, as messages name it.
func DisplayName(target Target) string {
	switch target {
	case Claude:
		return "Claude Code"
	case Cursor:
		return "Cursor"
	case Codex:
		return "Codex"
	case OpenCode:
		return "OpenCode"
	case OMP:
		return "Oh My Pi"
	case Grok:
		return "Grok"
	case Agy:
		return "Antigravity"
	}
	return string(target)
}
