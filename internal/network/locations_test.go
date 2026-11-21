package network

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func TestCacheLocationsAdvertiseOnlyUnchangedVerifiedTrees(t *testing.T) {
	home := t.TempDir()
	g, _ := identityGateway(t, home, nil)
	if err := g.auth.store.BindNode(t.Context(), "node-local"); err != nil {
		t.Fatal(err)
	}
	key := cache.ComputeKey("verified-tree")
	root := filepath.Join(home, "cache", key)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("checked bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	metadata, _, err := paths.TreeMetadata(root)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := cache.TreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "verified"), 0755); err != nil {
		t.Fatal(err)
	}
	stamp, _ := json.Marshal(map[string]string{"content_hash": digest, "metadata": metadata})
	if err := os.WriteFile(filepath.Join(home, "verified", key+".json"), stamp, 0644); err != nil {
		t.Fatal(err)
	}
	missing := cache.ComputeKey("missing")
	if err := os.WriteFile(filepath.Join(home, "verified", missing+".json"), stamp, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := g.auth.store.DB.Exec(`INSERT INTO cache_locations(cache_key,node_id,content_hash) VALUES (?,'peer','peer hash'),(?,'node-local','stale')`, missing, missing); err != nil {
		t.Fatal(err)
	}
	if err := g.advertiseCacheLocations(t.Context()); err != nil {
		t.Fatal(err)
	}
	var hash string
	var size, verified int64
	if err := g.auth.store.Reader.QueryRow("SELECT content_hash,size,verified_at FROM cache_locations WHERE node_id='node-local' AND cache_key=?", key).Scan(&hash, &size, &verified); err != nil || hash != digest || size != int64(len("checked bytes")) || verified == 0 {
		t.Fatalf("advertisement %s %d %d %v", hash, size, verified, err)
	}
	version, err := g.auth.store.DBVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := g.advertiseCacheLocations(t.Context()); err != nil {
		t.Fatal(err)
	}
	unchanged, err := g.auth.store.DBVersion(t.Context())
	if err != nil || unchanged != version {
		t.Fatalf("unchanged scan churned state: %d -> %d %v", version, unchanged, err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("changed bytes outside verification"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := g.advertiseCacheLocations(t.Context()); err != nil {
		t.Fatal(err)
	}
	var local, peer int
	if err := g.auth.store.Reader.QueryRow("SELECT count(*) FROM cache_locations WHERE node_id='node-local'").Scan(&local); err != nil {
		t.Fatal(err)
	}
	if err := g.auth.store.Reader.QueryRow("SELECT count(*) FROM cache_locations WHERE node_id='peer'").Scan(&peer); err != nil {
		t.Fatal(err)
	}
	if local != 0 || peer != 1 {
		t.Fatalf("stale pruning touched peers: local=%d peer=%d", local, peer)
	}
}

func TestBlobLocationsFollowWorkerUploadAndStableNodeBinding(t *testing.T) {
	home := t.TempDir()
	g, _ := identityGateway(t, home, nil)
	worker := &WorkerHTTP{stateStore: g.auth.store, sessionDir: filepath.Join(home, "network", "sessions", "session-image")}
	sha := strings.Repeat("a", 64)
	if err := advertiseBlob(t.Context(), worker.stateStore, filepath.Base(worker.sessionDir), sha, 123); err != nil {
		t.Fatal(err)
	}
	if err := g.auth.store.BindNode(t.Context(), "stable"); err != nil {
		t.Fatal(err)
	}
	if err := advertiseBlob(t.Context(), worker.stateStore, filepath.Base(worker.sessionDir), sha, 123); err != nil {
		t.Fatal(err)
	}
	var node, session string
	var size int64
	if err := g.auth.store.Reader.QueryRow("SELECT node_id,session_id,size FROM blob_locations WHERE sha=?", sha).Scan(&node, &session, &size); err != nil || node != "stable" || session != "session-image" || size != 123 {
		t.Fatalf("blob location: %s %s %d %v", node, session, size, err)
	}
	if _, err := g.auth.store.DB.Exec("INSERT INTO cache_locations(cache_key,node_id) VALUES (?,?)", cache.ComputeKey("pending"), "stable"); err != nil {
		t.Fatal(err)
	}
	if err := g.auth.store.BindNode(t.Context(), "another"); err == nil {
		t.Fatal("stable node rebound unexpectedly")
	}
}
