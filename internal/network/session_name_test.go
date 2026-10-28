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

func TestSessionNameRoutePersistsWithoutChangingWorkerIdentity(t *testing.T) {
	home, id := t.TempDir(), "session-test"
	workerDirectory := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(workerDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/state" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 64) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, TerminalState{Running: true, Rows: 24, Columns: 80})
	}))
	defer worker.Close()
	record := WorkerRecord{
		Session: SessionInfo{Directory: t.TempDir(), Harness: "claude", Mode: "default"},
		ID:      id, Address: strings.TrimPrefix(worker.URL, "http://"), Secret: strings.Repeat("s", 64),
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDirectory, "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	secret := strings.Repeat("g", 64)
	makeGateway := func() *Gateway {
		return &Gateway{auth: auth, origin: origin, local: nodeRecord{Secret: secret}, options: GatewayOptions{Home: home}}
	}
	request := func(gateway *Gateway, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, origin.String()+path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(w, r)
		return w
	}
	unauthenticated := httptest.NewRequest(http.MethodPatch, origin.String()+"/api/sessions/"+id, strings.NewReader(`{"name":"blocked"}`))
	unauthenticated.Header.Set("Origin", origin.String())
	unauthenticatedResponse := httptest.NewRecorder()
	makeGateway().Handler().ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated rename status: %d", unauthenticatedResponse.Code)
	}
	if response := request(makeGateway(), http.MethodPatch, "/api/sessions/"+id, `{}`); response.Code != http.StatusBadRequest {
		t.Fatalf("missing name status: %d %s", response.Code, response.Body.String())
	}
	response := request(makeGateway(), http.MethodPatch, "/api/sessions/"+id, `{"name":"  Pairing session  "}`)
	if response.Code != http.StatusOK {
		t.Fatalf("rename failed: %d %s", response.Code, response.Body.String())
	}
	var renamed SessionInfo
	if err := json.Unmarshal(response.Body.Bytes(), &renamed); err != nil {
		t.Fatal(err)
	}
	if renamed.ID != id || renamed.Name != "Pairing session" || !renamed.State.Running {
		t.Fatalf("unexpected rename response: %+v", renamed)
	}
	// A new gateway instance sees the label from disk while the worker identity stays intact.
	response = request(makeGateway(), http.MethodGet, "/api/sessions", "")
	var sessions []SessionInfo
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &sessions) != nil || len(sessions) != 1 || sessions[0].Name != renamed.Name {
		t.Fatalf("persisted session name: %d %s", response.Code, response.Body.String())
	}
	stored, err := workerRecord(home, id)
	if err != nil || stored.Session.Name != "" || stored.ID != id {
		t.Fatalf("worker identity changed: %+v %v", stored, err)
	}
	response = request(makeGateway(), http.MethodPatch, "/api/sessions/"+id, `{"name":""}`)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"name"`) {
		t.Fatalf("clearing name failed: %d %s", response.Code, response.Body.String())
	}
}

func TestSessionNameValidationAndPeerRoute(t *testing.T) {
	for _, name := range []string{strings.Repeat("a", 81), "bad\nname"} {
		if _, err := normalizeSessionName(name); err == nil {
			t.Fatalf("accepted invalid session name %q", name)
		}
	}
	if _, err := normalizeSessionName("  "); err != nil {
		t.Fatalf("empty name should clear a label: %v", err)
	}
	if !peerRoute(http.MethodPatch, "/api/sessions/session-test") {
		t.Fatal("session rename unavailable on an allowed peer")
	}
	for _, path := range []string{"/api/sessions", "/api/sessions/x/stop", "/api/sessions/../x"} {
		if peerRoute(http.MethodPatch, path) {
			t.Fatalf("unexpected remote PATCH route allowed: %s", path)
		}
	}
}
