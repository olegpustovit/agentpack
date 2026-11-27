package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/state"
)

func TestSessionHistory(t *testing.T) {
	g, origin := identityGateway(t, t.TempDir(), nil)
	ctx := t.Context()
	if _, err := g.store.DB.ExecContext(ctx, `
		WITH RECURSIVE numbers(n) AS (VALUES (1) UNION ALL SELECT n+1 FROM numbers WHERE n<205)
		INSERT INTO sessions(id,last_used,transport,node_id,ended_at)
		SELECT printf('chat-%03d',n), n/2, 'acp', CASE WHEN n%2=0 THEN 'offline' ELSE `+
		state.LocalNodeSQL+` END, n%2 FROM numbers;
		INSERT INTO sessions(id,last_used,transport,hidden) VALUES ('hidden',300,'acp',1),('terminal',300,'pty',0)
	`); err != nil {
		t.Fatal(err)
	}

	handler := g.Handler()
	request := func(query string, status int) sessionHistoryPage {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, origin.String()+"/api/sessions?history=1"+query, nil)
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("history %q: %d %s", query, w.Code, w.Body.String())
		}
		var page sessionHistoryPage
		if status == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
		}
		return page
	}
	first := request("&limit=1&node=offline", http.StatusOK)
	if len(first.Sessions) != 1 || first.Sessions[0].ID != "chat-205" || !first.Sessions[0].Reachable || first.Next != "102:chat-205" {
		t.Fatalf("first page: %+v", first)
	}
	second := request("&limit=1&before="+url.QueryEscape(first.Next), http.StatusOK)
	if len(second.Sessions) != 1 || second.Sessions[0].ID != "chat-204" || second.Sessions[0].Reachable || second.Next != "102:chat-204" {
		t.Fatalf("second page: %+v", second)
	}

	if page := request("", http.StatusOK); len(page.Sessions) != 100 || page.Next != "53:chat-106" {
		t.Fatalf("default page: %d, %q", len(page.Sessions), page.Next)
	}
	last := request("&limit=999", http.StatusOK)
	if len(last.Sessions) != 200 || last.Next != "3:chat-006" {
		t.Fatalf("capped page: %d, %q", len(last.Sessions), last.Next)
	}
	tail := request("&before="+url.QueryEscape(last.Next), http.StatusOK)
	var tailIDs []string
	for _, row := range tail.Sessions {
		tailIDs = append(tailIDs, row.ID)
	}
	if tail.Next != "" || !slices.Equal(tailIDs, []string{"chat-005", "chat-004", "chat-003", "chat-002", "chat-001"}) {
		t.Fatalf("last page: %v, %q", tailIDs, tail.Next)
	}
	if empty := request("&before=0:chat-001", http.StatusOK); empty.Sessions == nil || len(empty.Sessions) != 0 || empty.Next != "" {
		t.Fatalf("empty page: %+v", empty)
	}
	for _, query := range []string{"&limit=0", "&limit=abc", "&before=broken", "&before=102:"} {
		request(query, http.StatusBadRequest)
	}

	for _, plan := range []struct {
		where string
		args  []any
		want  string
	}{
		{"", []any{101}, "SCAN sessions USING INDEX sessions_history"},
		{sessionHistoryBefore, []any{102, "chat-205", 101}, "SEARCH sessions USING INDEX sessions_history"},
	} {
		var id, parent, unused int
		var detail string
		err := g.store.Reader.QueryRowContext(ctx, "EXPLAIN QUERY PLAN "+sessionHistorySelect+plan.where+sessionHistoryOrder, plan.args...).Scan(&id, &parent, &unused, &detail)
		if err != nil || !strings.Contains(detail, plan.want) {
			t.Fatalf("query plan: %q, %v", detail, err)
		}
	}
}

func TestRestoreLegacyHistory(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	ctx := t.Context()
	if _, err := g.store.DB.ExecContext(ctx, `
		INSERT INTO sessions(id,node_id,hidden,transport,last_used,ended_at,archived_at) VALUES
		('prompted',`+state.LocalNodeSQL+`,1,'acp',30,20,0),
		('fallback',`+state.LocalNodeSQL+`,1,'acp',0,20,0),
		('empty',`+state.LocalNodeSQL+`,1,'acp',30,20,0),
		('remote','other',1,'acp',30,20,0),
		('live',`+state.LocalNodeSQL+`,1,'acp',30,0,0),
		('archived',`+state.LocalNodeSQL+`,1,'acp',30,20,40),
		('terminal',`+state.LocalNodeSQL+`,1,'terminal',30,20,0);
		WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM numbers WHERE n<129)
		INSERT INTO sessions(id,node_id,hidden,transport,last_used,ended_at)
		SELECT 'batch-'||n,`+state.LocalNodeSQL+`,1,'acp',30,20 FROM numbers;
		INSERT INTO conversation_events(session_id,seq,body)
		SELECT id,1,'{"kind":"prompt"}' FROM sessions WHERE id<>'empty';
	`); err != nil {
		t.Fatal(err)
	}
	check := func(id string, wantHidden int, wantClosed int64) {
		t.Helper()
		var hidden int
		var closed int64
		if err := g.store.Reader.QueryRowContext(ctx, "SELECT hidden,closed_at FROM sessions WHERE id=?", id).Scan(&hidden, &closed); err != nil {
			t.Fatal(err)
		}
		if hidden != wantHidden || closed != wantClosed {
			t.Errorf("%s: hidden=%d closed_at=%d, want %d and %d", id, hidden, closed, wantHidden, wantClosed)
		}
	}
	if err := restoreLegacyHistory(ctx, g.store); err != nil {
		t.Fatal(err)
	}
	check("prompted", 0, 30)
	check("fallback", 0, 20)
	var restored int
	if err := g.store.Reader.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE id LIKE 'batch-%' AND hidden=0 AND closed_at=30").Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != 129 {
		t.Errorf("restored %d of 129 batch chats", restored)
	}
	for _, id := range []string{"empty", "remote", "live", "archived", "terminal"} {
		check(id, 1, 0)
	}
	// Make a row eligible after completion and open a fresh store as a gateway restart would.
	if _, err := g.store.DB.ExecContext(ctx, "UPDATE sessions SET ended_at=20 WHERE id='live'"); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.Open(g.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := restoreLegacyHistory(ctx, reopened); err != nil {
		t.Fatal(err)
	}
	check("live", 1, 0)
}
