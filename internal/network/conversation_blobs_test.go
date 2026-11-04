package network

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreAndServeSessionBlob(t *testing.T) {
	dir := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00}
	sha, err := storeSessionBlob(dir, png)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(blobPath(dir, sha)); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	serveSessionBlob(rec, httptest.NewRequest(http.MethodGet, "/", nil), dir, sha)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("type = %q", rec.Header().Get("Content-Type"))
	}
	for name, want := range map[string]string{
		"Content-Disposition":     "inline",
		"Content-Security-Policy": "sandbox",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatal("body mismatch")
	}
}

func TestServeSessionBlobRejectsBadSHA(t *testing.T) {
	rec := httptest.NewRecorder()
	serveSessionBlob(rec, httptest.NewRequest(http.MethodGet, "/", nil), t.TempDir(), "not-a-sha")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestStoreSessionBlobRejectsSVG(t *testing.T) {
	_, err := storeSessionBlob(filepath.Join(t.TempDir()), []byte("<svg></svg>"))
	if err == nil {
		t.Fatal("expected rejection")
	}
}
func TestProxySessionBlobPreservesSafeHeadersAndCapsBody(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	headers := make(http.Header)
	headers.Set("Content-Type", "image/png")
	headers.Set("Content-Disposition", "inline")
	headers.Set("Content-Security-Policy", "sandbox")
	headers.Set("X-Content-Type-Options", "nosniff")
	recorder := httptest.NewRecorder()
	proxySessionBlob(recorder, &http.Response{
		StatusCode: http.StatusOK,
		Header:     headers,
		Body:       io.NopCloser(bytes.NewReader(png)),
	})
	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), png) {
		t.Fatalf("response = %d %x", recorder.Code, recorder.Body.Bytes())
	}
	for name, want := range map[string]string{
		"Content-Type":            "image/png",
		"Content-Disposition":     "inline",
		"Content-Security-Policy": "sandbox",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := recorder.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	oversized := httptest.NewRecorder()
	proxySessionBlob(oversized, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxBlobBytes+1))),
	})
	if oversized.Code != http.StatusBadGateway {
		t.Fatalf("oversized response status = %d, want 502", oversized.Code)
	}
}
func TestBlobQuotaLimitsBytesAndFileCount(t *testing.T) {
	if blobQuotaExceeded(maxSessionBlobBytes-1, 1, 1) {
		t.Fatal("last byte within quota was rejected")
	}
	for _, tc := range []struct {
		used, incoming int64
		files          int
	}{
		{used: maxSessionBlobBytes, incoming: 1},
		{used: maxSessionBlobBytes - 1, incoming: 2},
		{used: 0, files: maxSessionBlobFiles, incoming: 1},
	} {
		if !blobQuotaExceeded(tc.used, tc.files, tc.incoming) {
			t.Fatalf("quota accepted used=%d files=%d incoming=%d", tc.used, tc.files, tc.incoming)
		}
	}
}

func TestWorkerBlobUploadEnforcesSessionQuota(t *testing.T) {
	sessionDir := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	for i := 0; i < maxSessionBlobFiles; i++ {
		image := append(append([]byte(nil), png...), byte(i))
		if _, err := storeSessionBlob(sessionDir, image); err != nil {
			t.Fatalf("store image %d: %v", i, err)
		}
	}
	duplicate := append(append([]byte(nil), png...), byte(0))
	if _, err := storeSessionBlob(sessionDir, duplicate); err != nil {
		t.Fatalf("deduplicated image: %v", err)
	}
	worker := &WorkerHTTP{sessionDir: sessionDir}
	mux := http.NewServeMux()
	worker.registerConversation(mux)
	image := append(append([]byte(nil), png...), byte(0xff))
	server := httptest.NewServer(mux)
	defer server.Close()
	response, err := http.Post(server.URL+"/conversation/blobs", "image/png", bytes.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("upload status = %d", response.StatusCode)
	}
}

func TestConversationBlobProxyAcceptsMoreThanJSONBodyLimit(t *testing.T) {
	const size = 2 << 20
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "image/png" {
			http.Error(w, "wrong content type", http.StatusUnsupportedMediaType)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil || len(data) != size {
			http.Error(w, "truncated upload", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	record := WorkerRecord{Address: upstream.Listener.Addr().String(), Secret: strings.Repeat("x", 64)}
	request := httptest.NewRequest(http.MethodPost, "/conversation/blobs", bytes.NewReader(make([]byte, size)))
	request.Header.Set("Content-Type", "image/png")
	recorder := httptest.NewRecorder()
	proxyWorkerConversation(recorder, request, record, "/blobs")
	if recorder.Code != http.StatusOK {
		t.Fatalf("proxy status = %d body=%q", recorder.Code, recorder.Body.String())
	}
}
