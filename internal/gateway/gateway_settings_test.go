package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/gateway/mesh"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/registry"
	"github.com/olegpustovit/agentpack/internal/session"

	"github.com/olegpustovit/agentpack/internal/state"
)

func TestGatewaySettingsSharingDoesNotStopAgents(t *testing.T) {
	home := t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = mesh.WriteSharingConfig(store, mesh.SharingConfig{Enabled: true, Allow: []string{"tag:server"}}); err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://agentpack.example")
	stopped := false
	g := &Gateway{store: store, auth: auth, origin: origin, local: network.NodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home}, stop: func() { stopped = true }}
	handler := g.Handler()
	request := httptest.NewRequest("POST", origin.String()+"/api/settings/sharing", bytes.NewBufferString(`{"enabled":false}`))
	request.Header.Set("Authorization", "Bearer "+g.local.Secret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || stopped {
		t.Fatalf("sharing stop: %d stopped=%v", response.Code, stopped)
	}
	config, err := mesh.ReadSharingConfig(store)
	if err != nil || config.Enabled || len(config.Allow) != 1 {
		t.Fatalf("config %+v %v", config, err)
	}
	request = httptest.NewRequest("GET", origin.String()+"/api/settings", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("unauthenticated settings: %d", response.Code)
	}
	request.Header.Set("Authorization", "Bearer "+g.local.Secret)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"sharing_enabled":false`) {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestPeerHTTPRejectsBrowserAndAdministrativeRoutes(t *testing.T) {
	home := t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://agentpack.example")
	g := &Gateway{store: auth.store, auth: auth, origin: origin, local: network.NodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home}}
	handler := g.tailnet.PeerHandler(g.store, g, &mesh.Peer{ID: "self", UserID: 7}, "100.100.1.2:7421")
	for _, test := range []struct {
		host, path, origin string
		want               int
	}{
		{"evil.example", "/api/sessions", "", 403},
		{"100.100.1.2:7421", "/api/sessions", "https://evil.example", 403},
		{"100.100.1.2:7421", "/api/node/stop", "", 404},
		{"100.100.1.2:7421", "/api/settings", "", 404},
		{"100.100.1.2:7421", "/api/sessions?node=another", "", 403},
	} {
		request := httptest.NewRequest("GET", "http://"+test.host+test.path, nil)
		request.Header.Set("Origin", test.origin)
		request.Header.Set("Authorization", "Bearer "+g.local.Secret)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%s %s: %d", test.host, test.path, response.Code)
		}
	}
}

func TestStopAgentsSkipsACPWorkersOwnedElsewhere(t *testing.T) {
	home := t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	const workerSecret = "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"
	stopped := map[string]bool{}
	worker := func(id string, info session.SessionInfo) {
		t.Helper()
		info.ID = id
		if err := session.UpsertSession(t.Context(), auth.store.DB, info); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+workerSecret {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			switch r.URL.Path {
			case "/state":
				_ = json.NewEncoder(w).Encode(session.TerminalState{Running: true, Interactive: true})
			case "/stop":
				stopped[id] = true
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		record := network.WorkerRecord{
			Session: info, ID: id, Address: strings.TrimPrefix(server.URL, "http://"),
			Secret: workerSecret,
		}
		record.Session = session.SessionInfo{}
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(home, "network", "sessions", id)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	owner := session.BrowserActor(testBrowserEmail)
	foreign := session.Actor{Kind: "peer", User: "alice@example.com", Node: "remote", StableNodeID: "stable-foreign"}
	worker("session-owned-acp", session.SessionInfo{Transport: "acp", StartedBy: &owner})
	worker("session-foreign-acp", session.SessionInfo{Transport: "acp", StartedBy: &foreign})
	worker("session-foreign-terminal", session.SessionInfo{Transport: "terminal", StartedBy: &foreign})

	g := &Gateway{
		store: auth.store, auth: auth, origin: origin, local: network.NodeRecord{Secret: strings.Repeat("x", 64)},
		options: GatewayOptions{Home: home},
	}
	listRequest := httptest.NewRequest(http.MethodGet, origin.String()+"/api/sessions", nil)
	listRequest.Header.Set("Authorization", "Bearer "+g.local.Secret)
	listRequest = listRequest.WithContext(session.WithActor(listRequest.Context(), owner))
	listResponse := httptest.NewRecorder()
	g.Handler().ServeHTTP(listResponse, listRequest)
	var listed []session.SessionInfo
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode public session list %q: %v", listResponse.Body.String(), err)
	}
	foundForeignACP := false
	for _, session := range listed {
		if session.ID == "session-foreign-acp" {
			foundForeignACP = true
			if session.StartedBy == nil || session.StartedBy.StableNodeID != "" {
				t.Fatalf("public session leaked stable peer identity: %+v", session.StartedBy)
			}
		}
	}
	if listResponse.Code != http.StatusOK || !foundForeignACP {
		t.Fatalf("session list status=%d found foreign ACP=%t", listResponse.Code, foundForeignACP)
	}
	if strings.Contains(listResponse.Body.String(), `"stable_node_id"`) {
		t.Fatalf("public session list included stable_node_id: %s", listResponse.Body.String())
	}
	privateRecord, err := g.workerRecord(t.Context(), "session-foreign-acp")
	if err != nil {
		t.Fatal(err)
	}
	if privateRecord.Session.StartedBy == nil || privateRecord.Session.StartedBy.StableNodeID != foreign.StableNodeID {
		t.Fatalf("private worker record lost stable peer identity: %+v", privateRecord.Session.StartedBy)
	}
	request := httptest.NewRequest(http.MethodPost, origin.String()+"/api/node/stop-agents", nil)
	request.Header.Set("Authorization", "Bearer "+g.local.Secret)
	request = request.WithContext(session.WithActor(request.Context(), owner))
	response := httptest.NewRecorder()
	g.Handler().ServeHTTP(response, request)
	var result struct {
		Stopped bool `json:"stopped"`
		Skipped int  `json:"skipped"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode stop response %q: %v", response.Body.String(), err)
	}
	if response.Code != http.StatusOK || !result.Stopped || result.Skipped != 1 {
		t.Fatalf("response %d: %+v (%s)", response.Code, result, response.Body.String())
	}
	if len(stopped) != 2 || !stopped["session-owned-acp"] || !stopped["session-foreign-terminal"] {
		t.Fatalf("stopped sessions = %v", stopped)
	}
	if stopped["session-foreign-acp"] {
		t.Fatal("stop-all stopped an ACP session owned by another peer")
	}
}

func TestProfilesAreLocalOnly(t *testing.T) {
	home := t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://agentpack.example")
	g := &Gateway{store: auth.store, auth: auth, origin: origin, local: network.NodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home}}
	handler := g.Handler()
	for _, request := range []struct {
		header, origin string
		want           int
	}{
		{"Bearer " + g.local.Secret, "", 200},
		{"Bearer " + g.local.Secret, origin.String(), 401},
		{"", "", 401},
	} {
		r := httptest.NewRequest("GET", origin.String()+"/api/debug/pprof/goroutine?debug=1", nil)
		if request.header != "" {
			r.Header.Set("Authorization", request.header)
		}
		if request.origin != "" {
			r.Header.Set("Origin", request.origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != request.want {
			t.Errorf("auth=%t origin=%q: %d, want %d", request.header != "", request.origin, w.Code, request.want)
		}
	}
}

func TestRemovedPeerCannotMutate(t *testing.T) {
	home := t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	store, err := registry.New(home)
	if err != nil {
		t.Fatal(err)
	}
	const peer, address = "removed-peer", "100.64.0.3"
	if err := store.EnrollPeer(peer); err != nil {
		t.Fatal(err)
	}
	if err := store.RemovePeer(peer); err != nil {
		t.Fatal(err)
	}
	var identity mesh.Identity
	identity.Node.StableID, identity.Node.User = peer, 7
	identity.Node.Addresses = []string{address + "/32"}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	g := &Gateway{store: auth.store, auth: auth, origin: origin, options: GatewayOptions{Home: home, Library: LibraryOptions{Store: store}}}
	host := "100.64.0.2:7421"
	// The tailnet layer has admitted this identity; the gateway must still refuse a removed peer.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.ServePeer(w, r, &mesh.Peer{ID: "self", UserID: 7}, identity)
	})
	for _, pattern := range peerPatterns() {
		method, path, _ := strings.Cut(pattern, " ")
		if method == http.MethodGet {
			continue
		}
		path = strings.NewReplacer("{id}", "session", "{queueId}", "queue", "{revision}", "revision", "{harness}", "codex").Replace(path)
		r := httptest.NewRequest(method, "http://"+host+path, nil)
		r.RemoteAddr = address + ":1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403", method, path, w.Code)
		}
	}
}

func TestReplicationSyncOnlyWakesEnrolledWorker(t *testing.T) {
	home := t.TempDir()
	g, origin := identityGateway(t, home, nil)
	store, err := registry.New(home)
	if err != nil {
		t.Fatal(err)
	}
	g.options.Library.Store = store
	const peer = "test-peer"
	if err := store.EnrollPeer(peer); err != nil {
		t.Fatal(err)
	}
	wake := make(chan struct{}, 1)
	g.statePulls.Store(peer, wake)
	testBrowserSession(t, g.auth, testBrowserEmail, "owner")
	testBrowserSession(t, g.auth, "member@example.com", "member")
	call := func(token string) int {
		r := httptest.NewRequest("POST", origin.String()+replicationPrefix+"sync", strings.NewReader(`{"peer":"test-peer"}`))
		r.Header.Set("Origin", origin.String())
		r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: token})
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if got := call("member"); got != http.StatusForbidden || len(wake) != 0 {
		t.Fatalf("member status = %d, wake count = %d", got, len(wake))
	}
	for range 2 {
		if got := call("owner"); got != http.StatusOK {
			t.Fatalf("owner status = %d", got)
		}
	}
	if len(wake) != 1 {
		t.Fatalf("owner wake count = %d, want 1", len(wake))
	}
}

func TestAdministrativeRoutesRejectMembers(t *testing.T) {
	g, origin := identityGateway(t, t.TempDir(), nil)
	testBrowserSession(t, g.auth, "member@example.com", "member")
	for _, path := range []string{"/api/node/stop", "/api/node/update", "/api/packages", "/api/mcp/servers",
		"/api/settings/sharing", "/api/worktrees/remove", "/api/worktrees/push"} {
		r := httptest.NewRequest("POST", origin.String()+path, strings.NewReader("{}"))
		r.Header.Set("Origin", origin.String())
		r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: "member"})
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403", path, w.Code)
		}
	}
}
