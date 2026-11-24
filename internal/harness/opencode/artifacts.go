package opencode

import (
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"

	"github.com/olegpustovit/agentpack/internal/artifacts"
	base "github.com/olegpustovit/agentpack/internal/harness"
)

var artifactLayout = artifacts.Layout{
	NativeKinds:   []artifacts.Kind{artifacts.Skill, artifacts.Command, artifacts.Agent},
	CommandFields: []string{"description"}, CommandKeys: artifacts.CommandExtraKeys,
	SkillKeys: artifacts.SkillExtraKeys, AgentKeys: artifacts.AgentExtraKeys,
	CommandExtras: commandExtras, AgentExtras: agentExtras, NestedAgents: true, WarningName: "OpenCode",
}

func commandExtras(source map[string]any) (map[string]any, []string) {
	model, found := source["model"]
	if text, _ := model.(string); !found || strings.Contains(text, "/") {
		return source, nil
	}
	// A Claude Code alias such as sonnet names no OpenCode model.
	extras := maps.Clone(source)
	delete(extras, "model")
	return extras, []string{fmt.Sprintf("model %v is not an OpenCode provider/model id; the command uses the session's model", model)}
}

// openCodeTools maps Claude Code tool names to the OpenCode tools they
// correspond to. Every OpenCode tool listed here is switched off for an agent
// whose Claude Code tools list does not grant it.
var openCodeTools = map[string]string{
	"read": "read", "write": "write", "edit": "edit", "multiedit": "edit", "notebookedit": "edit",
	"bash": "bash", "grep": "grep", "glob": "glob", "ls": "list", "list": "list",
	"webfetch": "webfetch", "websearch": "websearch", "task": "task", "agent": "task",
	"todowrite": "todowrite", "skill": "skill",
}

// openCodeColors gives Claude Code's named agent colors the hex form OpenCode
// requires; OpenCode's own theme color names pass through.
var openCodeColors = map[string]string{
	"red": "#ef4444", "blue": "#3b82f6", "green": "#22c55e", "yellow": "#eab308",
	"purple": "#a855f7", "orange": "#f97316", "pink": "#ec4899", "cyan": "#06b6d4",
	"primary": "primary", "secondary": "secondary", "accent": "accent", "success": "success",
	"warning": "warning", "error": "error", "info": "info",
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// agentExtras rewrites the frontmatter OpenCode rejects: it validates
// every agent file and refuses to load its whole configuration when one field
// has another form. Claude Code writes an agent's tools as a comma-separated
// string or a list of allowed tools, where OpenCode documents an object of
// tool name to boolean; a color is a name, where OpenCode wants hex; a model
// is an alias such as sonnet, where OpenCode wants provider/model. It returns
// a note for each value that could not be carried over.
func agentExtras(source map[string]any) (map[string]any, []string) {
	extras := maps.Clone(source)
	var notes []string
	if value, found := source["color"]; found {
		text, _ := value.(string)
		if mapped, known := openCodeColors[strings.ToLower(text)]; known {
			extras["color"] = mapped
		} else if !hexColor.MatchString(text) {
			delete(extras, "color")
			notes = append(notes, fmt.Sprintf("color %v is not a color OpenCode accepts and is left out", value))
		}
	}
	if value, found := source["mode"]; found {
		if text, _ := value.(string); text != "subagent" && text != "primary" && text != "all" {
			delete(extras, "mode")
			notes = append(notes, fmt.Sprintf("mode %v is not an OpenCode agent mode and is left out", value))
		}
	}
	if value, found := source["model"]; found {
		if text, _ := value.(string); !strings.Contains(text, "/") {
			delete(extras, "model")
			notes = append(notes, fmt.Sprintf("model %v is not an OpenCode provider/model id; the agent uses the session's model", value))
		}
	}
	allowed, hasAllowed := base.ToolNames(source["tools"])
	denied, hasDenied := base.ToolNames(source["disallowedTools"])
	delete(extras, "disallowedTools")
	if _, native := source["tools"].(map[string]any); native || !hasAllowed && !hasDenied {
		if !native {
			delete(extras, "tools")
		}
		return extras, notes
	}
	tools := make(map[string]any)
	var dropped []string
	enable := func(names []string, value bool) {
		for _, name := range names {
			mapped, found := openCodeTools[strings.ToLower(name)]
			if !found {
				dropped = append(dropped, name)
				continue
			}
			tools[mapped] = value
		}
	}
	if hasAllowed {
		for _, mapped := range openCodeTools {
			tools[mapped] = false
		}
		enable(allowed, true)
	}
	enable(denied, false)
	extras["tools"] = tools
	if len(dropped) != 0 {
		sort.Strings(dropped)
		notes = append(notes, fmt.Sprintf("tool(s) %s have no OpenCode equivalent and are left out of its tools map", strings.Join(dropped, ", ")))
	}
	return extras, notes
}
