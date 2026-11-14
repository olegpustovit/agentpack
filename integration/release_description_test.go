package integration

import (
	"os"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
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

func TestReleaseCachedLibrariesStillRunPlatformGates(t *testing.T) {
	contents, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If string `yaml:"if"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatal(err)
	}
	for job, dependency := range map[string]string{"codex-windows": "crsqlite", "goreleaser": "codex-windows", "smoke": "goreleaser", "publish": "smoke"} {
		condition := workflow.Jobs[job].If
		if !strings.Contains(condition, "always()") || !strings.Contains(condition, "needs."+dependency+".result == 'success'") {
			t.Errorf("%s must run after a successful %s even when the source-build ancestor was skipped: %s", job, dependency, condition)
		}
	}
}
