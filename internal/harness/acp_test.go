package harness_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func TestHarnessACPLaunchCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Codex launch keeps its generation lease open for the agent, and Windows cannot remove an open file at cleanup")
	}
	project := t.TempDir()
	stage := t.TempDir()
	home := t.TempDir()
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "stub")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(binDir, "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	npx := filepath.Join(binDir, "npx")
	if err := os.WriteFile(npx, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", home)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", stage)
	preloadACPAdapters(t)
	shared, err := paths.SharedGrokCredentialsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"CLAUDE_CODE_PATH", "OPENCODE_PATH", "CODEX_PATH", "GROK_PATH", "AGY_PATH", "CURSOR_AGENT_PATH", "OMP_PATH"} {
		t.Setenv(key, binary)
	}
	effective := mode.ImplicitEffective()
	ctx := base.LaunchContext{ProjectRoot: project, WorkspaceRoot: project, Mode: effective, Yolo: true}
	root, err := paths.StagingOpenCodeDirForMode(project, effective.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range registry.All() {
		t.Run(string(candidate.ID()), func(t *testing.T) {
			launch, err := candidate.ACP(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if launch.Cmd == nil {
				t.Fatal("nil Cmd")
			}
			if launch.Cmd.Path == "" {
				t.Fatal("empty Cmd.Path")
			}
		})
	}
}

type acpRegistryRoundTripper func(*http.Request) (*http.Response, error)

func (f acpRegistryRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func preloadACPAdapters(t *testing.T) {
	t.Helper()
	previousClient, previousResolver := http.DefaultClient, mcp.ResolveTree
	requests := 0
	http.DefaultClient = &http.Client{Transport: acpRegistryRoundTripper(func(request *http.Request) (*http.Response, error) {
		requests++
		parts := strings.Split(strings.TrimRight(request.URL.Path, "/"), "/")
		version := parts[len(parts)-1]
		body := fmt.Sprintf(`{"version":%q,"dist":{"integrity":"sha512-YWJj"}}`, version)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	mcp.ResolveTree = func(context.Context, string, string, string, string) ([]lockfile.NPMTreeEntry, string, error) {
		return nil, "cli.js", nil
	}
	t.Cleanup(func() {
		http.DefaultClient = previousClient
		mcp.ResolveTree = previousResolver
	})
	for _, packageSpec := range []string{
		"@agentclientprotocol/claude-agent-acp@0.100.0",
		"@agentclientprotocol/codex-acp@2.5.0",
	} {
		if _, err := base.EnsureACPAdapterExecutable(context.Background(), packageSpec); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 2 {
		t.Fatalf("initial registry requests = %d, want 2", requests)
	}
}
