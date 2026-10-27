package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

func TestLaunchEnvironmentCacheUsesVerifiedSyncStamp(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("AGENTPACK_SESSION_WORKER", "1")
	const id = "session-cached-environment"
	t.Setenv("AGENTPACK_SESSION_ID", id)
	if err := os.MkdirAll(filepath.Join(home, "network", "sessions", id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := manifest.WriteStub(root, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(root, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	packContext := paths.Context{WorkspaceRoot: root, DefinitionRoot: root}
	stamp := func(value string) {
		t.Helper()
		if err := packSync.WriteLaunchDigest(root, "default", harness.Claude, value); err != nil {
			t.Fatal(err)
		}
	}
	record := func(skipped bool) string {
		t.Helper()
		if err := recordLaunchEnvironment(packContext, "default", harness.Claude, skipped); err != nil {
			t.Fatal(err)
		}
		digest, err := network.SessionEnvironment(home, id)
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	stamp("first-verified-sync")
	before := record(false)
	endpoint := "https://one.example/mcp"
	if err := manifest.AddMCPServer(root, "one", mcp.Server{URL: &endpoint}); err != nil {
		t.Fatal(err)
	}
	// A successful skipped sync is the caller's evidence that the stored stamp
	// still describes its inputs; recording must not re-read their content.
	if got := record(true); got != before {
		t.Fatal("fast launch rehashed effective inputs")
	}
	stamp("second-verified-sync")
	changed := record(true)
	if changed == before {
		t.Fatal("changed sync stamp reused stale environment")
	}
	endpoint = "https://two.example/mcp"
	if err := manifest.AddMCPServer(root, "two", mcp.Server{URL: &endpoint}); err != nil {
		t.Fatal(err)
	}
	full := record(false)
	if full == changed {
		t.Fatal("full sync reused its cached environment")
	}
	if got := record(true); got != full {
		t.Fatal("full sync did not refresh environment cache")
	}

	otherWorkspace := t.TempDir()
	packContext.WorkspaceRoot = otherWorkspace
	skill := filepath.Join(otherWorkspace, ".agents", "skills", "example", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: example\ndescription: Example\n---\nWorkspace-specific skill\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := record(true); got == full {
		t.Fatal("another workspace reused the first workspace's cache")
	}
}
