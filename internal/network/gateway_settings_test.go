package network

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
)

func TestGatewaySettingsSharingDoesNotStopAgents(t *testing.T) {
	home := t.TempDir()
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	if err = WriteSharingConfig(home, SharingConfig{Enabled: true, Allow: []string{"tag:server"}}); err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://agentpack.example")
	stopped := false
	g := &Gateway{auth: auth, origin: origin, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home}, stop: func() { stopped = true }}
	handler := g.Handler()
	request := httptest.NewRequest("POST", origin.String()+"/api/settings/sharing", bytes.NewBufferString(`{"enabled":false}`))
	request.Header.Set("Authorization", "Bearer "+g.local.Secret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || stopped {
		t.Fatalf("sharing stop: %d stopped=%v", response.Code, stopped)
	}
	config, err := ReadSharingConfig(home)
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
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://agentpack.example")
	g := &Gateway{auth: auth, origin: origin, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home}}
	handler := g.peerHandler(&tailnetPeer{ID: "self", UserID: 7}, "100.100.1.2:7421")
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
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	const workerSecret = "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"
	stopped := map[string]bool{}
	worker := func(id string, session SessionInfo) {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+workerSecret {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			switch r.URL.Path {
			case "/state":
				_ = json.NewEncoder(w).Encode(TerminalState{Running: true, Interactive: true})
			case "/stop":
				stopped[id] = true
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		record := WorkerRecord{
			Session: session, ID: id, Address: strings.TrimPrefix(server.URL, "http://"),
			Secret: workerSecret,
		}
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
	owner := browserActor()
	foreign := Actor{Kind: "peer", User: "alice@example.com", Node: "remote", StableNodeID: "stable-foreign"}
	worker("session-owned-acp", SessionInfo{Transport: "acp", StartedBy: &owner})
	worker("session-foreign-acp", SessionInfo{Transport: "acp", StartedBy: &foreign})
	worker("session-foreign-terminal", SessionInfo{Transport: "terminal", StartedBy: &foreign})

	g := &Gateway{
		auth: auth, origin: origin, local: nodeRecord{Secret: strings.Repeat("x", 64)},
		options: GatewayOptions{Home: home},
	}
	listRequest := httptest.NewRequest(http.MethodGet, origin.String()+"/api/sessions", nil)
	listRequest.Header.Set("Authorization", "Bearer "+g.local.Secret)
	listRequest = listRequest.WithContext(withActor(listRequest.Context(), owner))
	listResponse := httptest.NewRecorder()
	g.Handler().ServeHTTP(listResponse, listRequest)
	var listed []SessionInfo
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
	privateRecord, err := workerRecord(home, "session-foreign-acp")
	if err != nil {
		t.Fatal(err)
	}
	if privateRecord.Session.StartedBy == nil || privateRecord.Session.StartedBy.StableNodeID != foreign.StableNodeID {
		t.Fatalf("private worker record lost stable peer identity: %+v", privateRecord.Session.StartedBy)
	}
	request := httptest.NewRequest(http.MethodPost, origin.String()+"/api/node/stop-agents", nil)
	request.Header.Set("Authorization", "Bearer "+g.local.Secret)
	request = request.WithContext(withActor(request.Context(), owner))
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
