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
