package omp

import (
	"fmt"
	"slices"
	"strings"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// OMP accepts explicit tool allowlists, so deny-only agents need a bounded list.
// shortcut: built-ins match OMP 18.14, refresh this list when OMP adds tools.
var ompTools = []string{"read", "bash", "edit", "ast_grep", "ast_edit", "ask", "debug", "ida", "eval", "github", "glob", "grep", "find", "lsp", "checkpoint", "rewind", "context_notes", "new_context", "security_scan", "task", "wait", "todo", "web_search", "write", "memory_edit", "retain", "recall", "reflect", "learn", "manage_skill"}

// AgentExtras preserves native task fields and translates cross-harness tool restrictions.
func AgentExtras(source map[string]any) (map[string]any, []string) {
	extras := make(map[string]any, len(source))
	for key, value := range source {
		extras[key] = value
	}
	var notes []string
	if source["model"] == "inherit" {
		delete(extras, "model")
	}
	allowed, hasAllowed := base.ToolNames(source["tools"])
	denied, hasDenied := base.ToolNames(source["disallowedTools"])
	if values, ok := source["disallowedTools"].([]any); ok {
		for _, value := range values {
			if _, ok := value.(string); !ok {
				hasDenied = false
				break
			}
		}
	}
	if _, present := source["disallowedTools"]; present && !hasDenied {
		delete(extras, "spawns")
		extras["tools"] = []string{}
		return extras, []string{"invalid disallowedTools; no built-in tools are granted"}
	}
	if value, present := source["tools"]; present && value != nil && !hasAllowed {
		delete(extras, "spawns")
		extras["tools"] = []string{}
		return extras, []string{"invalid tools; no built-in tools are granted"}
	}

	if len(denied) == 0 {
		hasDenied = false
	}
	if !hasAllowed && !hasDenied {
		return extras, notes
	}
	aliases := map[string]string{"websearch": "web_search", "webfetch": "read", "todowrite": "todo", "todoread": "todo", "multiedit": "edit", "notebookedit": "edit", "ls": "glob", "list": "glob", "agent": "task", "search": "grep"}
	rename := func(name string) string {
		lower := strings.ToLower(name)
		if mapped, found := aliases[lower]; found {
			return mapped
		}
		if slices.Contains(ompTools, lower) {
			return lower
		}
		return name
	}
	if !hasAllowed || slices.Contains(allowed, "*") {
		allowed = slices.Clone(ompTools)
	}
	tools := make([]string, 0, len(allowed))
	for _, name := range allowed {
		mapped := rename(name)
		blocked := false
		for _, deny := range denied {
			if deny == "*" || rename(deny) == mapped {
				blocked = true
				break
			}
		}
		if !blocked && !slices.Contains(tools, mapped) {
			tools = append(tools, mapped)
		}
	}
	if strings.Contains(fmt.Sprint(source["tools"]), "(") {
		tools = []string{}
		notes = append(notes, "scoped tool permissions cannot be represented; no built-in tools are granted")
	}
	if hasDenied && !hasAllowed {
		notes = append(notes, "disallowedTools becomes an explicit OMP built-in allowlist; custom tools are not granted")
	}
	extras["tools"] = tools
	if source["spawns"] != nil && !slices.Contains(tools, "task") {
		delete(extras, "spawns")
		notes = append(notes, "spawns is omitted because the task tool is not allowed")
	}
	return extras, notes
}
