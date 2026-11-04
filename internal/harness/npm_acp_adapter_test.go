package harness

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/mcp"
)

func TestSplitNPMPackageSpec(t *testing.T) {
	name, version, err := splitNPMPackageSpec("@agentclientprotocol/claude-agent-acp@0.100.0")
	if err != nil || name != "@agentclientprotocol/claude-agent-acp" || version != "0.100.0" {
		t.Fatalf("got %q %q %v", name, version, err)
	}
}

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestEnsureACPAdapterReusesResolvedSpecWithoutRegistry(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "node"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	requests := 0
	previousClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: testRoundTripper(func(request *http.Request) (*http.Response, error) {
		requests++
		body := `{"version":"0.100.0","dist":{"integrity":"sha512-YWJj"}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = previousClient })
	previousResolver := mcp.ResolveTree
	mcp.ResolveTree = func(context.Context, string, string, string, string) ([]lockfile.NPMTreeEntry, string, error) {
		return nil, "cli.js", nil
	}
	t.Cleanup(func() { mcp.ResolveTree = previousResolver })

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed; the adapter needs it")
	}
	spec := "@agentclientprotocol/claude-agent-acp@0.100.0"
	first, err := EnsureACPAdapterExecutable(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureACPAdapterExecutable(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || requests != 1 {
		t.Fatalf("first=%q second=%q registry requests=%d", first, second, requests)
	}
}

func TestAdapterSlot(t *testing.T) {
	got := adapterSlot("@agentclientprotocol/claude-agent-acp", "0.100.0")
	if got != "_agentclientprotocol_claude-agent-acp@0.100.0" {
		t.Fatalf("got %q", got)
	}
}

func TestACPAdapterCommandRunsScriptsThroughNode(t *testing.T) {
	script := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env node\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	if command := ACPAdapterCommand(script); command.Path != node || command.Args[1] != script {
		t.Fatalf("command = %v %v, want node then the script", command.Path, command.Args)
	}
}
