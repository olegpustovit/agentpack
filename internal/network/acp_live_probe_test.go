package network

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
)

// TestLiveACPProbe runs one real ACP turn against an installed agent and
// writes its log, for scripts/acp-conformance. It needs a synced project and
// the agent's own login, sends one prompt, and is skipped unless asked for.
func TestLiveACPProbe(t *testing.T) {
	harness, project, out := os.Getenv("AGENTPACK_ACP_PROBE"), os.Getenv("AGENTPACK_ACP_PROBE_PROJECT"), os.Getenv("AGENTPACK_ACP_PROBE_OUT")
	if harness == "" || project == "" || out == "" {
		t.Skip("set AGENTPACK_ACP_PROBE, AGENTPACK_ACP_PROBE_PROJECT and AGENTPACK_ACP_PROBE_OUT")
	}
	session, stop, err := startACPConversation(WorkerLaunch{Directory: project, Session: SessionInfo{Harness: harness, Directory: project, DefinitionRoot: project, Transport: "acp"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := session.Prompt(ctx, acp.PromptInput{Text: "Reply with exactly the word: ok. Do not use tools."}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	entries, _ := session.Attach(0)
	home, _ := os.UserHomeDir()
	var lines []string
	for _, entry := range entries {
		line, _ := json.Marshal(entry)
		text := strings.ReplaceAll(string(line), home, "~")
		lines = append(lines, strings.ReplaceAll(text, filepath.Dir(project), "/work"))
	}
	if err := os.WriteFile(out, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
