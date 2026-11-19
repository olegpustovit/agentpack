package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
)

func TestPresenceChild(t *testing.T) {
	if os.Getenv("AGENTPACK_PRESENCE_CHILD") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func TestNativePresencePreservesPipesAndOwnsStop(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestPresenceChild$")
	command.Env = append(os.Environ(), "AGENTPACK_PRESENCE_CHILD=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	home := t.TempDir()
	testPresenceGateway(t, home)
	cleanup, err := RegisterPresence(home, "test", SessionInfo{Directory: t.TempDir(), Harness: "codex"}, command.Process)
	if err != nil {
		command.Process.Kill()
		command.Wait()
		t.Fatal(err)
	}
	defer cleanup()
	sessions, err := Sessions(context.Background(), home)
	if err != nil || len(sessions) != 1 || sessions[0].State.Interactive || !sessions[0].State.Running {
		t.Fatalf("presence: %+v %v", sessions, err)
	}
	if err = StopWorker(context.Background(), home, sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err == nil {
		t.Fatal("native child did not stop")
	}
	cleanup()
	if sessions, err = Sessions(context.Background(), home); err != nil || len(sessions) != 0 {
		t.Fatal("presence survived cleanup", sessions, err)
	}
}

func testPresenceGateway(t *testing.T, home string) *Gateway {
	t.Helper()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1")
	g := &Gateway{auth: auth, store: auth.store, origin: origin, options: GatewayOptions{Home: home}, local: nodeRecord{Secret: strings.Repeat("s", 64)}}
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	g.local.Address = strings.TrimPrefix(server.URL, "http://")
	if err := os.MkdirAll(filepath.Join(home, "network"), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(g.local)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "network", "node.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestPresenceDoesNotOpenStateDatabase(t *testing.T) {
	home := t.TempDir()
	marker := []byte("foreground must not open this database")
	path := filepath.Join(home, "state.db")
	if err := os.WriteFile(path, marker, 0600); err != nil {
		t.Fatal(err)
	}
	methods := make(chan string, 4)
	var failEnd atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 64) || !strings.HasSuffix(r.URL.Path, "/presence") {
			t.Errorf("unexpected presence request: %s %s", r.Method, r.URL.Path)
		}
		methods <- r.Method
		if r.Method == http.MethodDelete && failEnd.Load() {
			http.Error(w, "gateway unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))
	defer server.Close()
	if err := os.Mkdir(filepath.Join(home, "network"), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(nodeRecord{Address: strings.TrimPrefix(server.URL, "http://"), Secret: strings.Repeat("s", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "network", "node.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	cleanup, err := RegisterPresence(home, "test", SessionInfo{Directory: t.TempDir(), Harness: "codex"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	cleanup()
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(marker) {
		t.Fatalf("foreground changed database: %q %v", after, err)
	}
	if len(methods) != 2 {
		t.Fatalf("presence calls: %d", len(methods))
	}
	if got := <-methods + "," + <-methods; got != "POST,DELETE" {
		t.Fatalf("presence lifecycle: %s", got)
	}
	if _, err := os.Stat(filepath.Join(home, "logs")); !os.IsNotExist(err) {
		t.Fatalf("successful presence wrote diagnostics: %v", err)
	}
	failEnd.Store(true)
	cleanup, err = RegisterPresence(home, "test", SessionInfo{Directory: t.TempDir(), Harness: "codex"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	files, err := filepath.Glob(filepath.Join(home, "logs", "spool", "*.ndjson"))
	if err != nil || len(files) != 1 {
		t.Fatalf("failed presence end lost diagnostics: %v %v", files, err)
	}
	data, err = os.ReadFile(files[0])
	var event obs.Event
	if err != nil || json.Unmarshal(data, &event) != nil || event.Level != "error" || event.SessionID == "" || event.Attrs["version"] != "test" || strings.Contains(string(data), strings.Repeat("s", 64)) {
		t.Fatalf("presence end diagnostic: %s %v", data, err)
	}
	after, err = os.ReadFile(path)
	if err != nil || string(after) != string(marker) {
		t.Fatalf("failed presence end opened database: %q %v", after, err)
	}
}

func TestPresenceEndpointRejectsBrowserCredentials(t *testing.T) {
	g := testPresenceGateway(t, t.TempDir())
	testBrowserSession(t, g.auth, testBrowserEmail, "browser-token")
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if peerRoute(method, "/api/local/sessions/session-test/presence") {
			t.Fatal("private presence route is available to peers")
		}
		r := httptest.NewRequest(method, g.origin.String()+"/api/local/sessions/session-test/presence", nil)
		r.Header.Set("Origin", g.origin.String())
		r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: "browser-token"})
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("browser %s presence status %d: %s", method, w.Code, w.Body.String())
		}
	}
}

func TestPresenceStorageFailureIsRecorded(t *testing.T) {
	g := testPresenceGateway(t, t.TempDir())
	if err := g.auth.store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	queue := obs.NewQueue()
	r := httptest.NewRequest(http.MethodDelete, g.origin.String()+"/api/local/sessions/session-test/presence", nil)
	r.SetPathValue("id", "session-test")
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	r = r.WithContext(obs.WithSink(r.Context(), queue, obs.BuildMetadata("test", localStateNode, "gateway")))
	w := httptest.NewRecorder()
	g.servePresence(w, r)
	if w.Code != http.StatusInternalServerError || len(queue.Events) != 1 {
		t.Fatalf("storage failure: status=%d events=%d", w.Code, len(queue.Events))
	}
	event := <-queue.Events
	if event.Level != "error" || event.Fingerprint == "" || event.SessionID != "session-test" {
		t.Fatalf("storage failure lost diagnostic context: %+v", event)
	}
}
