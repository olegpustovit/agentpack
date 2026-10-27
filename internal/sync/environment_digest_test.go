package sync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mode"
)

func TestEnvironmentDigestAcceptsUnconfiguredProject(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	root := t.TempDir()
	before, err := EnvironmentDigest(root, root, "", harness.Claude)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.WriteStub(root, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(root, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	after, err := EnvironmentDigest(root, root, "", harness.Claude)
	if err != nil || before != after {
		t.Fatalf("empty project bookkeeping changed inputs: %v", err)
	}
}

func TestEnvironmentDigestUsesModeInputsAndVerifiesIntegrity(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	project, source := t.TempDir(), t.TempDir()
	if err := manifest.WriteStub(project, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	write := func(relative, content string) {
		t.Helper()
		path := filepath.Join(source, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude-plugin/plugin.json", `{"name":"demo"}`)
	write("skills/a/SKILL.md", "---\nname: a\ndescription: A\n---\nA\n")
	write("skills/b/SKILL.md", "---\nname: b\ndescription: B\n---\nB\n")
	write("mcp.json", `{"mcpServers":{"one":{"url":"https://one.example/mcp"},"two":{"url":"https://two.example/mcp"}}}`)
	var cached string
	copyPackage := func() {
		t.Helper()
		key, commit, directory, err := cache.CopyPackageDirToCache(source, "digest-test")
		if err != nil {
			t.Fatal(err)
		}
		cached = directory
		digest, err := cache.TreeDigest(directory)
		if err != nil {
			t.Fatal(err)
		}
		lock := lockfile.PackLock{Meta: lockfile.Meta{Name: "test", Version: "0.1.0"}, Packages: []lockfile.Package{{Module: "demo", URL: cache.FileURL(source), Kind: lockfile.PackagePlugin, CacheKey: key, Commit: commit, ContentHash: digest}}}
		if err := lock.Save(project); err != nil {
			t.Fatal(err)
		}
	}
	copyPackage()
	if err := manifest.ReplaceModes(project, map[string]mode.Definition{
		"a": {Base: mode.BaseNone, Enable: []string{"package-path:demo:skills/a/SKILL.md"}},
		"m": {Base: mode.BaseNone, Enable: []string{"package-path:demo:mcp.json", "mcp:one"}},
		"h": {Base: mode.BaseNone, Enable: []string{"package-path:demo:hooks/hooks.json", "package-path:demo:.claude-plugin/plugin.json"}},
	}); err != nil {
		t.Fatal(err)
	}
	// The hooks selector must resolve before the first snapshot.
	write("hooks/hooks.json", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo first"}]}]}}`)
	copyPackage()
	digest := func(selected string) string {
		t.Helper()
		value, err := EnvironmentDigest(project, project, selected, harness.Claude)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	a, all := digest("a"), digest("")
	write("skills/b/SKILL.md", "---\nname: b\ndescription: B\n---\nChanged B\n")
	copyPackage()
	if digest("a") != a {
		t.Fatal("unselected skill restarted mode a")
	}
	if digest("") == all {
		t.Fatal("changed selected skill did not affect default mode")
	}
	m := digest("m")
	write("mcp.json", `{"mcpServers":{"one":{"url":"https://one.example/mcp"},"two":{"url":"https://changed.example/mcp"}}}`)
	copyPackage()
	if digest("m") != m || digest("a") != a {
		t.Fatal("disabled MCP server changed selected environment")
	}
	write("mcp.json", `{"mcpServers":{"one":{"url":"https://changed.example/mcp"},"two":{"url":"https://changed.example/mcp"}}}`)
	copyPackage()
	if digest("m") == m {
		t.Fatal("selected MCP server change was missed")
	}
	h := digest("h")
	write(".claude-plugin/plugin.json", `{"name":"demo","hooks":{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo inline"}]}]}}}`)
	copyPackage()
	if digest("h") == h {
		t.Fatal("inline hook change was missed")
	}
	if digest("a") != a {
		t.Fatal("disabled hook restarted skill-only mode")
	}
	if err := os.WriteFile(filepath.Join(cached, "skills/a/SKILL.md"), []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := EnvironmentDigest(project, project, "a", harness.Claude)
	var integrity *cache.IntegrityError
	if !errors.As(err, &integrity) {
		t.Fatalf("unverified cache was hashed: %v", err)
	}
}
