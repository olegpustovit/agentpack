package artifacts

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
)

// Layout describes how a harness reads the markdown artifacts staged for it.
// Kinds without a native representation are rendered as skills.
type Layout struct {
	NativeKinds   []Kind
	CommandFields []string
	SkillKeys     []string
	CommandKeys   []string
	AgentKeys     []string
	CommandExtras func(map[string]any) (map[string]any, []string)
	AgentExtras   func(map[string]any) (map[string]any, []string)
	NestedAgents  bool
	WarningName   string
	Rule          func(Markdown) Rendered
	// AdditionalAgent emits a second representation for packaged agents.
	AdditionalAgent func(Markdown) Rendered
}

// Common extra fields shared by the markdown layouts, in output order.
var (
	SkillExtraKeys   = []string{"allowed-tools", "agent", "compatibility", "context", "disallowedTools", "license", "mcpServers", "metadata", "mode", "model", "permission", "subtask", "tools"}
	AgentExtraKeys   = []string{"color", "disallowedTools", "hidden", "hooks", "mcpServers", "mode", "model", "permission", "subtask", "tools"}
	CommandExtraKeys = []string{"agent", "allowed-tools", "context", "disable-model-invocation", "model", "subtask"}
)

// Kind is the kind an artifact takes in this layout.
func (layout *Layout) Kind(source Kind) Kind {
	if slices.Contains(layout.NativeKinds, source) {
		return source
	}
	return Skill
}

type Field struct {
	Key   string
	Value any
}

func (artifact Markdown) Render(layout *Layout) Rendered {
	switch layout.Kind(artifact.Kind) {
	case Skill:
		fields := []Field{{"name", artifact.Name}, {"description", artifact.Description}}
		if artifact.DisableModelInvocation || artifact.Kind == Command {
			fields = append(fields, Field{"disable-model-invocation", true})
		}
		fields = mergeExtra(fields, artifact.ExtraFrontmatter, layout.SkillKeys)
		return Rendered{RelativePath: filepath.ToSlash(filepath.Join("skills", artifact.StorageName, "SKILL.md")), Contents: RenderMarkdown(fields, artifact.skillBody())}
	case Command:
		var fields []Field
		for _, key := range layout.CommandFields {
			switch key {
			case "name":
				fields = append(fields, Field{key, artifact.Name})
			case "description":
				fields = append(fields, Field{key, artifact.Description})
			}
		}
		return artifact.renderWithExtras(artifact.NestedPath(), fields, layout.CommandKeys, layout.CommandExtras, layout.WarningName)
	case Agent:
		fields := []Field{{"name", artifact.Name}, {"description", artifact.Description}}
		destination := artifact.NestedPath()
		if !layout.NestedAgents {
			destination = "agents/" + path.Base(artifact.tailPath)
		}
		return artifact.renderWithExtras(destination, fields, layout.AgentKeys, layout.AgentExtras, layout.WarningName)
	default:
		if layout.Rule != nil {
			return layout.Rule(artifact)
		}
		return artifact.renderRule()
	}
}

func (artifact Markdown) renderWithExtras(destination string, fields []Field, keys []string, transform func(map[string]any) (map[string]any, []string), warningName string) Rendered {
	extras := artifact.ExtraFrontmatter
	var warnings []string
	if transform != nil {
		extras, warnings = transform(extras)
		for i := range warnings {
			warnings[i] = warningName + " " + artifact.Kind.Word() + " " + artifact.Name + ": " + warnings[i]
		}
	}
	return Rendered{RelativePath: destination, Contents: RenderMarkdown(mergeExtra(fields, extras, keys), artifact.Body), Warnings: warnings}
}

// NestedPath is where the artifact sits in its package's own layout, under its
// kind's directory (agents/team/reviewer.md).
func (artifact Markdown) NestedPath() string {
	return artifact.Kind.Word() + "s/" + artifact.tailPath
}

func (artifact Markdown) renderRule() Rendered {
	fields := []Field{{"description", artifact.Description}}
	if len(artifact.Globs) == 1 {
		fields = append(fields, Field{"globs", artifact.Globs[0]})
	} else if len(artifact.Globs) > 1 {
		fields = append(fields, Field{"globs", artifact.Globs})
	}
	if artifact.AlwaysApply {
		fields = append(fields, Field{"alwaysApply", true})
	}
	return Rendered{RelativePath: "rules/" + artifact.tailPath, Contents: RenderMarkdown(fields, artifact.Body)}
}

func (artifact Markdown) skillBody() string {
	if artifact.Kind != Rule || len(artifact.Globs) == 0 && !artifact.AlwaysApply {
		return artifact.Body
	}
	var output strings.Builder
	output.WriteString("## Original rule scope\n")
	if artifact.AlwaysApply {
		output.WriteString("- Applies in every session.\n")
	}
	if len(artifact.Globs) != 0 {
		quoted := make([]string, len(artifact.Globs))
		for index, glob := range artifact.Globs {
			quoted[index] = "`" + glob + "`"
		}
		output.WriteString("- Original Cursor globs: " + strings.Join(quoted, ", ") + "\n")
	}
	output.WriteByte('\n')
	output.WriteString(strings.TrimLeft(artifact.Body, "\n"))
	return trailingNewline(output.String())
}

func mergeExtra(fields []Field, extras map[string]any, allowed []string) []Field {
	if len(extras) == 0 || len(allowed) == 0 {
		return fields
	}
	existing := make(map[string]bool, len(fields))
	for _, field := range fields {
		existing[field.Key] = true
	}
	for _, key := range allowed {
		if value, found := extras[key]; found && !existing[key] {
			fields = append(fields, Field{key, value})
		}
	}
	return fields
}

func RenderMarkdown(fields []Field, body string) string {
	var output strings.Builder
	output.WriteString("---\n")
	for _, field := range fields {
		encoded, err := yaml.Marshal(map[string]any{field.Key: field.Value})
		if err != nil {
			panic(fmt.Sprintf("frontmatter serialization failed: %v", err))
		}
		output.Write(encoded)
	}
	output.WriteString("---\n\n")
	output.WriteString(strings.TrimLeft(body, "\n"))
	return trailingNewline(output.String())
}
