package codex

import (
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/artifacts"
	"github.com/pelletier/go-toml/v2"
)

func TestSubagentIsAlsoACodexCustomAgentFile(t *testing.T) {
	t.Parallel()
	artifact, err := artifacts.Parse("agents/research/company-researcher.md", "---\nname: Company Researcher\ndescription: Research a company\nallowed-tools:\n  - Read\n---\n\n# Company Researcher Agent\n\nResearch \"it\".\n", "")
	if err != nil {
		t.Fatal(err)
	}
	rendered := renderAgent(*artifact)
	if rendered.RelativePath != "agents/company-researcher.toml" {
		t.Fatalf("rendered = %#v", rendered)
	}
	var decoded map[string]string
	if err := toml.Unmarshal([]byte(rendered.Contents), &decoded); err != nil {
		t.Fatalf("%v\n%s", err, rendered.Contents)
	}
	if decoded["name"] != "company-researcher" || decoded["description"] != "Research a company" || !strings.Contains(decoded["developer_instructions"], "Research \"it\".") || len(decoded) != 3 {
		t.Fatalf("decoded = %#v", decoded)
	}
}
