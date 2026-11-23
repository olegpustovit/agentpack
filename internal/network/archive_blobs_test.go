package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/state"
)

func TestArchiveKeepsMainAttachmentsAndVerifiesBeforePeerCleanup(t *testing.T) {
	main, _ := identityGateway(t, t.TempDir(), nil)
	worker, _ := identityGateway(t, t.TempDir(), nil)
	for node, g := range map[string]*Gateway{"main": main, "worker": worker} {
		if err := g.auth.store.BindNode(t.Context(), node); err != nil {
			t.Fatal(err)
		}
		g.options.Library.Store = replicaStore(t)
	}
	if err := main.options.Library.Store.EnrollPeer("worker"); err != nil {
		t.Fatal(err)
	}
	if err := worker.options.Library.Store.EnrollPeer("main"); err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, main, "main", "main")
	const id = "session-archive-images"
	now := time.Now()
	archiveFixture(t, worker, id, now.Add(-8*24*time.Hour), 1, 1)
	png := []byte{0x89, 'P', 'N', 'G', 13, 10, 26, 10, 0, 1}
	workerDir, _ := sessionBlobDir(worker.options.Home, id)
	sha, err := storeSessionBlob(workerDir, png)
	if err != nil {
		t.Fatal(err)
	}
	if err := advertiseBlob(t.Context(), worker.auth.store, id, sha, int64(len(png))); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"seq": 1, "kind": "prompt", "payload": map[string]any{"images": []map[string]string{{"blob": "sha256:" + sha}}}})
	if _, err := worker.auth.store.DB.Exec("UPDATE conversation_events SET body=? WHERE session_id=?", string(body), id); err != nil {
		t.Fatal(err)
	}
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	archive, err := state.OpenArchive(main.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	if moved, err := main.archiveSession(t.Context(), archive, id, now); err == nil || moved {
		t.Fatal("archived without the attachment")
	}
	archive.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}/blobs/{sha}", worker.serveBlob)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "main")))
	}))
	defer source.Close()
	sourceURL, _ := url.Parse(source.URL)
	if err := main.prepareArchiveSession(t.Context(), id, func(context.Context, string) (*url.URL, error) { return sourceURL, nil }); err != nil {
		t.Fatal(err)
	}
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("image archive: %d %v", count, err)
	}
	syncArchiveFixture(t, main.auth.store, worker.auth.store)
	mainDir, _ := sessionBlobDir(main.options.Home, id)
	mainMux := http.NewServeMux()
	mainMux.HandleFunc("GET /api/sessions/{id}/blobs/{sha}", main.serveBlob)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mainMux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "worker")))
	}))
	defer destination.Close()
	destinationURL, _ := url.Parse(destination.URL)
	target := func(context.Context, string) (*url.URL, error) { return destinationURL, nil }
	if err := os.WriteFile(blobPath(mainDir, sha), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := worker.cleanupArchivedBlobs(t.Context(), target); err == nil {
		t.Fatal("stale Main location allowed removal of the last good copy")
	}
	if _, err := readSessionBlob(workerDir, sha); err != nil {
		t.Fatal("removed the source copy", err)
	}
	if err := os.WriteFile(blobPath(mainDir, sha), png, 0600); err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, worker, "main", "old-main")
	if err := worker.cleanupArchivedBlobs(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := readSessionBlob(workerDir, sha); err != nil {
		t.Fatal("removed during handover", err)
	}
	archiveRoles(t, worker, "main", "main")
	if err := os.WriteFile(filepath.Join(workerDir, "environment.json"), []byte("support"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.auth.store.DB.Exec(`CREATE TRIGGER fail_location_delete BEFORE DELETE ON blob_locations
 BEGIN SELECT RAISE(ABORT,'simulated write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := worker.cleanupArchivedBlobs(t.Context(), target); err == nil {
		t.Fatal("location delete unexpectedly succeeded")
	}
	if _, err := readSessionBlob(workerDir, sha); err != nil {
		t.Fatal("file removed before the location commit", err)
	}
	if _, err := worker.auth.store.DB.Exec("DROP TRIGGER fail_location_delete"); err != nil {
		t.Fatal(err)
	}
	if err := worker.cleanupArchivedBlobs(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(blobPath(workerDir, sha)); !os.IsNotExist(err) {
		t.Fatalf("peer image remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workerDir, "environment.json")); err != nil {
		t.Fatal("support file removed", err)
	}
	var count int
	if err := worker.auth.store.Reader.QueryRow("SELECT count(*) FROM blob_locations WHERE node_id='worker' AND session_id=?", id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale own location: %d %v", count, err)
	}
	if err := main.cleanupArchivedBlobs(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := readSessionBlob(mainDir, sha); err != nil {
		t.Fatal("Main image removed", err)
	}
	if err := PrepareArchive(t.Context(), main.auth.store, main.options.Home); err != nil {
		t.Fatal("valid archive attachments refused", err)
	}
	if err := os.Remove(blobPath(mainDir, sha)); err != nil {
		t.Fatal(err)
	}
	if err := PrepareArchive(t.Context(), main.auth.store, main.options.Home); err == nil || !strings.Contains(err.Error(), "archive attachment unavailable") {
		t.Fatalf("handover ignored a missing attachment: %v", err)
	}
}

func TestArchiveBlobRefsUsesPromptAttachments(t *testing.T) {
	sha := strings.Repeat("a", 64)
	refs, err := archiveBlobRefs(`{"kind":"tool_call_update","payload":{"blob":"bogus","image":"irrelevant"}}`)
	if err != nil || len(refs) != 0 {
		t.Fatalf("tool argument mistaken for a file: %v %v", refs, err)
	}
	refs, err = archiveBlobRefs(`{"kind":"prompt","payload":{"image":{"blob":"sha256:` + sha + `"},"resource_link":{"blob":"sha256:` + sha + `"}}}`)
	if err != nil || len(refs) != 1 || refs[0] != sha {
		t.Fatalf("prompt attachments: %v %v", refs, err)
	}
	if _, err := archiveBlobRefs(`{"kind":"prompt","payload":{"images":[{"blob":"sha256:bad"}]}}`); err == nil {
		t.Fatal("invalid attachment accepted")
	}
}
