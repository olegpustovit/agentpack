package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/environment"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/mcp"
)

// contentFixture builds a definition with a plugin package, modes, a pinned
// manifest MCP server, a contract and .agents content under root, with its own
// home, agentpack home and staging root.
func contentFixture(t *testing.T, root string) (definition string) {
	t.Helper()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("AGENTPACK_HOME", filepath.Join(root, "agentpack"))
	t.Setenv("AGENTPACK_STAGING_ROOT", filepath.Join(root, "staging"))
	for _, key := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "PI_CONFIG_DIR", "PI_CODING_AGENT_DIR", "OMP_PROFILE", "PI_PROFILE", "PI_CONFIG_FILES"} {
		t.Setenv(key, "")
	}
	for _, env := range []string{"CLAUDE_CODE_PATH", "OPENCODE_PATH", "CODEX_PATH", "CURSOR_AGENT_PATH", "GROK_PATH", "AGY_PATH", "OMP_PATH"} {
		t.Setenv(env, filepath.Join(root, "missing-binary"))
	}
	definition = filepath.Join(root, "definition")
	source := filepath.Join(root, "source")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(source, ".claude-plugin/plugin.json"), `{"name":"demo"}`)
	write(filepath.Join(source, "skills/a/SKILL.md"), "---\nname: a\ndescription: A\n---\nA\n")
	write(filepath.Join(source, "skills/b/SKILL.md"), "---\nname: b\ndescription: B\n---\nB\n")
	write(filepath.Join(source, "commands/c.md"), "---\ndescription: C\n---\nRun C\n")
	write(filepath.Join(source, "hooks/hooks.json"), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo start"}]}]}}`)
	write(filepath.Join(source, "mcp.json"), `{"mcpServers":{"one":{"url":"https://one.example/mcp"}}}`)
	write(filepath.Join(definition, "agentpack.toml"), `name = "golden"
version = "0.1.0"

[dependencies]
demo = { path = "../source" }

[modes.default]
base = "all"

[modes.a]
base = "none"
enable = ["package-path:demo:skills/a/SKILL.md", "mcp:files", ".agents:skills/x/SKILL.md"]

[mcp.servers.files]
command = "npx"
args = ["-y", "@example/files", "--token=secret"]
env = { KEY = "value" }
`)
	write(filepath.Join(definition, "contract.json"), `{"schema_version":1,"unknown_required":"warn"}`)
	write(filepath.Join(definition, ".agents/skills/x/SKILL.md"), "---\nname: x\ndescription: X\n---\nX\n")
	write(filepath.Join(definition, ".agents/skills/y/SKILL.md"), "---\nname: y\ndescription: Y\n---\nY\n")
	write(filepath.Join(definition, ".agents/mcp.json"), `{"mcpServers":{"dot":{"url":"https://dot.example/mcp"}}}`)
	key, commit, directory, _, err := cache.CopyPackageDirToCache(source, "golden")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := cache.TreeDigest(directory)
	if err != nil {
		t.Fatal(err)
	}
	command := "npx"
	server := mcp.Server{Command: &command, Args: []string{"-y", "@example/files", "--token=secret"}}
	lock := lockfile.PackLock{
		Meta:     lockfile.Meta{Name: "golden", Version: "0.1.0"},
		Packages: []lockfile.Package{{Module: "demo", Direct: true, URL: cache.FileURL(source), Kind: lockfile.PackagePlugin, CacheKey: key, Commit: commit, ContentHash: digest}},
		MCPServers: []lockfile.MCPServer{{
			Name: "files", Source: "manifest", Launcher: "npm", Status: "pinned", Definition: server.Definition(),
			Requested: "@example/files", Package: "@example/files", Version: "1.2.3", Integrity: "sha512-AAAA", Registry: "https://registry.npmjs.org",
		}},
	}
	if err := lock.Save(definition); err != nil {
		t.Fatal(err)
	}
	return definition
}

// contentIdentities are the host-path-free identities: EnvironmentDigest and
// preflight's source identity and generation ID.
func contentIdentities(t *testing.T, definition string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, item := range []struct {
		mode   string
		target base.Target
	}{{"", base.Claude}, {"", base.Codex}, {"a", base.Codex}} {
		value, err := EnvironmentDigest(definition, definition, item.mode, item.target)
		if err != nil {
			t.Fatal(err)
		}
		result["environment "+item.mode+"/"+string(item.target)] = value
		target := item.target
		report, err := NewService().Preflight(definition, PreflightOptions{Target: &target, WorkspaceRoot: definition, Mode: item.mode, Policy: environment.PolicyCI})
		if err != nil {
			t.Fatal(err)
		}
		result["source"] = report.SourceIdentity
		result["generation "+item.mode+"/"+string(item.target)] = report.GenerationID
	}
	return result
}

// TestContentIdentitiesGolden pins the content identities for a fixture and
// proves they do not depend on where the definition, cache or home live.
func TestContentIdentitiesGolden(t *testing.T) {
	want := map[string]string{}
	for _, root := range []string{filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "elsewhere", "second")} {
		got := contentIdentities(t, contentFixture(t, root))
		for name, value := range got {
			if golden, ok := contentGolden[name]; !ok || golden != value {
				t.Errorf("%s at %s = %q, want %q", name, root, value, golden)
			}
			want[name] = value
		}
	}
	if len(want) != len(contentGolden) {
		t.Errorf("identities = %v, want %v", want, contentGolden)
	}
}

var contentGolden = map[string]string{
	"environment /claude": "b63501f024e03e38fd241f01b5ecb03a874f6c882d0622fcbbec2bb550c6c861",
	"environment /codex":  "b63501f024e03e38fd241f01b5ecb03a874f6c882d0622fcbbec2bb550c6c861",
	"environment a/codex": "389c3ece23f78a223fc9fabd8c12a686f6c2b0b7260f0699838981f72423309b",
	"generation /claude":  "sha256:2dc2a53bcb65e2bbfb03144a85b754942640f27d378450bef4648da4b6cf956c",
	"generation /codex":   "sha256:9b109a31334e43b9d5a5014a5bd6c0d14f2fd9605ce80eb2c6fed71c680e6f03",
	"generation a/codex":  "sha256:29e441bce694d84edea4b6bd591698aee0fb67e079fa179dc460a9b5dba33308",
	"source":              "sha256-source-v1:aa2696f77e86e93106740acf2279456004ee9a145489c2582421293daa62192c",
}
