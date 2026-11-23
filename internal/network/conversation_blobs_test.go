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

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/state/statetest"
)

func TestStoreAndServeSessionBlob(t *testing.T) {
	dir := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00}
	sha, err := StoreSessionBlob(dir, png)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(BlobPath(dir, sha)); err != nil {
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
	_, err := StoreSessionBlob(filepath.Join(t.TempDir()), []byte("<svg></svg>"))
	if err == nil {
		t.Fatal("expected rejection")
	}
}
func TestBlobQuotaLimitsBytesAndFileCount(t *testing.T) {
	if blobQuotaExceeded(MaxSessionBlobBytes-1, 1, 1) {
		t.Fatal("last byte within quota was rejected")
	}
	for _, tc := range []struct {
		used, incoming int64
		files          int
	}{
		{used: MaxSessionBlobBytes, incoming: 1},
		{used: MaxSessionBlobBytes - 1, incoming: 2},
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
		if _, err := StoreSessionBlob(sessionDir, image); err != nil {
			t.Fatalf("store image %d: %v", i, err)
		}
	}
	duplicate := append(append([]byte(nil), png...), byte(0))
	if _, err := StoreSessionBlob(sessionDir, duplicate); err != nil {
		t.Fatalf("deduplicated image: %v", err)
	}
	worker := &WorkerHTTP{SessionDir: sessionDir}
	mux := http.NewServeMux()
	worker.RegisterConversation(mux)
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
	ProxyWorkerConversation(recorder, request, record, "/blobs")
	if recorder.Code != http.StatusOK {
		t.Fatalf("proxy status = %d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestBlobLocationsFollowWorkerUploadAndStableNodeBinding(t *testing.T) {
	home := t.TempDir()
	store := statetest.Open(t, home)
	worker := &WorkerHTTP{StateStore: store, SessionDir: filepath.Join(home, "network", "sessions", "session-image")}
	sha := strings.Repeat("a", 64)
	if err := AdvertiseBlob(t.Context(), worker.StateStore, filepath.Base(worker.SessionDir), sha, 123); err != nil {
		t.Fatal(err)
	}
	if err := store.BindNode(t.Context(), "stable"); err != nil {
		t.Fatal(err)
	}
	if err := AdvertiseBlob(t.Context(), worker.StateStore, filepath.Base(worker.SessionDir), sha, 123); err != nil {
		t.Fatal(err)
	}
	var node, session string
	var size int64
	if err := store.Reader.QueryRow("SELECT node_id,session_id,size FROM blob_locations WHERE sha=?", sha).Scan(&node, &session, &size); err != nil || node != "stable" || session != "session-image" || size != 123 {
		t.Fatalf("blob location: %s %s %d %v", node, session, size, err)
	}
	if _, err := store.DB.Exec("INSERT INTO cache_locations(cache_key,node_id) VALUES (?,?)", cache.ComputeKey("pending"), "stable"); err != nil {
		t.Fatal(err)
	}
	if err := store.BindNode(t.Context(), "another"); err == nil {
		t.Fatal("stable node rebound unexpectedly")
	}
}
