package network

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
)

func storedBrowserErrors(t *testing.T, g *Gateway) []map[string]any {
	t.Helper()
	if err := g.logs.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(g.options.Home, "logs", time.Now().UTC().Format(time.DateOnly)+".db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return []map[string]any{}
	}
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(path))+"?agentpack_plain=1&mode=ro&_query_only=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT lower(hex(trace_id)),node_id,user,ts,json(attrs) FROM events WHERE kind='ui.error'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var trace, node, user, at, attrs string
		if err := rows.Scan(&trace, &node, &user, &at, &attrs); err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(attrs), &data); err != nil {
			t.Fatal(err)
		}
		result = append(result, map[string]any{"trace_id": trace, "node_id": node, "user": user, "ts": at, "attrs": data})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBrowserTelemetryTrustLimitsAndPersistence(t *testing.T) {
	g, origin := identityGateway(t, t.TempDir(), nil)
	logs, err := obs.Open(g.options.Home, obs.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	g.logs = logs
	g.auth.sessions[sha256.Sum256([]byte("browser"))] = time.Now().Add(time.Hour)
	handler := g.Handler()
	request := func(body, token string) int {
		r := httptest.NewRequest("POST", origin.String()+"/api/telemetry", strings.NewReader(body))
		r.Header.Set("Origin", origin.String())
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if code := request(`{"events":[]}`, ""); code != 401 {
		t.Fatal("unauthenticated", code)
	}
	local := httptest.NewRequest("POST", origin.String()+"/api/telemetry", strings.NewReader(`{"events":[]}`))
	local.Header.Set("Authorization", "Bearer "+g.local.Secret)
	localResponse := httptest.NewRecorder()
	handler.ServeHTTP(localResponse, local)
	if localResponse.Code != 401 {
		t.Fatal("local bearer authorized browser telemetry", localResponse.Code)
	}
	if code := request(strings.Repeat(" ", 64<<10)+"{}", "browser"); code != 413 {
		t.Fatal("oversized", code)
	}
	if code := request(`{"events":[]} {}`, "browser"); code != 400 {
		t.Fatal("trailing JSON", code)
	}
	const header = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	body := `{"events":[{"name":"TypeError","message":"component failed","stack":"fixture stack","traceparent":"` + header + `","route":"/sessions","user":"forged-user","node_id":"forged-node","ts":"1900-01-01","search":{"password":"private-password","view":"active"},"viewport":[1280,800],"breadcrumbs":[{"kind":"api","name":"GET /api/sessions","trace_id":"0123456789abcdef0123456789abcdef","status":200,"prompt":"private prompt"}]}]}`
	if code := request(body, "browser"); code != 204 {
		t.Fatal("valid", code)
	}
	events := storedBrowserErrors(t, g)
	if len(events) != 1 || events[0]["trace_id"] != "0123456789abcdef0123456789abcdef" || events[0]["user"] != browserUser || events[0]["node_id"] != localStateNode {
		t.Fatalf("stored %+v", events)
	}
	encoded, _ := json.Marshal(events)
	for _, secret := range []string{"forged-user", "forged-node", "1900-01-01", "private-password", "private prompt"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("telemetry trusted/leaked", secret)
		}
	}
	for range 59 {
		if code := request(body, "browser"); code != 204 {
			t.Fatal("within rate", code)
		}
	}
	if code := request(body, "browser"); code != 429 {
		t.Fatal("rate limit", code)
	}
	g.auth.sessions[sha256.Sum256([]byte("other"))] = time.Now().Add(time.Hour)
	if code := request(body, "other"); code != 204 {
		t.Fatal("separate session", code)
	}
	r := httptest.NewRequest("POST", origin.String()+"/api/telemetry", nil)
	r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: "browser"})
	if code := g.auth.allowTelemetry(r, 1, time.Now().Add(time.Minute)); code != 204 {
		t.Fatal("window reset", code)
	}
}

// This subprocess fixture gives Playwright a real day store, without a production test endpoint.
func TestTelemetryBrowserFixture(t *testing.T) {
	if os.Getenv("AGENTPACK_TELEMETRY_FIXTURE") != "1" {
		return
	}
	g, _ := identityGateway(t, t.TempDir(), nil)
	logs, err := obs.Open(g.options.Home, obs.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	g.logs = logs
	g.auth.sessions[sha256.Sum256([]byte("telemetry-browser"))] = time.Now().Add(time.Hour)
	g.options.Assets = http.FileServer(http.Dir("../../ui/dist"))
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	g.origin, _ = url.Parse("http://" + server.Listener.Addr().String())
	mux := http.NewServeMux()
	stop := make(chan struct{}, 1)
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		var data any = []any{}
		switch r.URL.Path {
		case "/api/status":
			data = map[string]any{"authenticated": true, "setup_required": false}
		case "/api/identity":
			data = map[string]any{"node": map[string]any{"id": "gateway", "name": "Gateway", "os": "linux", "version": "test"}, "auth": "password"}
		case "/api/settings":
			data = map[string]any{"running": true, "platform": "linux", "sharing_enabled": false, "allowed_peers": []any{}}
		case "/api/limits":
			data = map[string]any{"limits": []any{}}
		case "/api/sessions":
			data = map[string]any{} // Make the real component throw on the malformed list.
		}
		writeJSON(w, data)
	})
	mux.HandleFunc("GET /__test_events", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, storedBrowserErrors(t, g)) })
	mux.HandleFunc("POST /__test_stop", func(w http.ResponseWriter, r *http.Request) { stop <- struct{}{}; w.WriteHeader(204) })
	mux.Handle("/", g.Handler())
	server.Config.Handler = mux
	server.Start()
	fmt.Println("TELEMETRY", server.URL)
	select {
	case <-stop:
	case <-time.After(15 * time.Second):
		t.Error("browser fixture timed out")
	}
}
