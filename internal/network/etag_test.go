package network

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serveConditional(t *testing.T, body string, method, path, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	handler := conditionalAPI(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
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
	large := strings.Repeat("x", etagMaxBody+1)
	rec := serveConditional(t, large, http.MethodGet, "/api/packages", "")
	if rec.Header().Get("ETag") != "" {
		t.Fatalf("ETag set on body above the %d byte cap", etagMaxBody)
	}
	if !bytes.Equal(rec.Body.Bytes(), []byte(large)) {
		t.Fatalf("large body truncated: got %d bytes, want %d", rec.Body.Len(), len(large))
	}
}
