package gateway

import (
	"bytes"
	"compress/gzip"
	"github.com/olegpustovit/agentpack/internal/network"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serveConditional(t *testing.T, body string, method, path, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	if row, _ := routeFor(method, path); row.etag() {
		handler = network.ConditionalAPI(handler)
	}
	req := httptest.NewRequest(method, path, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestConditionalAPIETagLifecycle(t *testing.T) {
	first := serveConditional(t, `{"a":1}`, http.MethodGet, "/api/sessions", "")
	if first.Code != http.StatusOK || first.Body.String() != `{"a":1}` {
		t.Fatalf("first response: code %d body %q", first.Code, first.Body.String())
	}
	tag := first.Header().Get("ETag")
	if len(tag) != 18 || tag[0] != '"' || tag[17] != '"' {
		t.Fatalf("ETag %q is not 16 hex digits in quotes", tag)
	}
	if got := first.Header().Get("Cache-Control"); got != "no-cache, private" {
		t.Fatalf("Cache-Control = %q", got)
	}

	if again := serveConditional(t, `{"a":1}`, http.MethodGet, "/api/sessions", ""); again.Header().Get("ETag") != tag {
		t.Fatalf("identical body gave ETag %q, want %q", again.Header().Get("ETag"), tag)
	}

	notModified := serveConditional(t, `{"a":1}`, http.MethodGet, "/api/sessions", tag)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("matching If-None-Match: code %d body %q", notModified.Code, notModified.Body.String())
	}

	changed := serveConditional(t, `{"a":2}`, http.MethodGet, "/api/sessions", tag)
	if changed.Code != http.StatusOK || changed.Body.String() != `{"a":2}` || changed.Header().Get("ETag") == tag {
		t.Fatalf("changed body with stale tag: code %d etag %q body %q", changed.Code, changed.Header().Get("ETag"), changed.Body.String())
	}
}

func TestConditionalAPIExclusions(t *testing.T) {
	cases := []struct {
		name, method, path string
	}{
		{"terminal stream", http.MethodGet, "/api/sessions/s1/terminal"},
		{"output stream", http.MethodGet, "/api/sessions/s1/output"},
		{"events", http.MethodGet, "/api/events"},
		{"sync events", http.MethodGet, "/api/sync/events"},
		{"status", http.MethodGet, "/api/status"},
		{"mutation", http.MethodPost, "/api/sessions"},
		{"non api", http.MethodGet, "/sessions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveConditional(t, "x", tc.method, tc.path, "")
			if rec.Header().Get("ETag") != "" {
				t.Fatalf("ETag set on excluded path %s %s", tc.method, tc.path)
			}
			if rec.Header().Get("Cache-Control") != "" {
				t.Fatalf("Cache-Control overwritten on excluded path: %q", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestConditionalAPIStreamsLargeBodiesWithoutETag(t *testing.T) {
	large := strings.Repeat("x", network.EtagMaxBody+1)
	rec := serveConditional(t, large, http.MethodGet, "/api/packages", "")
	if rec.Header().Get("ETag") != "" {
		t.Fatalf("ETag set on body above the %d byte cap", network.EtagMaxBody)
	}
	if !bytes.Equal(rec.Body.Bytes(), []byte(large)) {
		t.Fatalf("large body truncated: got %d bytes, want %d", rec.Body.Len(), len(large))
	}
}

func TestConditionalAPILetsStreamsFlush(t *testing.T) {
	handler := network.ConditionalAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		}
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/sessions/s1/conversation?since=0&stream=1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("stream through conditionalAPI = %d %s", recorder.Code, recorder.Body)
	}
}

func TestConditionalAPIGzipsLargeBodiesForClientsThatAccept(t *testing.T) {
	body := `[` + strings.Repeat(`{"id":"session","state":"idle"},`, 100) + `{}]`
	handler := network.ConditionalAPI(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	for _, accept := range []string{"", "gzip, deflate, br"} {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
		req.Header.Set("Accept-Encoding", accept)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		got := rec.Body.Bytes()
		if accept != "" {
			if rec.Header().Get("Content-Encoding") != "gzip" || len(got) >= len(body) {
				t.Fatalf("gzip not applied: encoding %q, %d bytes", rec.Header().Get("Content-Encoding"), len(got))
			}
			reader, err := gzip.NewReader(bytes.NewReader(got))
			if err != nil {
				t.Fatal(err)
			}
			if got, err = io.ReadAll(reader); err != nil {
				t.Fatal(err)
			}
		}
		if string(got) != body || !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
			t.Fatalf("accept %q: body differs or Vary missing", accept)
		}
	}
}
