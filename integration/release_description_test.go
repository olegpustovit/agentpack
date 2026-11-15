package integration

import (
	"os"
	"slices"
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
			If    string   `yaml:"if"`
			Needs []string `yaml:"needs"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatal(err)
	}
	for job, dependencies := range map[string][]string{"codex-windows": {"crsqlite"}, "goreleaser": {"crsqlite"}, "smoke": {"goreleaser"}, "publish": {"smoke", "codex-windows"}} {
		condition := workflow.Jobs[job].If
		for _, dependency := range dependencies {
			if !slices.Contains(workflow.Jobs[job].Needs, dependency) || !strings.Contains(condition, "always()") || !strings.Contains(condition, "needs."+dependency+".result == 'success'") {
				t.Errorf("%s must require a successful %s even when the source-build ancestor was skipped: %s", job, dependency, condition)
			}
		}
	}
	if slices.Contains(workflow.Jobs["goreleaser"].Needs, "codex-windows") {
		t.Fatal("independent platform checks must run concurrently")
	}

}

func TestReleaseRunsFullSuiteOnceAfterUIStaging(t *testing.T) {
	contents, err := os.ReadFile("../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct{ Before struct{ Hooks []string } }
	if err := yaml.Unmarshal(contents, &config); err != nil {
		t.Fatal(err)
	}
	staged, tests := false, 0
	for _, hook := range config.Before.Hooks {
		if hook == "make ui-stage" {
			staged = true
		}
		if strings.HasPrefix(hook, "go test ./...") {
			tests++
			if !staged {
				t.Fatal("full suite must see the staged UI")
			}
		}
	}
	if tests != 1 {
		t.Fatalf("release before hooks have %d full suites, want one", tests)
	}
	contents, err = os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct{ Steps []struct{ Run string } }
	}
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, step := range workflow.Jobs["goreleaser"].Steps {
		if strings.Contains(step.Run, "go test ./...") {
			t.Fatal("workflow duplicates GoReleaser's full suite")
		}
	}
}
