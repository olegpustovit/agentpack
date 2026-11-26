package agy

import (
	"path"
	"strings"

	"github.com/olegpustovit/agentpack/internal/artifacts"
)

var artifactLayout = artifacts.Layout{
	// agy never loads a plugin's commands/, so commands are staged as skills.
	NativeKinds: []artifacts.Kind{artifacts.Skill, artifacts.Agent, artifacts.Rule},
	Rule:        renderRule,
}

// renderRule writes a rule the way Antigravity reads one: a .md file whose
// frontmatter sets trigger (always_on, glob, model_decision or manual).
// Antigravity silently discards a rule file without a valid trigger, which is
// what a Cursor rule's alwaysApply and globs keys are to it.
func renderRule(artifact artifacts.Markdown) artifacts.Rendered {
	fields := []artifacts.Field{{Key: "description", Value: artifact.Description}}
	switch {
	case artifact.AlwaysApply:
		fields = append(fields, artifacts.Field{Key: "trigger", Value: "always_on"})
	case len(artifact.Globs) != 0:
		fields = append(fields, artifacts.Field{Key: "trigger", Value: "glob"}, artifacts.Field{Key: "globs", Value: strings.Join(artifact.Globs, ",")})
	case artifact.Description != "":
		fields = append(fields, artifacts.Field{Key: "trigger", Value: "model_decision"})
	default:
		fields = append(fields, artifacts.Field{Key: "trigger", Value: "manual"})
	}
	name := artifact.NestedPath()
	if extension := path.Ext(name); strings.EqualFold(extension, ".mdc") {
		name = strings.TrimSuffix(name, extension) + ".md"
	}
	return artifacts.Rendered{RelativePath: name, Contents: artifacts.RenderMarkdown(fields, artifact.Body)}
}
