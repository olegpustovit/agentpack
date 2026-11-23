package network

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestArchivedSessionsRefuseEveryWriteAndNativeResume(t *testing.T) {
	home := t.TempDir()
	g, origin := identityGateway(t, home, nil)
	directory := seedFolder(t, home)
	folder, err := FolderIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	owner := browserActor(testBrowserEmail)
	session := SessionInfo{ID: "session-archived", Directory: directory, FolderID: folder, Harness: "cursor", Transport: "acp", AgentSessionID: "native-archived", StartedBy: &owner}
	if err := upsertSession(t.Context(), g.store.DB, session); err != nil {
		t.Fatal(err)
	}
	if _, err := g.store.DB.Exec("UPDATE sessions SET archived_at=1 WHERE id=?", session.ID); err != nil {
		t.Fatal(err)
	}
	handler := g.Handler()
	for _, path := range []string{"/conversation/prompt", "/conversation/prompt/images", "/conversation/permission", "/conversation/cancel", "/conversation/elicitation", "/conversation/config", "/conversation/blobs", "/restart", "/fork", "/stop", "/input"} {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest("POST", origin.String()+"/api/sessions/"+session.ID+path, strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer "+g.local.Secret)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 409 || strings.TrimSpace(w.Body.String()) != "archived" {
				t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
			}
		})
	}
	for _, resumeDirectory := range []string{directory, t.TempDir(), directory + "/missing"} {
		for _, transport := range []string{"acp", "terminal"} {
			body, _ := json.Marshal(map[string]string{"directory": resumeDirectory, "harness": "agent", "agent_session_id": session.AgentSessionID, "transport": transport})
			r := httptest.NewRequest("POST", origin.String()+"/api/sessions", strings.NewReader(string(body)))
			r.Header.Set("Authorization", "Bearer "+g.local.Secret)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 409 || strings.TrimSpace(w.Body.String()) != "archived" {
				t.Fatalf("resume %s in %s: %d %s", transport, resumeDirectory, w.Code, w.Body.String())
			}
		}
	}
	if err := upsertSession(t.Context(), g.store.DB, session); !errors.Is(err, errSessionArchived) {
		t.Fatalf("worker revived archived metadata: %v", err)
	}
	entry := acp.LogEntry{Seq: 1, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"late event"}}`)}
	if err := persistConversationChangesSince(t.Context(), g.store.DB, session.ID, acp.LogChanges{LastSeq: 1, Entries: []acp.LogEntry{entry}}, 0); err == nil {
		t.Fatal("worker added events to archived session")
	}
	ids, err := archivedNativeIDs(t.Context(), g.store, folder, "agent")
	if err != nil || !ids[session.AgentSessionID] {
		t.Fatalf("harness alias bypassed archive state: %v %v", ids, err)
	}
}

func TestMainBadgeMetadataFollowsReplicatedRole(t *testing.T) {
	home := t.TempDir()
	g, origin := identityGateway(t, home, nil)
	if err := g.store.BindNode(t.Context(), "local-main"); err != nil {
		t.Fatal(err)
	}
	g.identityAt = time.Now()
	g.identitySelf = tailnetSelf{ID: "local-main"}
	roles, err := g.store.Main(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := g.store.SetMain(t.Context(), roles, home); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", origin.String()+"/api/identity", nil)
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	var identity identityResponse
	if err := json.Unmarshal(w.Body.Bytes(), &identity); err != nil || !identity.Node.Main {
		t.Fatalf("local main metadata: %s %v", w.Body.String(), err)
	}
	if _, err := g.store.DB.Exec("UPDATE mesh_settings SET value='peer-main' WHERE key='main_node'; INSERT INTO mesh_nodes(node_id,name) VALUES ('peer-main','Archive computer')"); err != nil {
		t.Fatal(err)
	}
	nodes, err := g.meshNodes(t.Context(), nil)
	if err != nil || len(nodes) != 1 || !nodes[0].Main {
		t.Fatalf("peer main metadata: %+v %v", nodes, err)
	}
}
