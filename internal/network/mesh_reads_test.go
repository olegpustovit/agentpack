package network

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestMeshReadsSurviveOwnerShutdownAndRefuseWrites(t *testing.T) {
	source, server, _ := syncGateway(t, "remote")
	home := t.TempDir()
	target, origin := identityGateway(t, home, nil)
	target.store = target.auth.store
	target.options.Library.Store = replicaStore(t)
	if err := target.options.Library.Store.EnrollPeer("remote"); err != nil {
		t.Fatal(err)
	}
	owner := browserActor(testBrowserEmail)
	info := SessionInfo{ID: "session-remote", Name: "Saved chat", Directory: "/remote/project", Harness: "codex", Transport: "acp", AgentSessionID: "native-remote", StartedBy: &owner, LastUsed: time.Now()}
	if err := upsertSession(t.Context(), source.auth.store.DB, info); err != nil {
		t.Fatal(err)
	}
	entry := acp.LogEntry{Seq: 1, Kind: "message", Payload: json.RawMessage(`{"text":"persisted history"}`)}
	body, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.auth.store.DB.Exec("INSERT INTO conversation_events(session_id,seq,body) VALUES (?,1,?); UPDATE sessions SET last_seq=1", info.ID, string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := source.auth.store.DB.Exec(`INSERT INTO limits(node_id,harness,snapshot,collected_at) VALUES ('remote','codex','{"harness":"codex","state":"available","windows":[]}',1)`); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := url.Parse(server.URL)
	if _, _, err := pullState(t.Context(), target.auth.store, target.options.Library.Store, server.Client(), endpoint, nil); err != nil {
		t.Fatal(err)
	}
	server.Close()
	handler := target.Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, origin.String()+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+target.local.Secret)
		// This test acts as the owning browser account, rather than the OS account.
		r = r.WithContext(withActor(r.Context(), owner))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/sessions", "/api/sessions?node=remote"} {
		w := request("GET", path, "")
		if w.Code != 200 {
			t.Fatalf("sessions: %d %s", w.Code, w.Body.String())
		}
		var entries []SessionInfo
		if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].NodeID != "remote" || entries[0].Reachable {
			t.Fatalf("offline sessions: %+v", entries)
		}
	}
	for _, path := range []string{"/api/recent?node=remote", "/api/agent-sessions?node=remote", "/api/limits?node=remote"} {
		w := request("GET", path, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"node_id":"remote"`) || !strings.Contains(w.Body.String(), `"reachable":false`) {
			t.Fatalf("offline read %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, suffix := range []string{"", "?node=remote", "?node=remote&stream=1"} {
		w := request("GET", "/api/sessions/session-remote/conversation"+suffix, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "persisted history") {
			t.Fatalf("offline conversation: %d %s", w.Code, w.Body.String())
		}
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	fallbackURL, _ := url.Parse(failing.URL)
	for _, disconnect := range []bool{false, true} {
		if disconnect {
			failing.Close()
		}
		r := httptest.NewRequest("GET", origin.String()+"/api/sessions/session-remote/conversation", nil)
		r.SetPathValue("id", info.ID)
		w := httptest.NewRecorder()
		target.proxyTailnetTo(w, r, "remote", fallbackURL)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "persisted history") {
			t.Fatalf("history proxy fallback: %d %s", w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/conversation/prompt", "/restart", "/stop", "/fork"} {
		w := request("POST", "/api/sessions/session-remote"+path, `{}`)
		if w.Code != 503 {
			t.Fatalf("offline write %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := request("PATCH", "/api/sessions/session-remote?node=remote", `{"name":"Renamed offline"}`)
	if w.Code != 200 {
		t.Fatalf("replicated rename: %d %s", w.Code, w.Body.String())
	}
	var name string
	if err := target.auth.store.Reader.QueryRow("SELECT name FROM sessions WHERE id=?", info.ID).Scan(&name); err != nil || name != "Renamed offline" {
		t.Fatalf("rename was not saved: %q %v", name, err)
	}
}

func TestMeshLivenessExpiresAndLocalFoldersKeepFilesystemChecks(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	local, err := g.auth.store.NodeID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	g.replicaStatus.Store("peer", replicaContact{Reachable: true, At: time.Now().Add(-time.Minute)})
	if online, _ := g.nodeContact("peer", local); !online {
		t.Fatal("successful recent sync not reachable")
	}
	g.replicaStatus.Store("peer", replicaContact{Reachable: true, At: time.Now().Add(-2 * time.Minute)})
	if online, _ := g.nodeContact("peer", local); online {
		t.Fatal("stale sync stayed reachable")
	}
	g.replicaStatus.Store("peer", replicaContact{At: time.Now(), Error: "offline"})
	if online, _ := g.nodeContact("peer", local); online {
		t.Fatal("failed sync stayed reachable")
	}
	last := time.Now().Add(-time.Minute)
	g.replicaStatus.Store("peer", replicaContact{At: time.Now(), LastSuccess: last, Error: "offline"})
	if _, got := g.nodeContact("peer", local); !got.Equal(last) {
		t.Fatal("failed contact replaced last successful sync time")
	}
	if err := upsertSession(t.Context(), g.auth.store.DB, SessionInfo{ID: "session-missing", Directory: "/missing/project"}); err != nil {
		t.Fatal(err)
	}
	folders, err := g.meshRecent(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Available || !folders[0].Reachable {
		t.Fatalf("local filesystem grant bypassed: %+v", folders)
	}
}

func TestMeshNodeInventoryKeepsOfflineMachinesAfterRestart(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	if _, err := g.auth.store.DB.Exec("INSERT INTO mesh_nodes(node_id,name,app_version) VALUES ('asleep','Sleeping computer','v1.0.0')"); err != nil {
		t.Fatal(err)
	}
	nodes, err := g.meshNodes(t.Context(), nil)
	if err != nil || len(nodes) != 1 || nodes[0].Online || nodes[0].Name != "Sleeping computer" {
		t.Fatalf("offline inventory: %+v %v", nodes, err)
	}
	g.replicaStatus.Store("asleep", replicaContact{Reachable: true, At: time.Now()})
	nodes, err = g.meshNodes(t.Context(), nil)
	if err != nil || len(nodes) != 1 || !nodes[0].Online {
		t.Fatalf("reachable inventory: %+v %v", nodes, err)
	}
}

func TestMeshRecentSessionListKeepsOlderBookmarks(t *testing.T) {
	g, origin := identityGateway(t, t.TempDir(), nil)
	store := g.auth.store.DB
	now := time.Now()
	for i := range 101 {
		info := SessionInfo{ID: fmt.Sprintf("session-old-%03d", i), Transport: "acp", LastUsed: now.Add(time.Duration(i) * time.Second)}
		if err := upsertSession(t.Context(), store, info); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Exec("UPDATE sessions SET ended_at=last_used"); err != nil {
		t.Fatal(err)
	}
	if err := upsertSession(t.Context(), store, SessionInfo{ID: "session-live", Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, origin.String()+path, nil)
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w
	}
	list := request("/api/sessions")
	var rows []SessionInfo
	if err := json.Unmarshal(list.Body.Bytes(), &rows); err != nil || len(rows) != 101 || rows[len(rows)-1].ID != "session-live" {
		t.Fatalf("recent list status=%d rows=%d err=%v", list.Code, len(rows), err)
	}
	for _, row := range rows {
		if row.ID == "session-old-000" {
			t.Fatal("oldest stopped chat remained in the recent list")
		}
	}
	old := request("/api/sessions/session-old-000")
	var row SessionInfo
	if err := json.Unmarshal(old.Body.Bytes(), &row); old.Code != http.StatusOK || err != nil || row.ID != "session-old-000" {
		t.Fatalf("old bookmark status=%d id=%q err=%v", old.Code, row.ID, err)
	}
}
