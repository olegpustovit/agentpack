package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/environment"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/claude"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/staging"
)

func TestPreflightDoesNotMutateLockOrCheckout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	definition := t.TempDir()
	workspace := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	lockBefore, err := os.ReadFile(paths.LockPath(definition))
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(workspace, "untouched.txt")
	if err := os.WriteFile(marker, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := base.Claude
	report, err := NewService().Preflight(definition, PreflightOptions{
		Target:         &target,
		Policy:         environment.PolicyLocal,
		StrictExternal: true,
		WorkspaceRoot:  workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.DefinitionRoot == "" || report.WorkspaceRoot != workspace {
		t.Fatalf("report roots: %#v", report)
	}

	lockAfter, err := os.ReadFile(paths.LockPath(definition))
	if err != nil {
		t.Fatal(err)
	}
	if string(lockBefore) != string(lockAfter) {
		t.Fatal("preflight mutated pack.lock")
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "untouched.txt" {
		t.Fatalf("workspace changed: %#v", entries)
	}
}

func TestPreflightDoesNotNormalizeCachedMarketplace(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	definition := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	root, err := cache.EntryDir("marketplace-cache")
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".agents/plugins/marketplace.json", `{"plugins":[{"name":"demo","source":{"source":"local","path":"./plugins/demo"}}]}`)
	write("plugins/demo/.claude-plugin/plugin.json", `{"name":"demo"}`)
	lock := lockfile.PackLock{Packages: []lockfile.Package{{
		Module: "github.com/acme/demo", Kind: lockfile.PackagePlugin, Owner: "acme", Repo: "demo", Commit: strings.Repeat("a", 40), CacheKey: "marketplace-cache",
	}}}
	if err := lock.Save(definition); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, ".agents", "plugins", "marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	target := base.Claude
	report, err := NewService().Preflight(definition, PreflightOptions{Target: &target, WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var cacheFinding bool
	for _, finding := range report.Findings {
		cacheFinding = cacheFinding || finding.Code == "CACHE_MISSING" || finding.Code == "CACHE_INTEGRITY"
	}
	if !cacheFinding {
		t.Fatalf("missing cache finding: %#v", report.Findings)
	}
	after, err := os.ReadFile(filepath.Join(root, ".agents", "plugins", "marketplace.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("preflight changed marketplace input: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude-plugin", "plugin.json")); !os.IsNotExist(err) {
		t.Fatalf("preflight materialized a cache manifest: %v", err)
	}
}

func TestPreflightPlannedWritesMatchStrictSyncRefusal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	definition := t.TempDir()
	workspace := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	target := base.Cursor
	report, err := NewService().Preflight(definition, PreflightOptions{
		Target: &target, Policy: environment.PolicyLocal, StrictExternal: true, WorkspaceRoot: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.PlannedWrites) == 0 {
		t.Fatal("expected planned overlay write")
	}
	_, syncErr := NewService().Sync(context.Background(), definition, SyncOptions{
		Target: &target, WorkspaceRoot: workspace, StrictExternal: true,
	})
	if syncErr == nil {
		t.Fatal("strict sync should refuse cursor overlay")
	}
	for _, write := range report.PlannedWrites {
		if !strings.Contains(syncErr.Error(), "workspace overlay") && !strings.Contains(syncErr.Error(), "strict-external") {
			t.Fatalf("sync error %q should mention strict overlay refusal (planned %v)", syncErr, report.PlannedWrites)
		}
		_ = write
	}
}

func TestPreflightStrictExternalFlagsCursorOverlay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	definition := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	target := base.Cursor
	report, err := NewService().Preflight(definition, PreflightOptions{
		Target:         &target,
		Policy:         environment.PolicyLocal,
		StrictExternal: true,
		WorkspaceRoot:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("expected blocking finding for cursor under strict-external")
	}
	found := false
	for _, finding := range report.Findings {
		if finding.Code == "WORKSPACE_WRITE_REQUIRED" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing WORKSPACE_WRITE_REQUIRED: %#v", report.Findings)
	}
}

func TestRestoreFrozenLeavesLockBytesUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	definition := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.LockPath(definition))
	if err != nil {
		t.Fatal(err)
	}
	if err := NewService().RestoreFrozen(context.Background(), definition); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(paths.LockPath(definition))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("restore mutated pack.lock")
	}
}

func TestRestoreFrozenFailsMissingInputWithoutLockChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	definition := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	lock := lockfile.PackLock{LockfileVersion: 2, Packages: []lockfile.Package{{
		Module: "missing-local", Direct: true, Kind: lockfile.PackageSkill,
		Owner: "path", Repo: "missing", Path: "skill",
		Commit: "local", CacheKey: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		URL: "file:///no/such/locked/skill/source",
	}}}
	if err := lock.Save(definition); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.LockPath(definition))
	if err != nil {
		t.Fatal(err)
	}
	err = NewService().RestoreFrozen(context.Background(), definition)
	if err == nil {
		t.Fatal("expected missing-input failure")
	}
	after, err := os.ReadFile(paths.LockPath(definition))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed restore mutated pack.lock")
	}
}

func TestRelocatedDefinitionsShareSourceIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	first := t.TempDir()
	if err := manifest.WriteStub(first, "portable", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(first, "portable", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	ws1, ws2 := t.TempDir(), t.TempDir()
	report1, err := NewService().Preflight(first, PreflightOptions{WorkspaceRoot: ws1})
	if err != nil {
		t.Fatal(err)
	}
	second := t.TempDir()
	for _, name := range []string{paths.ManifestName, paths.LockfileName} {
		data, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(second, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report2, err := NewService().Preflight(second, PreflightOptions{WorkspaceRoot: ws2})
	if err != nil {
		t.Fatal(err)
	}
	if report1.SourceIdentity == "" || report1.SourceIdentity != report2.SourceIdentity {
		t.Fatalf("source identity not portable: %q vs %q", report1.SourceIdentity, report2.SourceIdentity)
	}
	if report1.LockDigest == "" || report1.LockDigest != report2.LockDigest {
		t.Fatalf("lock digest not portable: %q vs %q", report1.LockDigest, report2.LockDigest)
	}
	if err := environment.SaveBinding(environment.Binding{
		Environment: "portable", DefinitionRoot: second, WorkspaceRoot: ws2,
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(ws2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("relocated bind wrote checkout: %#v", entries)
	}
}

func TestPreflightFlagsAmbientUserSkill(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	definition := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	cacheKey := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	lock := lockfile.PackLock{LockfileVersion: 2, Packages: []lockfile.Package{{
		Module: "github.com/example/skills/helper", Direct: true, Kind: lockfile.PackageSkill,
		Owner: "example", Repo: "skills", Path: "helper", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CacheKey: cacheKey,
	}}}
	if err := lock.Save(definition); err != nil {
		t.Fatal(err)
	}
	cacheSkill := filepath.Join(home, "cache", cacheKey)
	if err := os.MkdirAll(cacheSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheSkill, "SKILL.md"), []byte("---\nname: helper\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, native := range []string{".claude", ".grok"} {
		if err := os.RemoveAll(filepath.Join(home, ".claude")); err != nil {
			t.Fatal(err)
		}
		nativeSkill := filepath.Join(home, native, "skills", "helper")
		if err := os.MkdirAll(nativeSkill, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nativeSkill, "SKILL.md"), []byte("# helper\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		targets := append([]base.Target{""}, base.AllTargets()...)
		for _, target := range targets {
			t.Run(native+"/"+string(target), func(t *testing.T) {
				options := PreflightOptions{Policy: environment.PolicyCI, StrictExternal: true, WorkspaceRoot: t.TempDir()}
				if target != "" {
					options.Target = &target
				}
				report, err := NewService().Preflight(definition, options)
				if err != nil {
					t.Fatal(err)
				}
				wantOmitted := target == base.Grok || target == base.Claude && native == ".claude"
				found := false
				for _, finding := range report.Findings {
					if finding.Code == "CONFIG_SHADOWED" && finding.Source == "helper" {
						found = true
						if finding.Severity != environment.SeverityViolation {
							t.Errorf("collision must violate CI policy: %#v", finding)
						}
						if finding.Target != string(base.Grok) && !(native == ".claude" && finding.Target == string(base.Claude)) {
							t.Errorf("collision reported for unrelated target: %#v", finding)
						}
					}
				}
				if found != (wantOmitted || target == "") {
					t.Errorf("collision findings: %#v", report.Findings)
				}
				foundArtifact := false
				for _, artifact := range report.Artifacts {
					if artifact.Name == "helper" && artifact.Module == "github.com/example/skills/helper" {
						foundArtifact = true
						if artifact.Omitted != wantOmitted {
							t.Errorf("incorrect omission: %#v", artifact)
						}
					}
				}
				if !foundArtifact {
					t.Fatal("pack skill absent from report")
				}
				if target == "" && report.OverallStatus != "violated" {
					t.Fatalf("status=%s findings=%#v", report.OverallStatus, report.Findings)
				}
			})
		}
	}
}

func TestSourceIdentityStableAcrossRelocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	first := t.TempDir()
	if err := manifest.WriteStub(first, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(first, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	lock, err := lockfile.Load(first)
	if err != nil {
		t.Fatal(err)
	}
	id1, err := sourceIdentity(first, lock)
	if err != nil {
		t.Fatal(err)
	}
	second := t.TempDir()
	for _, name := range []string{paths.ManifestName, paths.LockfileName} {
		data, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(second, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	id2, err := sourceIdentity(second, lock)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == "" || id1 != id2 {
		t.Fatalf("identity not portable: %q vs %q", id1, id2)
	}
	if !strings.HasPrefix(id1, "sha256-source-v1:") {
		t.Fatalf("source identity has no versioned digest prefix: %q", id1)
	}
}

func TestSourceIdentityCanonicalizesSemanticDefinition(t *testing.T) {
	first := sourceIdentityForTest(t, `name = "demo"
version = "1.0.0"
description = "portable pack"

[dependencies]
"github.com/team/skills" = { tag = "v1" }

[modes.review]
base = "none"
enable = ["package:github.com/team/skills", "mcp:search"]
`)
	formatted := sourceIdentityForTest(t, `# Same parsed definition, different formatting and table order.
description="portable pack"
version="1.0.0"
name="demo"

[modes.review]
enable=["package:github.com/team/skills", "mcp:search"]
base="none"

[dependencies]
"github.com/team/skills"={tag="v1"}
`)
	if first != formatted {
		t.Fatalf("semantic TOML formatting changed source identity: %q != %q", first, formatted)
	}

	ordered := sourceIdentityForTest(t, `name = "demo"
version = "1.0.0"
[mcp.servers.search]
command = "runner"
args = ["--first", "one", "--second", "two"]
`)
	reordered := sourceIdentityForTest(t, `name = "demo"
version = "1.0.0"
[mcp.servers.search]
command = "runner"
args = ["--second", "two", "--first", "one"]
`)
	if ordered == reordered {
		t.Fatal("source identity ignored semantically significant MCP argument order")
	}
	pathOne := sourceIdentityForTest(t, `name = "demo"
version = "1.0.0"
[mcp.servers.search]
command = "/private/one/bin/runner"
args = ["--config", "/private/one/search.toml"]
cwd = "/private/one"
`)
	pathTwo := sourceIdentityForTest(t, `name = "demo"
version = "1.0.0"
[mcp.servers.search]
command = "/private/two/bin/runner"
args = ["--config", "/private/two/search.toml"]
cwd = "/private/two"
`)
	if pathOne != pathTwo {
		t.Fatalf("host-local paths changed source identity: %q != %q", pathOne, pathTwo)
	}
}

func TestSourceIdentityExcludesMCPSecretsButKeepsPublicArguments(t *testing.T) {
	definition := func(secret, token string, label string) string {
		return fmt.Sprintf(`name = "demo"
version = "1.0.0"
[mcp.servers.search]
command = "runner"
args = ["--token", %q, "--label", %q]
env = { API_TOKEN = %q }
headers = { Authorization = %q }
[mcp.servers.search.oauth]
client_id = "public-client"
client_secret = %q
scopes = ["read", "write"]
`, token, label, secret, secret, secret)
	}
	first := sourceIdentityForTest(t, definition("secret-one", "token-one", "stable"))
	changedSecrets := sourceIdentityForTest(t, definition("secret-two", "token-two", "stable"))
	changedPublicArgument := sourceIdentityForTest(t, definition("secret-two", "token-two", "changed"))
	if first != changedSecrets {
		t.Fatalf("MCP secret values affected source identity: %q != %q", first, changedSecrets)
	}
	if first == changedPublicArgument {
		t.Fatal("source identity ignored a non-secret MCP argument change")
	}
}

func TestSourceIdentityCanonicalizesContractMapsAndPreservesArrayOrder(t *testing.T) {
	contract := func(requirements string) string {
		return `{"schema_version":1,"unknown_required":"fail","requirements":[` + requirements + `]}`
	}
	one := `{"id":"first","target":"*","selector":{"category":"skill","name":"review","property":"presence"},"predicate":"present","minimum_evidence":"generated","severity":"error"}`
	two := `{"id":"second","target":"*","selector":{"category":"skill","name":"test","property":"presence"},"predicate":"present","minimum_evidence":"generated","severity":"error"}`
	first := sourceIdentityWithContract(t, contract(one+","+two))
	mapReordered := sourceIdentityWithContract(t, ` { "requirements" : [ `+one+`, `+two+` ], "unknown_required" : "fail", "schema_version" : 1 } `)
	if first != mapReordered {
		t.Fatalf("contract JSON formatting/map order changed source identity: %q != %q", first, mapReordered)
	}
	arrayReordered := sourceIdentityWithContract(t, contract(two+","+one))
	if first == arrayReordered {
		t.Fatal("source identity ignored contract requirement order")
	}
}

func TestSourceIdentityIncludesLockedMCPDependencyTree(t *testing.T) {
	root := t.TempDir()
	if err := manifest.WriteStub(root, "demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(root, "demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	lock, err := lockfile.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	lock.MCPServers = []lockfile.MCPServer{{Name: "search", Launcher: "npm", Status: lockfile.MCPPinned, Package: "@team/search", Version: "1.2.3", TreeDigest: "sha256:tree-a", Tree: []lockfile.NPMTreeEntry{{Path: "node_modules/@team/search", Version: "1.2.3", Integrity: "sha512:package-a"}}}}
	one, err := sourceIdentity(root, lock)
	if err != nil {
		t.Fatal(err)
	}
	lock.MCPServers[0].Tree[0].Integrity = "sha512:package-b"
	two, err := sourceIdentity(root, lock)
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("source identity ignored locked MCP dependency integrity")
	}
}

func TestSourceIdentityIncludesLockedPackageContentHash(t *testing.T) {
	root := t.TempDir()
	if err := manifest.WriteStub(root, "demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(root, "demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	lock, err := lockfile.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	lock.Packages = []lockfile.Package{{Module: "github.com/team/skills", Kind: lockfile.PackageSkill, Direct: true, Commit: "abc123", ContentHash: "sha256-tree-v1:first"}}
	one, err := sourceIdentity(root, lock)
	if err != nil {
		t.Fatal(err)
	}
	lock.Packages[0].ContentHash = "sha256-tree-v1:second"
	two, err := sourceIdentity(root, lock)
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("source identity ignored the locked package content hash")
	}
}

func sourceIdentityForTest(t *testing.T, manifestText string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(paths.ManifestPath(root), []byte(manifestText), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(root, "demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	lock, err := lockfile.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := sourceIdentity(root, lock)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func sourceIdentityWithContract(t *testing.T, contract string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(paths.ManifestPath(root), []byte("name = \"demo\"\nversion = \"1.0.0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "contract.json"), []byte(contract), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(root, "demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	lock, err := lockfile.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := sourceIdentity(root, lock)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestPreflightFlagsProjectAmbientSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	cacheKey := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	definition := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	lock := lockfile.PackLock{LockfileVersion: 2, Packages: []lockfile.Package{{
		Module: "github.com/example/skills/review", Direct: true, Kind: lockfile.PackageSkill,
		Owner: "example", Repo: "skills", Path: "review", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CacheKey: cacheKey,
	}}}
	if err := lock.Save(definition); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "cache", cacheKey), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cache", cacheKey, "SKILL.md"), []byte("---\nname: review\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	projectSkill := filepath.Join(workspace, ".claude", "skills", "review")
	if err := os.MkdirAll(projectSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectSkill, "SKILL.md"), []byte("# review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := NewService().Preflight(definition, PreflightOptions{
		Policy: environment.PolicyCI, WorkspaceRoot: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range report.Findings {
		if finding.Code == "AMBIENT_ARTIFACT" && finding.Source == "review" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing AMBIENT_ARTIFACT: %#v", report.Findings)
	}
}

func TestPreflightReceiptStaleOnLockDigestMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	definition := t.TempDir()
	workspace := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	receipt := environment.Receipt{
		SchemaVersion: 1, Phase: "probe", ReceiptID: "stale-demo",
		LockDigest: "sha256:deadbeef", Mode: "default",
		Target:        environment.ReceiptTarget{Adapter: "claude"},
		Coverage:      []environment.CoverageNote{{Category: "skill", Scope: "native_catalog", Completeness: "complete"}},
		OverallStatus: "ready",
	}
	if _, err := environment.SaveReceipt(workspace, receipt); err != nil {
		t.Fatal(err)
	}
	target := base.Claude
	report, err := NewService().Preflight(definition, PreflightOptions{
		Target: &target, WorkspaceRoot: workspace, ReceiptID: "stale-demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range report.Findings {
		if finding.Code == "RECEIPT_STALE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing RECEIPT_STALE: %#v", report.Findings)
	}
}

func TestPreflightContractNoWorkspaceWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	definition := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(definition, "contract.json")
	if err := os.WriteFile(contractPath, []byte(`{
		"schema_version": 1,
		"unknown_required": "fail",
		"requirements": [{
			"id": "no-checkout-config-writes",
			"target": "*",
			"selector": {"category": "planned_write", "property": "workspace_configuration"},
			"predicate": "no_workspace_write",
			"minimum_evidence": "generated",
			"severity": "error"
		}],
		"allowances": []
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	target := base.Claude
	report, err := NewService().Preflight(definition, PreflightOptions{
		Target: &target, Policy: environment.PolicyLocal, StrictExternal: true, WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ContractResults) != 1 || report.ContractResults[0].Result != "satisfied" {
		t.Fatalf("contract %#v", report.ContractResults)
	}
}

func TestReceiptFreshnessRejectsInputsPolicyBinaryAndFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("AGENTPACK_HOME", filepath.Join(home, "state"))
	definition, workspace := t.TempDir(), t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(home, "claude")
	if err := os.WriteFile(executable, []byte("fake-native-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_PATH", executable)
	target := base.Claude
	options := PreflightOptions{Target: &target, WorkspaceRoot: workspace, Policy: environment.PolicyLocal}
	report, err := NewService().Preflight(definition, options)
	if err != nil {
		t.Fatal(err)
	}
	receipt := environment.Receipt{SchemaVersion: 1, Phase: "probe", ReceiptID: "fresh", CreatedAt: time.Now().UTC(), SourceIdentity: report.SourceIdentity, LockDigest: report.LockDigest, WorkspaceDigest: report.WorkspaceDigest, GenerationID: report.GenerationID, PolicyDigest: report.PolicyDigest, Mode: report.Mode, Target: report.ExpectedTarget, Termination: environment.Termination{Status: "completed"}, OverallStatus: "ready", Properties: []environment.ObservedProperty{{Category: "skill", ArtifactID: "bundle/skills/review", Property: "presence", Value: true, Evidence: environment.EvidenceObserved, Method: "native_catalog"}}}
	receipt.Target.NativeVersion = "test-version"
	evidence := filepath.Join(home, "native-evidence.json")
	if err := os.WriteFile(evidence, []byte(`{"inventory":"review"}`), 0600); err != nil {
		t.Fatal(err)
	}
	evidenceDigest, err := environment.ExecutableIdentity(evidence)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Properties[0].EvidenceRef = evidence
	receipt.Properties[0].EvidenceDigest = evidenceDigest

	if _, err := environment.SaveReceipt(workspace, receipt); err != nil {
		t.Fatal(err)
	}
	options.ReceiptID = "fresh"
	check := func(wantCode string) {
		t.Helper()
		report, err := NewService().Preflight(definition, options)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range report.Findings {
			found = found || f.Code == wantCode
		}
		if !found {
			t.Fatalf("missing %s: %#v", wantCode, report.Findings)
		}
	}
	check("NATIVE_OBSERVED")
	options.Policy = environment.PolicyCI
	check("RECEIPT_STALE")
	options.Policy = environment.PolicyLocal
	if err := os.WriteFile(executable, []byte("fake-native-b"), 0o755); err != nil {
		t.Fatal(err)
	}
	check("RECEIPT_STALE")
	if err := os.WriteFile(executable, []byte("fake-native-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "CLAUDE.md"), []byte("changed native instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	check("RECEIPT_STALE")
	if err := os.Remove(filepath.Join(workspace, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, []byte(`{"inventory":"modified"}`), 0600); err != nil {
		t.Fatal(err)
	}
	check("OBSERVATION_UNAVAILABLE")
	if err := os.WriteFile(evidence, []byte(`{"inventory":"review"}`), 0600); err != nil {
		t.Fatal(err)
	}
	receipt.Termination.NativeExitCode = 17
	receipt.OverallStatus = "error"
	if _, err := environment.SaveReceipt(workspace, receipt); err != nil {
		t.Fatal(err)
	}
	check("PROBE_FAILED")
}

func TestStrictCICleanHomeDoesNotViolateInheritanceBoilerplate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("AGENTPACK_HOME", filepath.Join(home, "state"))
	definition := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(home, "claude")
	if err := os.WriteFile(exe, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_PATH", exe)
	target := base.Claude
	options := PreflightOptions{Target: &target, WorkspaceRoot: t.TempDir(), Policy: environment.PolicyCI, StrictExternal: true}
	report, err := NewService().Preflight(definition, options)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("clean CI falsely blocked: %#v", report.Findings)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"custom":"setting"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err = NewService().Preflight(definition, options)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("existing undeclared configuration did not block CI")
	}
}

func TestEffectivePlanFiltersModesAndUsesNativeRenderedProperties(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	key := strings.Repeat("a", 64)
	root := filepath.Join(home, "cache", key)
	if err := os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte(`{"name":"pack"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := []byte("---\nname: review\n---\nReview changes.\n")
	if err := os.WriteFile(filepath.Join(root, "skills", "review", "SKILL.md"), source, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "commands", "check.md"), []byte("Check code"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "agents", "team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", "team", "reviewer.md"), []byte("---\ndescription: Review\n---\nReview changes."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "agents", "ops"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", "ops", "reviewer.md"), []byte("---\ndescription: Ops review\n---\nReview operations."), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := lockfile.PackLock{Packages: []lockfile.Package{{Module: "github.com/team/pack", Kind: lockfile.PackagePlugin, CacheKey: key, Name: "pack"}}}
	selected, err := mode.NewEffective("review", mode.Definition{Base: mode.BaseAll, Disable: []string{"package-path:github.com/team/pack:skills/review/SKILL.md"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := base.Codex
	records, err := plannedArtifacts(lock, selected, t.TempDir(), t.TempDir(), &target, true)
	if err != nil {
		t.Fatal(err)
	}
	disabled, converted, nativeAgentOutput, nativeAgentOverride, nativeAgentOverwritten := false, false, false, false, false
	for _, record := range records {
		if record.Name == "review" {
			disabled = record.Omitted
		}
		if record.Name == "check" {
			converted = record.Kind == "skill" && record.OutputPath == "skills/check/SKILL.md" && record.Properties["rendered_digest"] != nil && record.Properties["source_digest"] != record.Properties["rendered_digest"]
		}
		if record.Name == "reviewer" && strings.HasPrefix(record.ID, "github.com/team/pack:agents/") {
			outputs, _ := record.Properties["additional_outputs"].([]string)
			nativeAgentOutput = nativeAgentOutput || len(outputs) == 1 && outputs[0] == "agents/reviewer.toml"
			overrides, _ := record.Properties["overrides_outputs"].([]string)
			overwritten, _ := record.Properties["overwritten_outputs"].([]string)
			nativeAgentOverride = nativeAgentOverride || strings.Contains(strings.Join(overrides, ","), "agents/reviewer.toml")
			nativeAgentOverwritten = nativeAgentOverwritten || strings.Contains(strings.Join(overwritten, ","), "agents/reviewer.toml")
		}
	}
	if !disabled || !converted || !nativeAgentOutput || !nativeAgentOverride || !nativeAgentOverwritten {
		t.Fatalf("mode/render plan mismatch: %#v", records)
	}
}

func TestPreflightDotAgentsUsesStagingPlanAndMarksNativeCodexSkill(t *testing.T) {
	workspace := t.TempDir()
	dot := filepath.Join(workspace, ".agents")
	for relative, contents := range map[string]string{
		"skills/local/SKILL.md": "---\nname: local\n---\nNative skill\n",
		"agents/review.md":      "---\ndescription: Review agent\n---\nAgent\n",
		"commands/review.md":    "---\ndescription: Review command\n---\nCommand\n",
		"commands/disabled.md":  "---\ndescription: Disabled\n---\nDisabled\n",
	} {
		path := filepath.Join(dot, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	effective, err := mode.NewEffective("dot", mode.Definition{Base: mode.BaseAll, Disable: []string{".agents:commands/disabled.md"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := base.Codex
	records, err := plannedArtifacts(lockfile.PackLock{}, effective, t.TempDir(), workspace, &target, true)
	if err != nil {
		t.Fatal(err)
	}
	var native, converted, omitted bool
	for _, record := range records {
		switch record.ID {
		case ".agents:skills/local/SKILL.md:codex":
			native = record.OutputPath == "" && record.Properties["native_source_path"] == ".agents/skills/local/SKILL.md" && record.Evidence == environment.EvidenceDeclared
		case ".agents:commands/review.md:codex":
			converted = record.OutputPath == "skills/review/SKILL.md" && record.Properties["rendered_digest"] != nil
		case ".agents:commands/disabled.md:codex":
			omitted = record.Omitted && record.OutputPath == ""
		}
	}
	if !native || !converted || !omitted {
		t.Fatalf("dot-agent plan mismatch: %#v", records)
	}
	t.Setenv("AGENTPACK_DOT_AGENTS", "0")
	records, err = plannedArtifacts(lockfile.PackLock{}, effective, t.TempDir(), workspace, &target, true)
	if err != nil {
		t.Fatal(err)
	}
	native, converted = false, false
	for _, record := range records {
		if record.ID == ".agents:skills/local/SKILL.md:codex" {
			native = record.Properties["native_source_path"] == ".agents/skills/local/SKILL.md" && !record.Omitted
		}
		if record.ID == ".agents:commands/review.md:codex" {
			converted = record.Omitted && record.Reason == "disabled by AGENTPACK_DOT_AGENTS"
		}
	}
	if !native || !converted {
		t.Fatalf("disabled overlay/native Codex report mismatch: %#v", records)
	}
}

func TestPreflightMCPUsesHarnessProjectionWithoutExposingValues(t *testing.T) {
	home, stagingRoot := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", stagingRoot)
	cacheKey := strings.Repeat("b", 64)
	pluginRoot, err := cache.EntryDir(cacheKey)
	if err != nil {
		t.Fatal(err)
	}
	for relative, content := range map[string]string{
		".claude-plugin/plugin.json": `{"name":"demo-plugin"}`,
		"mcp.json":                   `{"mcpServers":{"lookup":{"command":"node","args":["${CLAUDE_PLUGIN_ROOT}/scripts/run.js"]},"plugin_path":{"command":"node","args":["${PLUGIN_ROOT}/scripts/run.js"]}}}`,
		".lsp.json":                  `{"gopls":{"command":"gopls","extensionToLanguage":{".go":"go"}}}`,
		"scripts/run.js":             "console.log('ready')\n",
	} {
		path := filepath.Join(pluginRoot, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lock := lockfile.PackLock{Packages: []lockfile.Package{{Module: "github.com/acme/plugin", Kind: lockfile.PackagePlugin, CacheKey: cacheKey, Name: "demo-plugin"}}}
	for relative, content := range map[string]string{
		".config/opencode/opencode.json": `{"mcp":{"lookup":{"type":"local","command":["user"],"enabled":true}}}`,
		".codex/config.toml":             "[mcp_servers.lookup]\ncommand = \"user\"\n",
		".grok/config.toml":              "[plugins]\nenabled = [\"other\"]\n\n[mcp_servers.lookup]\ncommand = \"user\"\n",
		".cursor/mcp.json":               `{"mcpServers":{"lookup":{"command":"user"}}}`,
	} {
		path := filepath.Join(home, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range base.AllTargets() {
		definition := t.TempDir()
		if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
			t.Fatal(err)
		}
		manifestText := "[mcp.servers.manifest_secret]\ncommand = \"node\"\nargs = [\"--secret-argument\"]\n[mcp.servers.manifest_secret.env]\nTOKEN = \"top-secret-value\"\n"
		if err := os.WriteFile(paths.ManifestPath(definition), []byte(manifestText), 0o644); err != nil {
			t.Fatal(err)
		}
		dotPath := filepath.Join(definition, ".agents", "mcp.json")
		if err := os.MkdirAll(filepath.Dir(dotPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dotPath, []byte(`{"mcpServers":{"codex_overlay":{"command":"dot"}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		project, err := manifest.Load(definition)
		if err != nil {
			t.Fatal(err)
		}
		effective := mode.ImplicitEffective()
		entries, err := staging.CollectMCP(definition, lock, project, &effective)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (staging.Pipeline{ProjectRoot: definition, Lock: lock, Manifest: project, Mode: effective, Target: &target}).Rebuild(); err != nil {
			t.Fatalf("stage %s: %v", target, err)
		}
		records, err := plannedMCPArtifacts(lock, effective, definition, definition, &target)
		if err != nil {
			t.Fatalf("preflight %s: %v", target, err)
		}
		candidate, err := registry.ByTarget(target)
		if err != nil {
			t.Fatal(err)
		}
		projection, err := candidate.PlanMCP(entries, base.StageContext{ProjectRoot: definition, WorkspaceRoot: definition, Mode: effective})
		if err != nil {
			t.Fatalf("materialization plan %s: %v", target, err)
		}
		serverFound, dotFound := false, false
		for _, record := range records {
			if record.ID == "mcp:lookup:"+string(target) {
				wantWinner := "plugin"
				if target == base.OpenCode || target == base.Codex || target == base.Grok || target == base.Cursor {
					wantWinner = "user"
				}
				if record.Winner == wantWinner {
					serverFound = true
				}
			}
			if target == base.Codex && record.ID == "mcp:codex_overlay:codex" && record.Winner == ".agents" {
				dotFound = true
			}
			if record.Kind != "mcp_file" {
				continue
			}
			matched := false
			for _, file := range projection.Files {
				fileName := filepath.Base(file.Path)
				if strings.Contains(filepath.ToSlash(file.Path), "/.cursor/") {
					fileName = "fake-home/.cursor/" + fileName
				}
				if record.Name == fileName && record.Properties["scope"] == file.Scope {
					actual, err := os.ReadFile(file.Path)
					if err != nil {
						t.Fatalf("read staged %s MCP output %s: %v", target, file.Path, err)
					}
					var digest string
					if file.Scope == "mcp_servers" {
						digest, err = base.MCPTOMLScopeDigest(actual, file.Scope)
					} else {
						digest, err = base.MCPJSONScopeDigest(actual, file.Scope)
					}
					if err != nil {
						t.Fatal(err)
					}
					matched = digest == record.Properties["scope_digest"]
				}
			}
			if !matched {
				t.Fatalf("preflight %s digest differs from materialization projection: %#v vs %#v", target, record, projection.Files)
			}
		}
		if !serverFound {
			t.Fatalf("preflight %s did not report expected lookup winner", target)
		}
		if target == base.Codex && !dotFound {
			t.Fatal("Codex preflight did not report the .agents MCP overlay")
		}
		if target == base.Claude {
			settingsPath, err := claude.SettingsPath(base.StageContext{ProjectRoot: definition, WorkspaceRoot: definition, Mode: effective})
			if err != nil {
				t.Fatal(err)
			}
			settings, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, record := range records {
				if record.Kind == "settings_file" && record.ID == "settings:claude:agentpack-overlay" {
					found = digestBytes(settings) == record.Properties["rendered_digest"]
				}
			}
			if !found {
				t.Fatal("Claude settings report does not match staged overlay bytes")
			}
		}
		if target == base.Grok {
			grokHome, err := paths.StagingGrokHomeDirForMode(definition, effective.Name())
			if err != nil {
				t.Fatal(err)
			}
			config, err := os.ReadFile(filepath.Join(grokHome, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			digest, err := base.MCPTOMLScopeDigest(config, "plugins")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, record := range records {
				if record.ID == "settings:grok:plugins" {
					found = digest == record.Properties["scope_digest"]
				}
			}
			if !found {
				t.Fatal("Grok settings report does not match staged plugin settings")
			}
		}
		if target == base.Claude || target == base.OpenCode || target == base.Grok {
			lspEntries, err := staging.CollectLSP(lock, &effective)
			if err != nil {
				t.Fatal(err)
			}
			lspEntriesResolved, err := staging.PlanLSPRoots(definition, effective.Name(), lspEntries)
			if err != nil {
				t.Fatal(err)
			}
			lspProjection, err := candidate.PlanLSP(lspEntriesResolved, base.StageContext{ProjectRoot: definition, WorkspaceRoot: definition, Mode: effective})
			if err != nil {
				t.Fatalf("plan %s LSP: %v", target, err)
			}
			for _, file := range lspProjection.Files {
				actual, err := os.ReadFile(file.Path)
				if err != nil {
					t.Fatalf("read staged %s LSP %s: %v", target, file.Path, err)
				}
				found := false
				for _, record := range records {
					if record.Kind == "lsp_file" && record.Properties["target"] == string(target) && record.Name == filepath.Base(file.Path) {
						found = digestBytes(actual) == record.Properties["rendered_digest"]
					}
				}
				if !found {
					t.Fatalf("preflight %s LSP report differs from staged bytes: %#v", target, file.Path)
				}
			}
		}
		encoded, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "top-secret-value") || strings.Contains(string(encoded), "--secret-argument") {
			t.Fatalf("MCP report for %s exposed a configured value: %s", target, encoded)
		}
		if target == base.Claude {
			prior := make(map[string]any)
			for _, record := range records {
				if record.Kind == "mcp_file" {
					prior[record.ID] = record.Properties["scope_digest"]
				}
			}
			if _, err := (staging.Pipeline{ProjectRoot: definition, Lock: lock, Manifest: project, Mode: effective, Target: &target}).Rebuild(); err != nil {
				t.Fatalf("rebuild %s after generation rotation: %v", target, err)
			}
			next, err := plannedMCPArtifacts(lock, effective, definition, definition, &target)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range next {
				if record.Kind == "mcp_file" && prior[record.ID] != record.Properties["scope_digest"] {
					t.Fatalf("MCP scope digest changed across generation rotation for %s: %v -> %v", record.ID, prior[record.ID], record.Properties["scope_digest"])
				}
			}
		}
	}
}

func TestPreflightReportsStructuredMCPCollectionOmission(t *testing.T) {
	definition := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(definition, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(definition, ".agents", "mcp.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := plannedMCPArtifacts(lockfile.PackLock{}, mode.ImplicitEffective(), definition, definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Kind == "mcp_omission" && record.Name == ".agents/mcp.json" && record.Reason == "invalid MCP definition" {
			return
		}
	}
	t.Fatalf("preflight omitted the MCP collection warning: %#v", records)
}

func TestPreflightReportsMalformedLSPSourceWithoutRawContents(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	key := strings.Repeat("c", 64)
	root, err := cache.EntryDir(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lsp.json"), []byte(`{"token":"private-value"`), 0o600); err != nil {
		t.Fatal(err)
	}
	definition := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	lock := lockfile.PackLock{Packages: []lockfile.Package{{Module: "github.com/acme/plugin", Kind: lockfile.PackagePlugin, CacheKey: key}}}
	target := base.Claude
	records, err := plannedMCPArtifacts(lock, mode.ImplicitEffective(), definition, definition, &target)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Kind == "lsp_omission" && strings.Contains(record.Name, "github.com/acme/plugin") {
			if strings.Contains(record.Reason, "private-value") {
				t.Fatal("raw malformed LSP contents leaked")
			}
			return
		}
	}
	t.Fatalf("preflight omitted malformed LSP source diagnostic: %#v", records)
}

func TestContractWarningSeverityIsNonBlocking(t *testing.T) {
	report := environment.Report{}
	applyContract(&report, environment.Contract{SchemaVersion: 1, Requirements: []environment.Requirement{{ID: "optional", Predicate: "present", Selector: map[string]any{"category": "skill", "name": "missing"}, Severity: environment.SeverityWarning}}})
	if got := overallStatus(report); got != "ready_with_warnings" {
		t.Fatalf("warning status=%s", got)
	}
}

func TestPreflightDetectsMaterializationInputsChangedAfterSync(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("AGENTPACK_HOME", filepath.Join(home, "state"))
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	definition, workspace := t.TempDir(), t.TempDir()
	if err := manifest.WriteStub(definition, "materialized", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "materialized", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(home, "claude")
	if err := os.WriteFile(exe, []byte("fake-native"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_PATH", exe)
	target := base.Claude
	options := PreflightOptions{Target: &target, Policy: environment.PolicyLocal, WorkspaceRoot: workspace}
	if _, err := NewService().Sync(context.Background(), definition, SyncOptions{Target: &target, WorkspaceRoot: workspace, Frozen: true}); err != nil {
		t.Fatal(err)
	}
	stale := func(want bool) {
		t.Helper()
		report, err := NewService().Preflight(definition, options)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, finding := range report.Findings {
			found = found || finding.Code == "STAGING_INPUTS_STALE"
		}
		if found != want {
			t.Fatalf("stale=%t want %t findings=%#v", found, want, report.Findings)
		}
	}
	stale(false)
	original, err := os.ReadFile(paths.ManifestPath(definition))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ManifestPath(definition), append(original, []byte("\n[modes.default]\nbase=\"none\"\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	stale(true)
	if err := os.WriteFile(paths.ManifestPath(definition), original, 0600); err != nil {
		t.Fatal(err)
	}
	stale(false)
	options.WorkspaceRoot = t.TempDir()
	stale(true)
	options.WorkspaceRoot = workspace
	if err := os.MkdirAll(filepath.Join(workspace, ".agents", "skills", "review"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".agents", "skills", "review", "SKILL.md"), []byte("Review changes"), 0600); err != nil {
		t.Fatal(err)
	}
	stale(true)
}

// A project's own .agents skills are project content. Only a project skill that carries a
// locked skill's name is reported, and its absence leaves a never-probed workspace unknown.
func TestPreflightReportsOnlySameNamedAgentsSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		t.Fatal(err)
	}
	cacheKey := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	definition := t.TempDir()
	if err := manifest.WriteStub(definition, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	lock := lockfile.PackLock{LockfileVersion: 2, Packages: []lockfile.Package{{
		Module: "github.com/example/skills/review", Direct: true, Kind: lockfile.PackageSkill,
		Owner: "example", Repo: "skills", Path: "review", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CacheKey: cacheKey,
	}}}
	if err := lock.Save(definition); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "cache", cacheKey), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cache", cacheKey, "SKILL.md"), []byte("---\nname: review\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	writeSkill := func(name string) {
		t.Helper()
		directory := filepath.Join(workspace, ".agents", "skills", name)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sources := func() []string {
		t.Helper()
		target := base.Claude
		report, err := NewService().Preflight(definition, PreflightOptions{Policy: environment.PolicyCI, WorkspaceRoot: workspace, Target: &target, ReceiptID: "latest"})
		if err != nil {
			t.Fatal(err)
		}
		missing := false
		var found []string
		for _, finding := range report.Findings {
			missing = missing || finding.Code == "RECEIPT_MISSING"
			if finding.Code == "RECEIPT_STALE" {
				t.Fatalf("a never-probed workspace is not a stale receipt: %#v", finding)
			}
			if finding.Code == "AMBIENT_ARTIFACT" {
				found = append(found, finding.Source)
			}
		}
		if !missing {
			t.Fatalf("missing RECEIPT_MISSING: %#v", report.Findings)
		}
		return found
	}
	digest := func() string {
		t.Helper()
		target := base.Codex
		report, err := NewService().Preflight(definition, PreflightOptions{WorkspaceRoot: workspace, Target: &target})
		if err != nil {
			t.Fatal(err)
		}
		return report.WorkspaceDigest
	}
	before := digest()
	for _, root := range []string{".codex", ".opencode"} {
		directory := filepath.Join(workspace, root, "skills", "extra")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: extra\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		after := digest()
		if after == before {
			t.Fatalf("a skill added under %s/skills did not change the workspace digest", root)
		}
		before = after
		if err := os.RemoveAll(filepath.Join(workspace, root)); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill("house-style")
	if found := sources(); len(found) != 0 {
		t.Fatalf("the project's own skill was reported as a collision: %v", found)
	}
	writeSkill("review")
	if found := sources(); len(found) != 1 || found[0] != "review" {
		t.Fatalf("same-named project skill not reported exactly once: %v", found)
	}
}

// The plan must name what staging writes: a skill reached through a path the
// manifest declares, and a command that shares a skill's name on Codex.
func TestEffectivePlanFollowsDeclaredPathsAndConvertedNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	key := strings.Repeat("b", 64)
	root := filepath.Join(home, "cache", key)
	for relative, body := range map[string]string{
		".codex-plugin/plugin.json":     `{"name":"pack","skills":"./.codex/skills/"}`,
		".codex/skills/devops/SKILL.md": "---\nname: devops\ndescription: DevOps\n---\nDeploy.\n",
		"skills/build-bot/SKILL.md":     "---\nname: build-bot\ndescription: Skill\n---\nSkill body.\n",
		"commands/build-bot.md":         "---\ndescription: Command\n---\nCommand body.\n",
		"commands/modernize.md":         "---\ndescription: Start\nargument-hint: [system] [--source <path>]\n---\nBody.\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lock := lockfile.PackLock{Packages: []lockfile.Package{{Module: "github.com/team/pack", Kind: lockfile.PackagePlugin, CacheKey: key, Name: "pack"}}}
	outputs := func(target base.Target) map[string]string {
		records, err := plannedArtifacts(lock, mode.ImplicitEffective(), t.TempDir(), t.TempDir(), &target, true)
		if err != nil {
			t.Fatal(err)
		}
		result := make(map[string]string)
		for _, record := range records {
			if !record.Omitted && record.OutputPath != "" {
				result[record.OutputPath] = record.ID
			}
		}
		return result
	}
	codex := outputs(base.Codex)
	for output, id := range map[string]string{
		"skills/devops/SKILL.md":            "github.com/team/pack:.codex/skills/devops/SKILL.md",
		"skills/build-bot/SKILL.md":         "github.com/team/pack:skills/build-bot/SKILL.md",
		"skills/build-bot-command/SKILL.md": "github.com/team/pack:commands/build-bot.md",
		"skills/modernize/SKILL.md":         "github.com/team/pack:commands/modernize.md",
	} {
		if codex[output] != id {
			t.Fatalf("codex plan: %s comes from %q, want %q\n%v", output, codex[output], id, codex)
		}
	}
	claude := outputs(base.Claude)
	if claude["commands/build-bot.md"] == "" || claude["skills/devops/SKILL.md"] == "" || claude["skills/build-bot-command/SKILL.md"] != "" {
		t.Fatalf("claude plan = %v", claude)
	}
}

func TestFingerprintInputsDanglingSymlink(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "skills")
	if err := os.Symlink(".agents/skills", link); err != nil {
		t.Skip(err)
	}
	before, err := fingerprintInputs([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".agents", "skills"), 0755); err != nil {
		t.Fatal(err)
	}
	after, err := fingerprintInputs([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("creating the missing target must change the fingerprint")
	}
}
