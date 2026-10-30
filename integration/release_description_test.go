package integration

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseDescriptionIncludesInstallCommandsAndAssets(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../.goreleaser.yaml")
	if err != nil {
		t.Fatalf("read GoReleaser configuration: %v", err)
	}

	configuration := string(contents)
	required := []string{
		"## Install agentpack {{ .Version }}",
		"https://github.com/olegpustovit/agentpack/releases/download/{{ .Tag }}/agentpack-installer.sh | sh",
		`powershell -ExecutionPolicy ByPass -c "irm https://github.com/olegpustovit/agentpack/releases/download/{{ .Tag }}/agentpack-installer.ps1 | iex"`,
		"./scripts/render-installers.sh {{ .Version }} ./.release-assets/installers",
		"glob: ./.release-assets/installers/agentpack-installer.sh",
		"glob: ./.release-assets/installers/agentpack-installer.ps1",
		"name_template: checksums.txt",
		"write only to `$HOME/.local/bin`",
		"do not modify shell profiles or `PATH`",
	}
	for _, expected := range required {
		if !strings.Contains(configuration, expected) {
			t.Errorf("GoReleaser configuration is missing release requirement %q", expected)
		}
	}
}

func TestReleaseGateUsesLatestInstallers(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../.github/workflows/agent-config-gate.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(contents)
	for _, expected := range []string{
		"release:\n    types: [published]",
		"workflows: [Release]",
		"types: [completed]",
		"github.event.workflow_run.conclusion == 'success'",
		"github.event.workflow_run.event == 'push'",
		"https://github.com/olegpustovit/agentpack/releases/latest/download/agentpack-installer.sh | sh",
		"https://github.com/olegpustovit/agentpack/releases/latest/download/agentpack-installer.ps1 | iex",
		"run: agentpack --version",
	} {
		if !strings.Contains(workflow, expected) {
			t.Errorf("release gate is missing %q", expected)
		}
	}
	if strings.Contains(workflow, "releases/download/") {
		t.Error("release gate must not pin an old installer")
	}
}

func TestReleaseBuildsGatewayUIBeforeEmbedding(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "hooks:\n    - make ui-stage\n") {
		t.Error("GoReleaser must run make ui-stage before building the embedded gateway UI")
	}
}
