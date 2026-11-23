package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/olegpustovit/agentpack/internal/network"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPeerBlobFetchChecksHashCachesAndEnforcesQuota(t *testing.T) {
	source, _ := identityGateway(t, t.TempDir(), nil)
	receiver, _ := identityGateway(t, t.TempDir(), nil)
	for node, g := range map[string]*Gateway{"source": source, "receiver": receiver} {
		if err := g.store.BindNode(t.Context(), node); err != nil {
			t.Fatal(err)
		}
		g.options.Library.Store = replicaStore(t)
	}
	if err := source.options.Library.Store.EnrollPeer("receiver"); err != nil {
		t.Fatal(err)
	}
	if err := receiver.options.Library.Store.EnrollPeer("source"); err != nil {
		t.Fatal(err)
	}
	png := []byte{0x89, 'P', 'N', 'G', 13, 10, 26, 10, 0, 1}
	sourceDir, err := sessionBlobDir(source.options.Home, "session-source")
	if err != nil {
		t.Fatal(err)
	}
	sha, err := network.StoreSessionBlob(sourceDir, png)
	if err != nil {
		t.Fatal(err)
	}
	if err := network.AdvertiseBlob(t.Context(), source.auth.store, "session-source", sha, int64(len(png))); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.auth.store.DB.Exec("INSERT INTO blob_locations(sha,node_id,session_id,size) VALUES (?,'source','session-source',?)", sha, len(png)); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var corrupt atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}/blobs/{sha}", source.serveBlob)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if corrupt.Load() {
			w.Write([]byte("incorrect bytes"))
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "receiver"))
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	targetFor := func(context.Context, string) (*url.URL, error) { return target, nil }
	data, err := receiver.fetchBlob(t.Context(), "session-copy", sha, targetFor)
	if err != nil || !bytes.Equal(data, png) || calls.Load() != 1 {
		t.Fatalf("peer fetch: %q %v calls=%d", data, err, calls.Load())
	}
	if _, err := receiver.fetchBlob(t.Context(), "session-other", sha, targetFor); err != nil || calls.Load() != 1 {
		t.Fatalf("ignored local copy in another session: %v calls=%d", err, calls.Load())
	}
	var session string
	if err := receiver.auth.store.Reader.QueryRow("SELECT session_id FROM blob_locations WHERE sha=? AND node_id='receiver'", sha).Scan(&session); err != nil || session != "session-other" {
		t.Fatalf("cached location: %s %v", session, err)
	}
	corrupt.Store(true)
	wanted := sha256.Sum256(append(append([]byte(nil), png...), 2))
	badSHA := hex.EncodeToString(wanted[:])
	if _, err := receiver.auth.store.DB.Exec("INSERT INTO blob_locations(sha,node_id,session_id,size) VALUES (?,'source','session-source',?)", badSHA, len(png)+1); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.fetchBlob(t.Context(), "session-corrupt", badSHA, targetFor); !errors.Is(err, network.ErrBlobIntegrity) {
		t.Fatalf("accepted corrupt peer bytes: %v", err)
	}
	badDir, _ := sessionBlobDir(receiver.options.Home, "session-corrupt")
	if _, err := os.Stat(network.BlobPath(badDir, badSHA)); !os.IsNotExist(err) {
		t.Fatalf("published corrupt copy: %v", err)
	}
	quotaDir, _ := sessionBlobDir(receiver.options.Home, "session-full")
	if err := os.MkdirAll(filepath.Join(quotaDir, "blobs"), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(quotaDir, "blobs", strings.Repeat("b", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(network.MaxSessionBlobBytes); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := receiver.fetchBlob(t.Context(), "session-full", sha, targetFor); !errors.Is(err, network.ErrSessionBlobQuota) {
		t.Fatalf("bypassed local quota: %v", err)
	}
	copyDir, _ := sessionBlobDir(receiver.options.Home, "session-copy")
	if err := os.WriteFile(network.BlobPath(copyDir, sha), []byte("corrupt local copy"), 0600); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if _, err := receiver.fetchBlob(t.Context(), "session-copy", sha, targetFor); !errors.Is(err, network.ErrBlobIntegrity) || calls.Load() != before {
		t.Fatalf("silently replaced corrupt local bytes: %v", err)
	}
}

func TestWorkerBlobUploadRecordsLocation(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	dir, _ := sessionBlobDir(g.options.Home, "session-upload")
	worker := &network.WorkerHTTP{StateStore: g.store, SessionDir: dir}
	mux := http.NewServeMux()
	worker.RegisterConversation(mux)
	png := []byte{0x89, 'P', 'N', 'G', 13, 10, 26, 10}
	r := httptest.NewRequest("POST", "/conversation/blobs", bytes.NewReader(png))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var count, size int
	if err := g.store.Reader.QueryRow("SELECT count(*),sum(size) FROM blob_locations WHERE session_id='session-upload'").Scan(&count, &size); err != nil || count != 1 || size != len(png) {
		t.Fatalf("upload location: %d %d %v", count, size, err)
	}
}
