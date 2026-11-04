package hub

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestFormatChatThreadFullFixtureAtTerminalWidths(t *testing.T) {
	state := readFullChatFixture(t)
	expanded := make(map[string]bool)
	for i := range state.Items {
		item := state.Items[i]
		if item.Kind == "agent_thought_chunk" {
			expanded[chatThoughtKey(i)] = true
		}
		if item.Kind == "tool" && exploratoryToolKind(decodeChatTool(item.Data).Kind) {
			end, _ := exploratoryRun(state.Items, i)
			if end-i > 1 {
				expanded[chatToolGroupKey(i)] = true
				i = end - 1
			}
		}
	}
	want := []string{
		"I'll update the reattach path to pass the last replay offset.",
		"Checking worker HTTP and gateway compression.",
		"Use the direct tone.",
		"Done. The gateway will resume from the saved offset.",
		"Plan · 1/2 complete",
		"Reproduce with a restarted gateway",
		"Publish the branch",
		"conn, err := dial(ctx, record, 0)",
		"since := record.lastOffset.Load()",
		"conn, err := dial(ctx, record, since)",
		"Permission required",
		"ok",
		"Run go test on worker package",
		"Allow once",
		"Deny",
		"Which tone should the copy use?",
		"Direct",
		"Friendly",
		"Formal",
		"Image attached",
		"image/png",
		"640×360",
		"Context compacted",
		"Compaction summary retained.",
	}
	for _, width := range []int{46, 72, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			lines := formatChatThread(state, width, expanded)
			flat := strings.NewReplacer("│", " ", "┃", " ").Replace(strings.Join(lines, " "))
			joined := strings.Join(strings.Fields(flat), " ")
			for _, text := range want {
				if !strings.Contains(joined, text) {
					t.Errorf("width %d lost %q:\n%s", width, text, strings.Join(lines, "\n"))
				}
			}
			if strings.Contains(joined, "52;c;evil") || strings.Contains(joined, "injected") {
				t.Errorf("width %d leaked a stripped OSC payload: %q", width, joined)
			}
			for _, line := range lines {
				if strings.ContainsAny(line, "\x1b\x07") {
					t.Errorf("width %d emitted a terminal control: %q", width, line)
				}
				if got := ansi.StringWidth(line); got > width {
					t.Errorf("width %d emitted %d-cell line %q", width, got, line)
				}
			}
		})
	}
}
func TestFormatChatThreadNoticeAndCompaction(t *testing.T) {
	state := ReduceThread([]acp.LogEntry{
		{Kind: "notice", Payload: json.RawMessage(`{"sessionUpdate":"notice","severity":"warning","title":"MCP server unavailable","description":"Continuing without it."}`)},
		{Kind: "compaction_update", Payload: json.RawMessage(`{"sessionUpdate":"compaction_update","compactionId":"cmp_1","status":"in_progress"}`)},
		{Kind: "compaction_summary_chunk", Payload: json.RawMessage(`{"sessionUpdate":"compaction_summary_chunk","compactionId":"cmp_1","content":{"type":"text","text":"Old partial summary"}}`)},
		{Kind: "compaction_update", Payload: json.RawMessage(`{"sessionUpdate":"compaction_update","compactionId":"cmp_1","status":"completed","summary":[{"type":"text","text":"Full retained summary"}]}`)},
	})
	lines := formatChatThread(state, 72, nil)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"MCP server unavailable · Continuing without it.", "Conversation compacted", "Full retained summary"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Old partial summary") {
		t.Fatalf("summary patch did not replace streamed chunks:\n%s", joined)
	}
	for _, line := range lines {
		if got := ansi.StringWidth(line); got > 72 {
			t.Errorf("emitted %d-cell line %q", got, line)
		}
	}
}

func TestChatToolGroupExpandsByHeaderLine(t *testing.T) {
	state := readFullChatFixture(t)
	collapsed := formatChatThread(state, 46, nil)
	groupLine := -1
	for line, text := range collapsed {
		if strings.HasPrefix(text, "▸ Explored ") {
			groupLine = line
			break
		}
	}
	if groupLine < 0 {
		t.Fatalf("exploratory group header missing:\n%s", strings.Join(collapsed, "\n"))
	}
	key := chatToolGroupAtLine(state, 46, groupLine, nil)
	if key != "tools:2" {
		t.Fatalf("group key = %q, want tools:2", key)
	}
	if got := chatToolGroupAtLine(state, 46, groupLine+1, nil); got != "" {
		t.Fatalf("non-group line key = %q, want empty", got)
	}
	if strings.Contains(strings.Join(collapsed, "\n"), "Read worker_http.go") {
		t.Fatal("exploratory details appeared while collapsed")
	}

	expanded := formatChatThread(state, 46, map[string]bool{key: true})
	joined := strings.Join(expanded, "\n")
	for _, detail := range []string{"Read worker_http.go", "Search replay", "Read terminal_client.go", "Search lastOffset"} {
		if !strings.Contains(joined, detail) {
			t.Errorf("expanded group missing %q:\n%s", detail, joined)
		}
	}
	for _, line := range expanded {
		if got := ansi.StringWidth(line); got > 46 {
			t.Errorf("expanded group emitted %d-cell line %q", got, line)
		}
	}
	if got := chatToolGroupAtLine(state, 46, groupLine+1, map[string]bool{key: true}); got != "" {
		t.Fatalf("expanded tool detail line key = %q, want empty", got)
	}
}
func TestChatThoughtAndToolRowsCollapseAndExpand(t *testing.T) {
	state := acp.ThreadState{Items: []acp.ThreadItem{
		{Kind: "agent_thought_chunk", Data: json.RawMessage(`{"text":"private reasoning details"}`)},
		{Kind: "tool", Data: json.RawMessage(`{"kind":"execute","title":"Run command","status":"completed","command":"go test","output":"all tests passed"}`)},
	}}
	collapsed := formatChatThread(state, 32, map[string]bool{"tool:1": false})
	joined := strings.Join(collapsed, "\n")
	if strings.Contains(joined, "private reasoning") || strings.Contains(joined, "all tests passed") {
		t.Fatalf("collapsed thought or tool output remained visible:\n%s", joined)
	}
	thoughtLine, toolLine := -1, -1
	for line, text := range collapsed {
		if strings.HasPrefix(text, "▸ Thought") {
			thoughtLine = line
		}
		if strings.HasPrefix(text, "▸ ✓ Run command") {
			toolLine = line
		}
	}
	if thoughtLine < 0 || chatToolGroupAtLine(state, 32, thoughtLine, map[string]bool{"tool:1": false}) != "thought:0" {
		t.Fatalf("thought header is not expandable: %q", collapsed)
	}
	if toolLine < 0 || chatToolGroupAtLine(state, 32, toolLine, map[string]bool{"tool:1": false}) != "tool:1" {
		t.Fatalf("tool header is not expandable: %q", collapsed)
	}
	expanded := formatChatThread(state, 32, map[string]bool{"thought:0": true, "tool:1": true})
	joined = strings.Join(expanded, "\n")
	for _, detail := range []string{"private reasoning details", "go test", "all tests passed"} {
		if !strings.Contains(joined, detail) {
			t.Errorf("expanded thread missing %q:\n%s", detail, joined)
		}
	}
	for _, line := range expanded {
		if got := ansi.StringWidth(line); got > 32 {
			t.Errorf("expanded row is %d cells wide: %q", got, line)
		}
	}
}
func TestChatContextSummaryClampsMeter(t *testing.T) {
	under := chatContextSummary(acp.ThreadState{ContextTokens: 20, ContextLimit: 100})
	if !strings.Contains(under, "[██░░░░░░░░] 20%") {
		t.Fatalf("context meter = %q", under)
	}
	over := chatContextSummary(acp.ThreadState{ContextTokens: 150, ContextLimit: 100})
	if !strings.Contains(over, "[██████████] 150%") {
		t.Fatalf("over-limit meter = %q", over)
	}
}
func TestChatMessageLineMapsWholeStreamedMessage(t *testing.T) {
	state := acp.ThreadState{Items: []acp.ThreadItem{
		{Kind: "agent_message_chunk", Data: json.RawMessage(`{"text":"First "}`)},
		{Kind: "agent_message_chunk", Data: json.RawMessage(`{"text":"and second chunk"}`)},
	}}
	if got := chatMessageAtLine(state, 10, 0, nil); got != "First and second chunk" {
		t.Fatalf("message action text = %q", got)
	}
}

func TestChatResourceLinkRendersAsFileAttachment(t *testing.T) {
	state := ReduceThread([]acp.LogEntry{{
		Kind:    "user_message_chunk",
		Payload: json.RawMessage(`{"resource_link":{"name":"photo.png","blob":"sha256:abc"}}`),
	}})
	lines := formatChatThread(state, 46, nil)
	if !strings.Contains(strings.Join(lines, "\n"), "File attached · photo.png") {
		t.Fatalf("resource link missing from chat thread: %q", lines)
	}
}
func TestChatResolvedRequestsRenderAnswersInsteadOfOptions(t *testing.T) {
	state := ReduceThread([]acp.LogEntry{
		{Kind: "permission_request", Payload: json.RawMessage(`{"requestId":"p1","tool":"Bash","options":[{"id":"allow","label":"Allow once"}]}`)},
		{Kind: "permission_result", Payload: json.RawMessage(`{"requestId":"p1","optionId":"allow"}`)},
		{Kind: "elicitation", Payload: json.RawMessage(`{"elicitationId":"e1","prompt":"Which tone?","choices":["Direct"]}`)},
		{Kind: "elicitation_result", Payload: json.RawMessage(`{"elicitationId":"e1","value":"Direct"}`)},
	})
	lines := strings.Join(formatChatThread(state, 72, nil), "\n")
	for _, want := range []string{"Permission answered · Bash", "Answer: Allow once", "✓ Which tone? · Answer: Direct"} {
		if !strings.Contains(lines, want) {
			t.Errorf("resolved thread missing %q:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, "Options: Allow once") || strings.Contains(lines, "Options: Direct") {
		t.Fatalf("resolved thread still offered answers:\n%s", lines)
	}
}
func TestChatAttachmentsMapToOpenablePlaceholderLines(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		name    string
		payload string
		label   string
	}{
		{"image", `{"image":{"blob":"sha256:` + sha + `","mime":"image/png"}}`, "Image attached"},
		{"resource link", `{"resource_link":{"name":"photo.png","blob":"sha256:` + sha + `"}}`, "File attached"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := ReduceThread([]acp.LogEntry{{Kind: "user_message_chunk", Payload: json.RawMessage(test.payload)}})
			lines := formatChatThread(state, 46, nil)
			for line, text := range lines {
				if strings.Contains(text, test.label) {
					if got := chatImageAtLine(state, 46, line, nil); got.SHA != sha {
						t.Fatalf("attachment at line %d = %+v, want sha %s", line, got, sha)
					}
					return
				}
			}
			t.Fatalf("missing %q in %q", test.label, lines)
		})
	}
}

func readFullChatFixture(t *testing.T) acp.ThreadState {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "testdata", "acp", "full.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var entries []acp.LogEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if scanner.Text() == "" {
			continue
		}
		var entry acp.LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return ReduceThread(entries)
}
