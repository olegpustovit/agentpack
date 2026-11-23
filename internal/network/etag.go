package network

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// EtagMaxBody bounds buffering; a larger response streams through without an ETag.
const EtagMaxBody = 8 << 20

// ConditionalAPI gives GET and HEAD responses of a routeTable row with ETags a content ETag so repeat reads can answer 304.
func ConditionalAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A stream (the hub's live conversation) must flush as it goes, never buffer.
		streaming := r.URL.Query().Get("stream") == "1" || strings.Contains(r.Header.Get("Accept"), "text/event-stream")
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || streaming {
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
	if rec.body.Len() > EtagMaxBody {
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
		// A handler that names its content (a worker's conversation sequence) skips the hash.
		tag := rec.Header().Get("ETag")
		if tag == "" {
			sum := sha256.Sum256(rec.body.Bytes())
			tag = `"` + hex.EncodeToString(sum[:8]) + `"`
			rec.Header().Set("ETag", tag)
		}
		rec.Header().Set("Cache-Control", "no-cache, private")
		if etagMatches(r.Header.Get("If-None-Match"), tag) {
			rec.ResponseWriter.WriteHeader(http.StatusNotModified)
			return
		}
	}
	body := rec.body.Bytes()
	rec.Header().Add("Vary", "Accept-Encoding")
	if len(body) >= gzipMinBody && rec.Header().Get("Content-Encoding") == "" && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		body = gzipped(body)
		rec.Header().Set("Content-Encoding", "gzip")
		rec.Header().Del("Content-Length")
	}
	rec.ResponseWriter.WriteHeader(status)
	_, _ = rec.ResponseWriter.Write(body)
}

// Below this size the gzip header and CPU cost more than the bytes saved.
const gzipMinBody = 1024

var gzipWriters = sync.Pool{New: func() any {
	writer, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
	return writer
}}

// gzipped compresses an already buffered API response; JSON lists shrink several times over a tailnet link.
func gzipped(body []byte) []byte {
	var out bytes.Buffer
	writer := gzipWriters.Get().(*gzip.Writer)
	writer.Reset(&out)
	_, _ = writer.Write(body)
	_ = writer.Close()
	gzipWriters.Put(writer)
	return out.Bytes()
}

func etagMatches(header, tag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		if strings.TrimPrefix(strings.TrimSpace(candidate), "W/") == tag {
			return true
		}
	}
	return false
}
