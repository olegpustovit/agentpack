package network

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// etagMaxBody bounds buffering; a larger response streams through without an ETag.
const etagMaxBody = 8 << 20

// etagPath reports whether an API path gets a content ETag. Live streams and status stay uncached.
func etagPath(p string) bool {
	if !strings.HasPrefix(p, "/api/") || p == "/api/events" || p == "/api/status" {
		return false
	}
	return !strings.HasPrefix(p, "/api/sessions/") || !(strings.HasSuffix(p, "/terminal") || strings.HasSuffix(p, "/output"))
}

// conditionalAPI gives GET and HEAD API responses a content ETag so repeat reads can answer 304.
func conditionalAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || !etagPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		rec := &etagRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		rec.finish(r)
	})
}

type etagRecorder struct {
	http.ResponseWriter
	status   int
	body     bytes.Buffer
	streamed bool
}

func (rec *etagRecorder) WriteHeader(code int) {
	if rec.status == 0 {
		rec.status = code
	}
}

func (rec *etagRecorder) Write(p []byte) (int, error) {
	if rec.streamed {
		return rec.ResponseWriter.Write(p)
	}
	rec.body.Write(p)
	if rec.body.Len() > etagMaxBody {
		rec.streamed = true
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		rec.ResponseWriter.WriteHeader(rec.status)
		_, err := rec.ResponseWriter.Write(rec.body.Bytes())
		rec.body.Reset()
		return len(p), err
	}
	return len(p), nil
}

func (rec *etagRecorder) finish(r *http.Request) {
	if rec.streamed {
		return
	}
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	if status == http.StatusOK {
		sum := sha256.Sum256(rec.body.Bytes())
		tag := `"` + hex.EncodeToString(sum[:8]) + `"`
		rec.Header().Set("ETag", tag)
		rec.Header().Set("Cache-Control", "no-cache, private")
		if etagMatches(r.Header.Get("If-None-Match"), tag) {
			rec.ResponseWriter.WriteHeader(http.StatusNotModified)
			return
		}
	}
	rec.ResponseWriter.WriteHeader(status)
	_, _ = rec.ResponseWriter.Write(rec.body.Bytes())
}

func etagMatches(header, tag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		if strings.TrimPrefix(strings.TrimSpace(candidate), "W/") == tag {
			return true
		}
	}
	return false
}
