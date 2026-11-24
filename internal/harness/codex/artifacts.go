package codex

import (
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/olegpustovit/agentpack/internal/artifacts"
)

var artifactLayout = artifacts.Layout{
	NativeKinds:     []artifacts.Kind{artifacts.Skill},
	SkillKeys:       artifacts.SkillExtraKeys,
	AdditionalAgent: renderAgent,
}

// renderAgent renders a subagent as a Codex custom agent file,
// agents/<name>.toml with name, description and developer_instructions, the
// form Codex reads from $CODEX_HOME/agents and .codex/agents. The agent's
// file name is its Codex name, so that it is a plain identifier.
func renderAgent(artifact artifacts.Markdown) artifacts.Rendered {
	name := strings.TrimSuffix(filepath.Base(artifact.NestedPath()), filepath.Ext(artifact.NestedPath()))
	encoded, err := toml.Marshal(map[string]any{
		"name":                   name,
		"description":            artifact.Description,
		"developer_instructions": strings.TrimSpace(artifact.Body) + "\n",
	})
	if err != nil {
		return artifacts.Rendered{}
	}
	return artifacts.Rendered{RelativePath: "agents/" + name + ".toml", Contents: string(encoded)}
}
