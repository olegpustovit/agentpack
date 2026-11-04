package hub

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestFormatThreadElicitation(t *testing.T) {
	state := acp.ReduceLog([]acp.LogEntry{{
		Kind:    "elicitation",
		Payload: json.RawMessage(`{"elicitationId":"e1","prompt":"Which tone?"}`),
	}})
	lines := formatThread(state, 80)
	if len(lines) < 2 || lines[0] != "◆ Needs you" {
		t.Fatalf("lines = %v", lines)
	}
	if !strings.Contains(lines[1], "Which tone?") {
		t.Fatalf("elicitation line = %q", lines[1])
	}
}

func TestPreviewWindow(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	got := previewWindow(lines, 3, 0)
	if len(got) != 3 || got[0] != "c" || got[2] != "e" {
		t.Fatalf("tail=0: %v", got)
	}
	got = previewWindow(lines, 3, 2)
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("tail=2: %v", got)
	}
}

func TestFormatThreadExploredGroup(t *testing.T) {
	state := acp.ThreadState{
		Items: []acp.ThreadItem{
			{Kind: "tool", Data: json.RawMessage(`{"title":"Read","kind":"read","status":"completed"}`)},
			{Kind: "tool", Data: json.RawMessage(`{"title":"Search","kind":"search","status":"completed"}`)},
			{Kind: "tool", Data: json.RawMessage(`{"title":"Edit","kind":"edit","status":"pending"}`)},
			{Kind: "tool", Data: json.RawMessage(`{"title":"Terminal","kind":"terminal","status":"completed"}`)},
		},
	}
	lines := formatThread(state, 80)
	foundExplore, foundEdit, foundTerminal := false, false, false
	for _, line := range lines {
		foundExplore = foundExplore || line == "▸ Explored 2 steps"
		foundEdit = foundEdit || strings.Contains(line, "Edit (pending)")
		foundTerminal = foundTerminal || strings.Contains(line, "Terminal (completed)")
	}
	if !foundExplore || !foundEdit || !foundTerminal {
		t.Fatalf("lines = %v", lines)
	}
}

func TestFormatThreadFullFixtureExplored(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "acp", "full.jsonl")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var entries []acp.LogEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry acp.LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	lines := formatThread(ReduceThread(entries), 120)
	found := false
	for _, line := range lines {
		if line == "▸ Explored 4 steps" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected grouped tools line, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestFormatThreadStripsOSC52(t *testing.T) {
	state := acp.ReduceLog([]acp.LogEntry{{
		Kind:    "agent_message_chunk",
		Payload: json.RawMessage(`{"text":{"text":"\u001b]52;c;evil\u0007injected"}}`),
	}})
	lines := formatThread(state, 80)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "\u001b") || strings.Contains(line, "52;c") {
			t.Fatalf("control sequence leaked: %q", line)
		}
		if strings.Contains(line, "injected") {
			found = true
		}
	}
	if !found {
		t.Fatalf("lines = %v", lines)
	}
}

func TestFormatThreadUserPrefix(t *testing.T) {
	state := acp.ReduceLog([]acp.LogEntry{{
		Kind:    "user_message_chunk",
		Payload: json.RawMessage(`{"text":{"text":"hello"}}`),
	}})
	lines := formatThread(state, 80)
	found := false
	for _, line := range lines {
		if strings.HasPrefix(line, "▷ hello") {
			found = true
		}
	}
	if !found {
		t.Fatalf("lines = %v", lines)
	}
}

func TestFormatThreadPlanKind(t *testing.T) {
	state := acp.ReduceLog([]acp.LogEntry{{
		Kind:    "plan",
		Payload: json.RawMessage(`{"entries":[{"status":"in_progress","content":"Publish the branch"}]}`),
	}})
	lines := formatThread(state, 80)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "Publish the branch") {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan line missing: %v", lines)
	}
}

func TestFormatThreadPermission(t *testing.T) {
	state := acp.ReduceLog([]acp.LogEntry{{
		Kind:    "permission_request",
		Payload: json.RawMessage(`{"requestId":"p1","tool":"Bash","summary":"Run tests"}`),
	}})
	lines := formatThread(state, 80)
	if len(lines) < 2 {
		t.Fatalf("expected needs-you + permission lines, got %v", lines)
	}
	if lines[0] != "◆ Needs you" {
		t.Fatalf("first line: %q", lines[0])
	}
}
func TestFormatThreadWrapsLongAgentText(t *testing.T) {
	const text = "A long agent response wraps at terminal width while preserving every glyph: 漢字 終わり"
	payload := json.RawMessage(`{"text":{"text":"A long agent response wraps at terminal width while preserving every glyph: 漢字 終わり"}}`)
	state := acp.ReduceLog([]acp.LogEntry{{Kind: "agent_message_chunk", Payload: payload}})
	lines := formatThread(state, 32)
	if len(lines) < 2 || strings.Join(strings.Fields(strings.Join(lines[1:], " ")), " ") != text {
		t.Fatalf("wrapped lines lost content: %q", lines)
	}
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > 28 {
			t.Fatalf("line width %d > 28: %q", width, line)
		}
	}
}
