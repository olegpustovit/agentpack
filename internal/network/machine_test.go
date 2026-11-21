package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAgentVersionsProbeOnceAndReportMissingAgents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the fake agent")
	}
	directory := t.TempDir()
	counter := filepath.Join(directory, "runs")
	claude := filepath.Join(directory, "claude")
	script := "#!/bin/sh\necho run >> " + counter + "\necho '2.1.140 (Claude Code)'\n"
	if err := os.WriteFile(claude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	for _, name := range []string{"CLAUDE_CODE_PATH", "CURSOR_AGENT_PATH", "CODEX_PATH", "OPENCODE_PATH", "GROK_PATH", "AGY_PATH", "OMP_PATH"} {
		t.Setenv(name, "")
	}
	for range 2 {
		agents := agentVersions(context.Background())
		if len(agents) != 7 || agents[0].Harness != "claude" || !agents[0].Installed || agents[0].Version != "2.1.140" {
			t.Fatalf("agents = %+v", agents)
		}
		for _, agent := range agents[1:] {
			if agent.Installed || agent.Version != "" {
				t.Fatalf("%s should be missing: %+v", agent.Harness, agent)
			}
		}
	}
	runs, _ := os.ReadFile(counter)
	if string(runs) != "run\n" {
		t.Fatalf("--version ran %q, want once", runs)
	}
}

func TestSystemResourcesReadsDisk(t *testing.T) {
	memory, free, total, err := systemResources(t.TempDir())
	if err != nil || memory == 0 || total == 0 || free > total {
		t.Fatalf("memory %d free %d total %d err %v", memory, free, total, err)
	}
}

func TestLatestReleaseIsCachedAndValidated(t *testing.T) {
	calls := 0
	tag := "v0.9.3"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://github.com/olegpustovit/agentpack/releases/tag/` + tag + `"}`))
	}))
	defer server.Close()
	previous := releaseURL
	releaseURL = server.URL
	reset := func() { latestRelease.version, latestRelease.url = "", "" }
	reset()
	t.Cleanup(func() { releaseURL = previous; reset() })

	for range 2 {
		version, _, err := fetchLatestRelease(context.Background())
		if err != nil || version != "0.9.3" {
			t.Fatalf("version %q err %v", version, err)
		}
	}
	if calls != 1 {
		t.Fatalf("GitHub was asked %d times, want 1", calls)
	}

	g, _ := identityGateway(t, t.TempDir(), nil)
	for _, current := range []string{"0.9.3", "0.10.0"} {
		g.options.Version = current
		if version, status, err := g.updateTarget(t.Context()); version != "" || status != http.StatusConflict || err == nil {
			t.Fatalf("running %s: target %q status %d error %v", current, version, status, err)
		}
	}
	if _, err := g.auth.store.DB.Exec("INSERT INTO mesh_nodes(node_id,name,app_version) VALUES ('peer','Peer','0.10.1-dev.10+abc')"); err != nil {
		t.Fatal(err)
	}
	if version, status, err := g.updateTarget(t.Context()); version != "0.10.1-dev.10" || status != http.StatusOK || err != nil {
		t.Fatalf("mesh target %q status %d error %v", version, status, err)
	}

	reset()
	tag = "v1.2.3; rm -rf /"
	if _, _, err := fetchLatestRelease(context.Background()); err == nil {
		t.Fatal("accepted a tag that is not a version")
	}
}
