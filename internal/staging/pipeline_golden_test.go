package staging

import (
	"encoding/json"
	"flag"
	"github.com/olegpustovit/agentpack/internal/harness/grok"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/cache"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
)

var updateStagedGolden = flag.Bool("update-staged", false, "rewrite testdata/staged golden files")

func TestPipelineStagedTreePerHarnessGolden(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("golden files use Unix paths and symlinks")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".local", "share"),
		"AGENTPACK_HOME": filepath.Join(home, "state"), "AGENTPACK_STAGING_ROOT": filepath.Join(home, "staging"),
		"AGENTPACK_DOT_AGENTS": "1", "AGENTPACK_KEEP_ATTRIBUTION": "",
		"CODEX_HOME": "", "GROK_HOME": "", "OPENCODE_CONFIG": "", "OPENCODE_CONFIG_DIR": "",
		"PI_CONFIG_DIR": "", "PI_CODING_AGENT_DIR": "",
	} {
		t.Setenv(key, value)
	}
	const hook = `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo pack"}]}]}}`
	writeTree(t, home, map[string]string{
		".codex/config.toml":            "model = \"fixture\"\n",
		".codex/hooks.json":             strings.Replace(hook, "echo pack", "echo user", 1),
		"state/shared/codex/logged-out": "logout\n", // avoid consulting the OS keyring
		".grok/config.toml":             "[plugins]\nenabled = ['another-plugin']\n",
	})
	pkg := lockfile.Package{Module: "github.com/example/demo", Kind: lockfile.PackagePlugin, CacheKey: "fixture", Direct: true}
	cacheRoot, err := cache.EntryDir(pkg.CacheKey)
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, cacheRoot, map[string]string{
		".claude-plugin/plugin.json": `{"name":"demo","version":"1.0.0"}`,
		"commands/review.md":         "---\nname: Review\ndescription: Review a change\nargument-hint: file\npermission: ask\nmodel: sonnet\n---\nReview it.\n",
		"agents/team/check.md":       "---\ndescription: Check work\ncolor: blue\nmodel: sonnet\ntools: Read, Grep\ndisallowedTools: Write\nthinkingLevel: high\n---\nCheck it.\n",
		"skills/review/SKILL.md":     "---\nname: review\ndescription: Read a change\nlicense: MIT\n---\nRead it.\n",
		"skills/review/example.txt":  "Supporting file\n",
		"rules/team/check.mdc":       "---\ndescription: Check sources\nglobs: ['**/*.go', '**/*.md']\n---\nCheck sources.\n",
		"rules/always.mdc":           "---\ndescription: Always check\nalwaysApply: true\n---\nAlways check.\n",
		"hooks/hooks.json":           hook,
	})
	for _, target := range base.AllTargets() {
		t.Run(string(target), func(t *testing.T) {
			project := filepath.Join(home, string(target), "project")
			workspace := filepath.Join(home, string(target), "workspace")
			if err := os.MkdirAll(project, 0o755); err != nil {
				t.Fatal(err)
			}
			writeTree(t, workspace, map[string]string{
				".agents/claude/commands/local-claude.md": "Local Claude command\n",
				".agents/codex/local.toml":                "theme = 'dark'\n",
				".agents/commands/local.md":               "Local command\n",
				".agents/agents/team/local-agent.md":      "Local agent\n",
				".agents/rules/team/local.mdc":            "---\nalwaysApply: true\n---\nLocal rule.\n",
				".agents/skills/local/SKILL.md":           "---\nname: local\n---\nLocal skill.\n",
				".claude/skills/review/SKILL.md":          "Project copy wins\n",
			})
			lock := lockfile.EmptyForProject(project)
			lock.Packages = []lockfile.Package{pkg}
			pipeline := Pipeline{ProjectRoot: project, WorkspaceRoot: workspace, Lock: lock, Mode: mode.ImplicitEffective(), Target: &target}
			if _, err := pipeline.Rebuild(); err != nil {
				t.Fatal(err)
			}
			candidate, err := registry.ByTarget(target)
			if err != nil {
				t.Fatal(err)
			}
			root, err := candidate.Root(pipeline.context())
			if err != nil {
				t.Fatal(err)
			}
			stage, err := paths.StagingRootForMode(project, "default")
			if err != nil {
				t.Fatal(err)
			}
			state, err := paths.ProjectStateDir(project)
			if err != nil {
				t.Fatal(err)
			}
			if target == base.Codex {
				t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(root)) })
			}
			normalize := strings.NewReplacer(root, "$ROOT", stage, "$STAGE", state, "$STATE", home, "$HOME")
			trees := map[string]map[string]string{"stage": snapshotGoldenTree(t, stage, normalize)}
			switch target {
			case base.Codex:
				trees["codex-home"] = snapshotGoldenTree(t, root, normalize)
			case base.Grok:
				grokHome, err := grok.StagingHome(project, "default")
				if err != nil {
					t.Fatal(err)
				}
				trees["grok-home"] = snapshotGoldenTree(t, grokHome, normalize)
			}
			golden := filepath.Join("testdata", "staged", string(target)+".json")
			data, err := json.MarshalIndent(trees, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, '\n')
			if *updateStagedGolden {
				writeTree(t, ".", map[string]string{golden: string(data)})
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != string(data) {
				t.Fatalf("staged tree changed for %s; compare with %s:\n%s", target, golden, data)
			}
		})
	}
}

func snapshotGoldenTree(t *testing.T, root string, normalize *strings.Replacer) map[string]string {
	t.Helper()
	files := make(map[string]string)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			files[relative+"/"] = ""
		case entry.Type()&fs.ModeSymlink != 0:
			destination, err := os.Readlink(path)
			if err != nil {
				return err
			}
			files[relative+" ->"] = normalize.Replace(destination)
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[relative] = normalize.Replace(string(data))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
