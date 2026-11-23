package mcp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"

	sdkacp "github.com/coder/acp-go-sdk"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

// fakeHost stands in for the gateway: no REST API, no browser login, one origin.
type fakeHost struct{}

func (fakeHost) ServeAPI(w http.ResponseWriter, r *http.Request, _ string) { http.NotFound(w, r) }
func (fakeHost) ServePrompt(w http.ResponseWriter, r *http.Request)        { http.NotFound(w, r) }
func (fakeHost) RemoveStartingPrompt(context.Context, string, string) (bool, error) {
	return false, nil
}
func (fakeHost) Emit(string)                        {}
func (fakeHost) Changes() (<-chan struct{}, func()) { return nil, func() {} }
func (fakeHost) Sessions(context.Context, string, bool) ([]session.SessionInfo, error) {
	return nil, nil
}
func (fakeHost) Origin(*http.Request) string { return testOrigin }
func (fakeHost) ClientKey(r *http.Request) (string, bool) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host, false
}
func (fakeHost) BrowserLogin(*http.Request) (string, bool) { return "", false }

const testOrigin = "http://127.0.0.1:7420"

func testServer(t *testing.T) *Server {
	t.Helper()
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m, err := New(store, t.TempDir(), testOrigin, fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAgentNamesFollowHarnessTargets(t *testing.T) {
	var names []string
	for _, agent := range mcpAgents() {
		names = append(names, string(agent))
	}
	if !slices.Equal(names, []string{"claude", "agent", "codex", "opencode", "grok", "agy", "omp"}) {
		t.Fatal("agent names drifted from harness targets", names)
	}
}

func TestMCPResponseBodyIsByteBounded(t *testing.T) {
	w := &mcpResponse{header: make(http.Header)}
	chunk := make([]byte, 1024)
	for range 4096 {
		if n, err := w.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("premature limit: %d %v", n, err)
		}
	}
	if n, err := w.Write([]byte{1}); err == nil || n != 0 || !w.tooLarge || w.body.Len() != 4<<20 {
		t.Fatalf("response unbounded: %d %v %d", n, err, w.body.Len())
	}
	if w.status != http.StatusOK {
		t.Fatalf("default response status: %d", w.status)
	}
}

func TestMCPResultsStayUnderOneMegabyte(t *testing.T) {
	handler := testServer(t).guardToolCalls(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", mcpResultLimit)}}}, nil
	})
	result, err := handler(t.Context(), "tools/call", nil)
	if err != nil || !result.(*mcp.CallToolResult).IsError {
		t.Fatalf("oversized result passed: %v", err)
	}
	var entries []acp.LogEntry
	for i := range 2 * network.TranscriptItems {
		entries = append(entries, acp.LogEntry{Seq: int64(i + 1), Kind: "prompt", Payload: json.RawMessage(`{"text":{"text":"` + strings.Repeat("y", 4096) + `"}}`)})
	}
	items, next := network.Transcript(entries)
	data, _ := json.Marshal(items)
	if next == "" || len(data) > mcpResultLimit/2 || items[len(items)-1].Seq != mustInt(t, next) {
		t.Fatalf("transcript page unbounded: %d items, %d bytes, next %q", len(items), len(data), next)
	}
	sessions := make([]int, 120)
	first, cursor, _ := page(sessions, "")
	second, last, _ := page(sessions, cursor)
	third, end, _ := page(sessions, last)
	if len(first) != mcpPageSize || len(second) != mcpPageSize || len(third) != 20 || end != "" {
		t.Fatal("paging", len(first), len(second), len(third), end)
	}
}

func mustInt(t *testing.T, value string) int64 {
	var n int64
	if err := json.Unmarshal([]byte(value), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTranscriptItemsAreTyped(t *testing.T) {
	entries := []acp.LogEntry{
		{Seq: 1, Kind: "prompt", Payload: json.RawMessage(`{"text":{"text":"Run the tests"}}`)},
		{Seq: 2, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"On "}}`)},
		{Seq: 3, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"it."}}`)},
		{Seq: 4, Kind: "tool_call", Payload: json.RawMessage(`{"toolCallId":"t1","title":"Bash","status":"pending"}`)},
		{Seq: 5, Kind: "tool_call_update", Payload: json.RawMessage(`{"toolCallId":"t1","status":"completed"}`)},
		{Seq: 6, Kind: "permission_request", Payload: json.RawMessage(`{"requestId":"p1","summary":"go test","options":[{"id":"a","label":"Allow","kind":"allow_once"},{"id":"x","label":"Odd","kind":"novel"}]}`)},
		{Seq: 7, Kind: "usage_update", Payload: json.RawMessage(`{}`)},
		{Seq: 8, Kind: "elicitation", Payload: json.RawMessage(`{"elicitationId":"e1","prompt":"Tone?","choices":["Direct"]}`)},
	}
	items, next := network.Transcript(entries)
	kinds := []string{}
	for _, item := range items {
		kinds = append(kinds, item.Kind)
	}
	if next != "" || !slices.Equal(kinds, []string{"prompt", "message", "tool", "permission", "question"}) {
		t.Fatal(kinds, next)
	}
	if items[1].Text != "On it." || items[1].Seq != 3 || items[2].Status != "completed" || items[2].Text != "Bash" {
		t.Fatalf("merge: %+v %+v", items[1], items[2])
	}
	if options := items[3].Options; options[0].Decision != sdkacp.PermissionOptionKindAllowOnce || options[1].Decision != "" {
		t.Fatalf("decisions: %+v", options)
	}
	url, ok := network.TranscriptItem(acp.LogEntry{Kind: "elicitation", Payload: json.RawMessage(`{"elicitationId":"e2","prompt":"Connect Artifacts","url":"https://chatgpt.com/apps/x"}`)})
	if !ok || url.Text != "Connect Artifacts https://chatgpt.com/apps/x" || len(url.Options) != 2 || url.Options[1].ID != "decline" {
		t.Fatalf("URL question: %+v", url)
	}
}
