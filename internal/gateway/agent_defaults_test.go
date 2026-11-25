package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
)

func TestAgentDefaultsRoutes(t *testing.T) {
	g, origin := identityGateway(t, t.TempDir(), nil)
	testBrowserSession(t, g.auth, testBrowserEmail, "owner")
	testBrowserSession(t, g.auth, "member@example.com", "member")
	handler := g.Handler()
	for _, tc := range []struct {
		method, token, body string
		status              int
		want                agentDefaults
	}{
		{"GET", "member", "", 200, agentDefaults{true, true}},
		{"PUT", "member", `{"agentpack_tools":false}`, 403, agentDefaults{}},
		{"PUT", "owner", `{"unknown":false}`, 400, agentDefaults{}},
		{"PUT", "owner", `{"replace_subagents":null}`, 400, agentDefaults{}},
		{"PUT", "owner", `{"agentpack_tools":false}`, 200, agentDefaults{false, true}},
		{"PUT", "owner", `{"replace_subagents":false}`, 200, agentDefaults{false, false}},
		{"GET", "member", "", 200, agentDefaults{false, false}},
	} {
		r := httptest.NewRequest(tc.method, origin.String()+"/api/agents/defaults", strings.NewReader(tc.body))
		r.Header.Set("Origin", origin.String())
		r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: tc.token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s as %s: got %d %s, want %d", tc.method, tc.body, tc.token, w.Code, w.Body, tc.status)
		}
		if tc.status == http.StatusOK {
			var got agentDefaults
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got != tc.want {
				t.Fatalf("%s %s: got %s (%v), want %+v", tc.method, tc.body, w.Body, err, tc.want)
			}
		}
	}
}

func TestChatStartUsesAgentDefaults(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	g, origin := identityGateway(t, home, func(_ context.Context, info session.SessionInfo, _ string) (string, error) {
		return info.ID, nil
	})
	t.Cleanup(g.starting.Wait)
	testBrowserSession(t, g.auth, testBrowserEmail, "owner")
	handler := g.Handler()
	if _, err := g.store.DB.Exec(`INSERT INTO agent_defaults(key,value) VALUES ('agentpack_tools',0),('replace_subagents',0)`); err != nil {
		t.Fatal(err)
	}

	trueValue := true
	var last session.SessionInfo
	for _, tc := range []struct {
		id    string
		tools *bool
		want  bool
	}{
		{"", nil, false},
		{"", &trueValue, true},
		{"session-d7148a740d2c495991c792576427836d", nil, false},
		{"session-d7148a740d2c495991c792576427836e", &trueValue, true},
	} {
		input := chatStartRequest{ID: tc.id, Directory: dir, Harness: "claude", Mode: "default", Transport: "acp", Tools: tc.tools}
		r := httptest.NewRequest("POST", origin.String()+"/api/sessions", strings.NewReader(string(network.MustRawJSON(input))))
		r.Header.Set("Origin", origin.String())
		r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: "owner"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var created session.SessionInfo
		if err := json.Unmarshal(w.Body.Bytes(), &created); w.Code != http.StatusOK || err != nil {
			t.Fatalf("start %q: %d %s (%v)", tc.id, w.Code, w.Body, err)
		}
		stored, err := session.StoredSession(t.Context(), g.store.Reader, created.ID)
		if err != nil || stored.Tools != tc.want || !stored.KeepNativeSubagents {
			t.Fatalf("start %q: tools=%v keep native subagents=%v err=%v", tc.id, stored.Tools, stored.KeepNativeSubagents, err)
		}
		last = stored
	}

	if _, err := g.store.DB.Exec(`DELETE FROM agent_defaults; INSERT INTO sessions(id) VALUES ('legacy-chat')`); err != nil {
		t.Fatal(err)
	}
	legacy, err := session.StoredSession(t.Context(), g.store.Reader, "legacy-chat")
	if err != nil || legacy.KeepNativeSubagents {
		t.Fatalf("legacy session should replace native subagents: %+v (%v)", legacy, err)
	}

	// Retrying the claimed start after the defaults change returns the original snapshot.
	input := chatStartRequest{ID: last.ID, Directory: dir, Harness: "claude", Mode: "default", Transport: "acp", Tools: &trueValue}
	retried, err := g.claimChatStart(t.Context(), session.BrowserActor(testBrowserEmail), input)
	if err != nil || !retried.KeepNativeSubagents {
		t.Fatalf("retry lost subagent snapshot: %+v (%v)", retried, err)
	}

	g.starting.Wait()
	if _, err := g.store.DB.Exec("UPDATE sessions SET ended_at=1 WHERE id=?", last.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := g.resumeSession(t.Context(), last.ID, "", "")
	if err != nil || !resumed.KeepNativeSubagents {
		t.Fatalf("resume lost subagent snapshot: %+v (%v)", resumed, err)
	}
}
