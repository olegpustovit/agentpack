package sync

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/environment"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
)

var updatePreflightGolden = flag.Bool("update-preflight", false, "rewrite testdata/preflight golden files")

// TestPreflightPerTargetGolden pins every harness's preflight report: inheritance notes,
// seeded user config, inherited config findings, planned overlay writes and capabilities.
func TestPreflightPerTargetGolden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("AGENTPACK_HOME", filepath.Join(home, "state"))
	t.Setenv("AGENTPACK_STAGING_ROOT", filepath.Join(home, "staging"))
	for _, key := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "PI_CONFIG_DIR", "PI_CODING_AGENT_DIR"} {
		t.Setenv(key, "")
	}
	for name, body := range map[string]string{
		".claude.json":                   `{"theme":"dark"}`,
		".claude/settings.json":          `{"model":"x"}`,
		".codex/config.toml":             "model = \"x\"\n",
		".grok/config.toml":              "model = \"x\"\n",
		".cursor/cli-config.json":        `{"version":1}`,
		".cursor/mcp.json":               `{"mcpServers":{}}`,
		".gemini/settings.json":          `{"a":1}`,
		".config/opencode/opencode.json": `{"model":"x"}`,
	} {
		path := filepath.Join(home, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, env := range []string{"CLAUDE_CODE_PATH", "OPENCODE_PATH", "CODEX_PATH", "CURSOR_AGENT_PATH", "GROK_PATH", "AGY_PATH", "OMP_PATH"} {
		t.Setenv(env, filepath.Join(home, "missing-binary"))
	}
	definition := filepath.Join(home, "definition")
	workspace := filepath.Join(home, "workspace")
	for _, directory := range []string{definition, workspace} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	for _, target := range base.AllTargets() {
		t.Run(string(target), func(t *testing.T) {
			report, err := NewService().Preflight(definition, PreflightOptions{Target: &target, WorkspaceRoot: workspace, Policy: environment.PolicyCI})
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got := strings.ReplaceAll(string(data), filepath.ToSlash(home), "$HOME")
			got = strings.ReplaceAll(got, home, "$HOME")
			path := filepath.Join("testdata", "preflight", string(target)+".json")
			if *updatePreflightGolden {
				if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got+"\n" != string(want) {
				t.Fatalf("preflight report for %s changed; rerun with -update-preflight and review the diff:\n%s", target, got)
			}
		})
	}
}
