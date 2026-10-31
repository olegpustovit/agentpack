package network

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testWebFiles() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte("<html>app</html>")},
		"icon.svg":               {Data: []byte("<svg/>")},
		"manifest.webmanifest":   {Data: []byte("{}")},
		"assets/index-abc.js":    {Data: []byte("console.log(1)")},
		"assets/index-abc.js.gz": {Data: []byte("GZIPPED")},
		"assets/style-def.css":   {Data: []byte("body{}")},
	}
}

func TestWebAssetsDecisionTable(t *testing.T) {
	handler := webAssets(testWebFiles())
	cases := []struct {
		name, method, path, accept string
		status                     int
		contentType, cache         string
		body                       string
		encoding                   string
	}{
		{name: "root", method: "GET", path: "/", status: 200, contentType: "text/html; charset=utf-8", cache: "no-cache", body: "<html>app</html>"},
		{name: "session deep link", method: "GET", path: "/sessions/local/x", status: 200, contentType: "text/html; charset=utf-8", cache: "no-cache", body: "<html>app</html>"},
		{name: "library dotted id", method: "GET", path: "/library/package/github.com/a/b", status: 200, contentType: "text/html; charset=utf-8", cache: "no-cache", body: "<html>app</html>"},
		{name: "login route", method: "GET", path: "/login", status: 200, contentType: "text/html; charset=utf-8", cache: "no-cache", body: "<html>app</html>"},
		{name: "hashed js", method: "GET", path: "/assets/index-abc.js", status: 200, contentType: "text/javascript; charset=utf-8", cache: "public, max-age=31536000, immutable", body: "console.log(1)"},
		{name: "gzip sibling", method: "GET", path: "/assets/index-abc.js", accept: "gzip, br", status: 200, contentType: "text/javascript; charset=utf-8", cache: "public, max-age=31536000, immutable", body: "GZIPPED", encoding: "gzip"},
		{name: "missing asset is never html", method: "GET", path: "/assets/missing.js", status: 404},
		{name: "svg icon", method: "GET", path: "/icon.svg", status: 200, contentType: "image/svg+xml", cache: "no-cache", body: "<svg/>"},
		{name: "manifest", method: "GET", path: "/manifest.webmanifest", status: 200, contentType: "application/manifest+json", cache: "no-cache", body: "{}"},
		{name: "missing png", method: "GET", path: "/missing.png", status: 404},
		{name: "post refused", method: "POST", path: "/anything", status: 405},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.accept != "" {
				r.Header.Set("Accept-Encoding", tc.accept)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status: want %d got %d body %q", tc.status, w.Code, w.Body.String())
			}
			if tc.status != 200 {
				return
			}
			if got := w.Header().Get("Content-Type"); got != tc.contentType {
				t.Fatalf("content-type: want %q got %q", tc.contentType, got)
			}
			if got := w.Header().Get("Cache-Control"); got != tc.cache {
				t.Fatalf("cache-control: want %q got %q", tc.cache, got)
			}
			if got := w.Header().Get("Content-Encoding"); got != tc.encoding {
				t.Fatalf("content-encoding: want %q got %q", tc.encoding, got)
			}
			if tc.encoding != "" && w.Header().Get("Vary") != "Accept-Encoding" {
				t.Fatalf("vary: %q", w.Header().Get("Vary"))
			}
			if got := w.Body.String(); got != tc.body {
				t.Fatalf("body: want %q got %q", tc.body, got)
			}
		})
	}
}

func TestWebAssetsServesPlaceholderWithoutBuild(t *testing.T) {
	files := fstest.MapFS{"placeholder.html": {Data: []byte("not built")}}
	for _, target := range []string{"/", "/sessions/local/x"} {
		rec := httptest.NewRecorder()
		webAssets(files).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "not built" {
			t.Fatalf("%s: got %d %q", target, rec.Code, rec.Body.String())
		}
	}
}
