package network

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/agentpack/internal/obs"
)

func diagnosticGatewayFixture(t *testing.T) (*oauthHarness, obs.Event) {
	t.Helper()
	h := newOAuthHarness(t)
	logs, err := obs.Open(h.gateway.options.Home, obs.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trace := obs.NewTrace()
	event := obs.Event{TS: time.Now().Add(-time.Minute), TraceID: trace.ID, SpanID: trace.Span, NodeID: "fixture-node", Proc: "worker", Kind: "worker", Name: "unexpected_exit", Level: "error", Attrs: map[string]any{"version": "v1.2.0", "message": "fixture failure"}}.Sanitized()
	logs.Write(event)
	parent := event
	parent.Level, parent.Kind, parent.SpanID = "info", "http", obs.NewTrace().Span
	parent.TS = parent.TS.Add(-time.Second)
	logs.Write(parent)
	if err := logs.Close(); err != nil {
		t.Fatal(err)
	}
	return h, event
}

func TestDiagnosticsRoutesRequireLoginAndOrigin(t *testing.T) {
	h, event := diagnosticGatewayFixture(t)
	path := "/api/logs/issues/" + event.Fingerprint
	for _, test := range []struct {
		method, path, body string
		login, origin      bool
		status             int
	}{{"GET", "/api/logs/issues", "", false, false, 401}, {"GET", path, "", false, false, 401}, {"POST", path + "/resolve", `{"version":"v1.2.0"}`, true, false, 403}, {"GET", "/api/logs/issues?status=bad", "", true, false, 400}, {"GET", "/api/logs/issues/bad", "", true, false, 400}, {"GET", "/api/logs/issues/" + strings.Repeat("a", 64), "", true, false, 404}, {"POST", path + "/resolve", `{"note":"no version"}`, true, true, 400}, {"POST", path + "/resolve", `{"version":"v1.2.0","who":"attacker"}`, true, true, 400}, {"POST", path + "/resolve", `{"version":"v1.2.0"} {}`, true, true, 400}, {"GET", "/api/logs/traces/bad", "", true, false, 400}} {
		var cookie *http.Cookie
		if test.login {
			cookie = h.cookie
		}
		response, data := h.request(t, test.method, test.path, test.body, "application/json", cookie, test.origin)
		if response.StatusCode != test.status {
			t.Fatalf("%s %s: %d want=%d %s", test.method, test.path, response.StatusCode, test.status, data)
		}
	}
	response, data := h.request(t, "GET", "/api/logs/issues", "", "", h.cookie, false)
	var issues []obs.Issue
	if response.StatusCode != 200 || json.Unmarshal(data, &issues) != nil || len(issues) != 1 || issues[0].Count != 1 {
		t.Fatalf("list: %d %s", response.StatusCode, data)
	}
	for _, action := range []string{"resolve", "reopen", "ignore"} {
		response, data := h.request(t, "POST", path+"/"+action, `{"version":"v1.2.0","note":"fixed"}`, "application/json", h.cookie, true)
		var issue obs.Issue
		want := map[string]string{"resolve": "resolved", "reopen": "open", "ignore": "ignored"}[action]
		if response.StatusCode != 200 || json.Unmarshal(data, &issue) != nil || issue.Status != want {
			t.Fatalf("%s: %d %s", action, response.StatusCode, data)
		}
		if action == "resolve" && (issue.ResolvedBy != browserUser || issue.ResolvedVersion != "v1.2.0") {
			t.Fatalf("resolution metadata=%+v", issue)
		}
	}
	response, data = h.request(t, "GET", "/api/logs/traces/"+event.TraceID, "", "", h.cookie, false)
	var events []obs.Event
	if response.StatusCode != 200 || json.Unmarshal(data, &events) != nil || len(events) != 2 || events[0].Kind != "http" {
		t.Fatalf("trace: %d %s", response.StatusCode, data)
	}
}

func TestMCPDiagnosticToolsReuseAuthenticatedRoutes(t *testing.T) {
	h, event := diagnosticGatewayFixture(t)
	tokens := h.exchange(t, h.codeForm(h.code(t)), 200)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "diagnostics-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.server.URL + "/mcp", HTTPClient: &http.Client{Transport: mcpBearerTransport{tokens.Access}}}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"list_issues", "get_issue", "get_trace", "resolve_issue"} {
		found := false
		for _, tool := range listed.Tools {
			if tool.Name == name {
				found = true
				if tool.Annotations == nil || tool.Annotations.ReadOnlyHint != (name != "resolve_issue") {
					t.Fatalf("incorrect mutation hint: %+v", tool)
				}
			}
		}
		if !found {
			t.Fatalf("missing tool: %s", name)
		}
	}
	for _, test := range []struct {
		name string
		args map[string]any
		fail bool
	}{{"list_issues", map[string]any{}, false}, {"get_issue", map[string]any{"fingerprint": event.Fingerprint}, false}, {"get_trace", map[string]any{"trace_id": event.TraceID}, false}, {"resolve_issue", map[string]any{"fingerprint": event.Fingerprint, "version": "v1.2.0", "note": "fixed by test"}, false}, {"list_issues", map[string]any{"status": "resolved"}, false}, {"get_issue", map[string]any{"fingerprint": "bad"}, true}, {"get_trace", map[string]any{"trace_id": "bad"}, true}, {"resolve_issue", map[string]any{"fingerprint": event.Fingerprint, "version": ""}, true}} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if err != nil || result.IsError != test.fail || len(result.Content) == 0 {
			t.Fatalf("tool=%s err=%v result=%+v", test.name, err, result)
		}
		if !test.fail {
			content, ok := result.Content[0].(*mcp.TextContent)
			if !ok || !json.Valid([]byte(content.Text)) {
				t.Fatalf("tool=%s invalid JSON result=%+v", test.name, result)
			}
			if test.name == "get_trace" {
				var events []obs.Event
				if json.Unmarshal([]byte(content.Text), &events) != nil || len(events) != 2 {
					t.Fatalf("incomplete trace: %s", content.Text)
				}
			}
		}
	}
	issue, err := obs.GetIssue(ctx, h.gateway.options.Home, event.Fingerprint)
	if err != nil || issue.Status != "resolved" || issue.Note != "fixed by test" {
		t.Fatalf("tool did not persist resolution: %+v %v", issue, err)
	}
}
