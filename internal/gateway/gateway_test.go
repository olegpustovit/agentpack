package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/registry"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"

	"github.com/olegpustovit/agentpack/internal/paths"
)

func TestNodeRequestForwardsBodyAndAuthorization(t *testing.T) {
	secret := strings.Repeat("s", 64)
	body := `{"directory":"/tmp/project"}`
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions" || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("request = %s %s authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		wantBody := body
		if requests == 2 {
			wantBody = ""
		}
		if string(got) != wantBody {
			t.Errorf("body = %q, want %q", got, wantBody)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	home := t.TempDir()
	if err := paths.EnsurePrivateDirectory(filepath.Join(home, "network")); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(network.NodeRecord{Address: strings.TrimPrefix(server.URL, "http://"), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "network", "node.json"), record, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NodeRequest(context.Background(), home, http.MethodPost, "/api/sessions", strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := NodeRequest(context.Background(), home, http.MethodPost, "/api/sessions"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestGatewayAuthenticationHostAndOrigin(t *testing.T) {
	auth, err := testUnclaimedAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://agentpack.example")
	gateway := &Gateway{store: auth.store, auth: auth, origin: origin, local: network.NodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: t.TempDir()}, stop: func() {}}
	handler := gateway.Handler()
	check := func(method, path, host, origin, token string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, "https://"+host+path, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s host=%s origin=%s: %d %s", method, path, host, origin, w.Code, w.Body.String())
		}
	}
	check("GET", "/healthz", "agentpack.example", "", "", 200)
	check("GET", "/api/sessions", "agentpack.example", "", "", 403)
	check("GET", "/api/sessions", "evil.example", "", "", 403)
	check("POST", "/api/node/stop", "agentpack.example", "https://evil.example", "", 403)
	check("POST", "/api/node/stop", "agentpack.example", "", "", 403)
	check("GET", "/api/sessions", "127.0.0.1:7420", "", gateway.local.Secret, 200)
	check("GET", "/api/sessions", "agentpack.example", "https://evil.example", gateway.local.Secret, 403)
	r := httptest.NewRequest("POST", "https://agentpack.example/api/claim", bytes.NewBufferString(`{"email":"owner@example.com","password":"long-password-123"}`))
	r.Header.Set("Origin", "https://agentpack.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "https://agentpack.example/api/sessions", nil)
	r.AddCookie(w.Result().Cookies()[0])
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), gateway.local.Secret) {
		t.Fatal("authenticated session list", w.Code, w.Body.String())
	}
}

func TestGatewayRestrictsRemoteFolders(t *testing.T) {
	home := t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	called := false
	g := &Gateway{store: auth.store, auth: auth, origin: origin, local: network.NodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home, Start: func(context.Context, session.SessionInfo, string) (string, error) {
		called = true
		return "session-test", nil
	}}}
	r := httptest.NewRequest("POST", origin.String()+"/api/sessions", bytes.NewBufferString(`{"directory":"/unregistered","harness":"claude"}`))
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || called {
		t.Fatal("unregistered folder launched", w.Code)
	}
}

func TestGatewayPackageRestartChoice(t *testing.T) {
	home, directory := t.TempDir(), t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := remember(home, session.SessionInfo{Directory: directory, Harness: "claude", Mode: "default"}); err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	var restarts []bool
	g := &Gateway{store: auth.store, auth: auth, origin: origin, local: network.NodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{
		Home: home,
		Install: func(_ context.Context, gotDirectory, spec string, restart bool, _ string) error {
			if gotDirectory != directory || spec != "github.com/example/pack@abcdef" {
				t.Fatalf("unexpected install: %s %s", gotDirectory, spec)
			}
			restarts = append(restarts, restart)
			return nil
		},
	}}
	for _, choice := range []struct {
		value any
		want  bool
	}{{nil, true}, {true, true}, {false, false}} {
		body := map[string]any{"directory": directory, "spec": "github.com/example/pack@abcdef"}
		if choice.value != nil {
			body["restart"] = choice.value
		}
		data, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", origin.String()+"/api/packages", bytes.NewReader(data))
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK || len(restarts) == 0 || restarts[len(restarts)-1] != choice.want {
			t.Fatalf("restart %v: status=%d calls=%v body=%s", choice.value, w.Code, restarts, w.Body.String())
		}
	}
}

func TestGatewayInstallsMCPServerIntoRecentFolder(t *testing.T) {
	home, directory := t.TempDir(), t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := remember(home, session.SessionInfo{Directory: directory, Harness: "claude", Mode: "default"}); err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	var got []registry.MCPServerInput
	g := &Gateway{store: auth.store, auth: auth, origin: origin, local: network.NodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{
		Home: home,
		InstallMCP: func(_ context.Context, gotDirectory string, server registry.MCPServerInput, restart bool) error {
			if gotDirectory != directory || !restart {
				t.Fatalf("unexpected install into %s restart=%v", gotDirectory, restart)
			}
			got = append(got, server)
			return nil
		},
	}}
	post := func(body map[string]any) int {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", origin.String()+"/api/mcp/servers", bytes.NewReader(data))
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if code := post(map[string]any{"directory": t.TempDir(), "name": "pw", "command": "npx"}); code != 400 || len(got) != 0 {
		t.Fatalf("unknown folder: status %d, installs %v", code, got)
	}
	if code := post(map[string]any{"directory": directory, "name": "pw", "command": "npx", "args": []string{"-y", "@playwright/mcp"}}); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got) != 1 || got[0].Name != "pw" || got[0].Command != "npx" || len(got[0].Args) != 2 {
		t.Fatalf("installed %+v", got)
	}
}

func TestGatewayPeerProxyOnlyForAPIAndUnknownAPIIsJSON404(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://agentpack.example")
	local := network.NodeRecord{Secret: strings.Repeat("x", 64)}
	gateway := &Gateway{store: auth.store, auth: auth, origin: origin, local: local, options: GatewayOptions{Home: t.TempDir(), Assets: webAssets(testWebFiles())}, stop: func() {}}
	handler := gateway.Handler()
	serve := func(path string, withSecret bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", "https://agentpack.example"+path, nil)
		if withSecret {
			r.Header.Set("Authorization", "Bearer "+local.Secret)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := serve("/sessions?node=x", false); w.Code != 200 || w.Body.String() != "<html>app</html>" {
		t.Fatalf("/sessions?node=x: %d %q", w.Code, w.Body.String())
	}
	w := serve("/api/nope", true)
	if w.Code != 404 || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != "{\"error\":\"not found\"}\n" {
		t.Fatalf("/api/nope: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
}
func TestWorkerRequestTimeoutAllowsACPConversationOperations(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   time.Duration
	}{
		{http.MethodPost, "/stop", 2 * time.Minute},
		{http.MethodPost, "/api/sessions", 2 * time.Minute},
		{http.MethodPost, "/api/sessions/session-1/stop?node=remote", 2 * time.Minute},
		{http.MethodPost, "/api/sessions/session-1/restart?node=remote", 2 * time.Minute},
		{http.MethodPost, "/api/sessions/session-1/conversation/prompt", 2 * time.Minute},
		{http.MethodPost, "/api/sessions/session-1/conversation/blobs", 2 * time.Minute},
		{http.MethodGet, "/api/sessions/session-1/blobs/sha", 2 * time.Minute},
		{http.MethodPost, "/api/sessions/session-1/conversation/permission", 2 * time.Second},
		{http.MethodGet, "/api/sessions/session-1/conversation/prompt", 2 * time.Second},
		{http.MethodGet, "/api/logs/traces/trace-id?all_nodes=1", 5 * time.Second},
		{http.MethodGet, "/api/cache/entry?hash=digest", 2 * time.Minute},
	}
	for _, test := range tests {
		timeout := network.WorkerRequestTimeout
		if strings.HasPrefix(test.path, "/api/") {
			timeout = apiRequestTimeout
		}
		if got := timeout(test.method, test.path); got != test.want {
			t.Errorf("request timeout(%q, %q) = %v, want %v", test.method, test.path, got, test.want)
		}
	}
}
func TestNodeRequestStreamStopsAtLimit(t *testing.T) {
	const secret = "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/exact" {
			_, _ = io.WriteString(w, "abc")
			return
		}
		_, _ = io.WriteString(w, "abcdef")
	}))
	defer server.Close()
	home := t.TempDir()
	if err := paths.EnsurePrivateDirectory(filepath.Join(home, "network")); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(network.NodeRecord{Address: strings.TrimPrefix(server.URL, "http://"), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "network", "node.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	err = NodeRequestStream(context.Background(), home, http.MethodGet, "/image", &got, 3)
	if err == nil || got.String() != "abc" {
		t.Fatalf("oversized response = %q, %v; want a 3-byte prefix and error", got.String(), err)
	}
	got.Reset()
	if err := NodeRequestStream(context.Background(), home, http.MethodGet, "/exact", &got, 3); err != nil || got.String() != "abc" {
		t.Fatalf("exact response = %q, %v; want success", got.String(), err)
	}
}

func TestGatewayOwnsAndClosesStateStore(t *testing.T) {
	home := t.TempDir()
	if err := paths.EnsurePrivateDirectory(filepath.Join(home, "network")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ServeGateway(ctx, GatewayOptions{Home: home, Address: "127.0.0.1:0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "state.db")); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(filepath.Join(home, "state.db"+suffix)); !os.IsNotExist(err) {
			t.Fatalf("gateway left state connections open (%s): %v", suffix, err)
		}
	}
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	if err := store.Reader.QueryRow("SELECT version FROM schema_meta").Scan(&version); err != nil || version != state.SchemaVersion() {
		t.Fatalf("gateway schema=%d err=%v", version, err)
	}
	if _, err := os.Stat(filepath.Join(home, "network", "node.json")); !os.IsNotExist(err) {
		t.Fatalf("gateway left its process record: %v", err)
	}
}

func TestGatewayLockPrecedesStateOpen(t *testing.T) {
	home := t.TempDir()
	if err := paths.EnsurePrivateDirectory(filepath.Join(home, "network")); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(home, "network", "gateway.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	if err := ServeGateway(context.Background(), GatewayOptions{Home: home, Address: "127.0.0.1:0"}); err == nil {
		t.Fatal("started a second gateway")
	}
	if _, err := os.Stat(filepath.Join(home, "state.db")); !os.IsNotExist(err) {
		t.Fatalf("locked-out gateway touched state: %v", err)
	}
}

func TestGatewayServesSessionsAndStopsDuringWorkerWrite(t *testing.T) {
	home := t.TempDir()
	worker, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if err := worker.BindNode(t.Context(), "test-worker-node"); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.DB.Exec("INSERT INTO sessions(id,name,transport,node_id) VALUES ('committed','committed','acp'," + state.LocalNodeSQL + ")"); err != nil {
		t.Fatal(err)
	}
	library, err := registry.NewWithStore(worker, filepath.Join(home, "registry"))
	if err != nil {
		t.Fatal(err)
	}
	defer library.Close()
	tx, err := worker.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO sessions(id,name,transport,node_id) VALUES ('pending','pending','acp'," + state.LocalNodeSQL + ")"); err != nil {
		t.Fatal(err)
	}

	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- ServeGateway(ctx, GatewayOptions{Home: home, Address: "127.0.0.1:0", Library: LibraryOptions{Store: library}})
	}()

	start, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		body, err := NodeRequest(start, home, http.MethodGet, "/api/sessions")
		if err == nil {
			var rows []session.SessionInfo
			if err := json.Unmarshal(body, &rows); err != nil || len(rows) != 1 || rows[0].ID != "committed" {
				t.Fatalf("session response = %s, %v", body, err)
			}
			break
		}
		select {
		case err := <-done:
			t.Fatalf("gateway exited during a worker write: %v", err)
		case <-start.Done():
			t.Fatalf("session read timed out during a worker write: %v", err)
		case <-ticker.C:
		}
	}

	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("gateway did not exit while the worker held the database writer lock")
	}
	lock := flock.New(filepath.Join(home, "network", "gateway.lock"))
	if ok, err := lock.TryLock(); !ok || err != nil {
		t.Fatalf("gateway retained its process lock: %v", err)
	}
	defer lock.Unlock()
	if err := tx.Commit(); err != nil {
		t.Fatalf("worker transaction could not commit after gateway exit: %v", err)
	}
}
