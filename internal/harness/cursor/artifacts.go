package cursor

import (
	"maps"
	"slices"

	"github.com/olegpustovit/agentpack/internal/artifacts"
	base "github.com/olegpustovit/agentpack/internal/harness"
)

var artifactLayout = artifacts.Layout{
	NativeKinds:   []artifacts.Kind{artifacts.Skill, artifacts.Command, artifacts.Agent, artifacts.Rule},
	CommandFields: []string{"name", "description"},
	SkillKeys:     artifacts.SkillExtraKeys,
	CommandKeys:   []string{"agent", "allowed-tools", "context", "disable-model-invocation", "model", "permission", "subtask"},
	AgentKeys:     slices.Concat(artifacts.AgentExtraKeys, []string{"readonly"}),
	AgentExtras:   agentExtras,
}

func agentExtras(source map[string]any) (map[string]any, []string) {
	extras := maps.Clone(source)
	delete(extras, "readonly")
	if cursorReadonly(source) {
		extras["readonly"] = true
	}
	return extras, nil
}

// cursorReadonly reports whether a Claude Code subagent's tools list leaves
// out every tool that edits files or runs commands. Cursor has no tools list
// for subagents; its one restriction is readonly: true ("no file edits, no
// state-changing shell commands"), which such a subagent gets.
func cursorReadonly(source map[string]any) bool {
	allowed, hasAllowed := base.ToolNames(source["tools"])
	if !hasAllowed || len(allowed) == 0 {
		return false
	}
	for _, tool := range allowed {
		switch tool {
		case "Write", "Edit", "MultiEdit", "NotebookEdit", "Bash", "*":
			return false
		}
	}
	return true
}
