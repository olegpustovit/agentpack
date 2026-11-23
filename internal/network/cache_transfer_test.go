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

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/lockfile"
)

func seedPeerCache(t *testing.T, g *Gateway) (string, string) {
	t.Helper()
	key := cache.ComputeKey("peer-entry")
	root := filepath.Join(g.options.Home, "cache", key)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("# Peer entry"), 0644); err != nil {
		t.Fatal(err)
	}
	hash, err := cache.TreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	node, err := g.store.NodeID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.store.DB.Exec("INSERT INTO cache_locations(cache_key,node_id,content_hash,size,verified_at) VALUES (?,?,?,12,1)", key, node, hash); err != nil {
		t.Fatal(err)
	}
	return key, hash
}

func TestPeerCacheRouteChecksEnrollmentPathsAndIntegrity(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	g.options.Library.Store = replicaStore(t)
	if err := g.options.Library.Store.EnrollPeer("trusted"); err != nil {
		t.Fatal(err)
	}
	if err := g.options.Library.Store.EnrollOwnMachine("legacy"); err != nil {
		t.Fatal(err)
	}
	key, hash := seedPeerCache(t, g)
	for _, peer := range []string{"", "unknown", "legacy", "trusted"} {
		r := httptest.NewRequest("GET", "/api/cache/"+key, nil)
		r.SetPathValue("cache_key", key)
		r.Header.Set("X-Agentpack-Peer", "trusted")
		if peer != "" {
			r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, peer))
		}
		w := httptest.NewRecorder()
		g.serveCache(w, r)
		want := 403
		if peer == "trusted" {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("peer %q: %d %s", peer, w.Code, w.Body.String())
		}
		if peer == "trusted" {
			out := t.TempDir()
			if err := cache.ExtractPeerTar(w.Body, out); err != nil {
				t.Fatal(err)
			}
			if actual, err := cache.TreeDigest(out); err != nil || actual != hash {
				t.Fatalf("peer bytes: %s %v", actual, err)
			}
		}
	}
	for _, bad := range []string{"../escape", "bad", strings.ToUpper(key), key + "/child"} {
		r := httptest.NewRequest("GET", "/api/cache/value", nil)
		r.SetPathValue("cache_key", bad)
		w := httptest.NewRecorder()
		g.serveCache(w, r)
		if w.Code != 400 || peerRoute("GET", "/api/cache/"+bad) {
			t.Fatalf("unsafe key %q: %d", bad, w.Code)
		}
	}
	if !peerRoute("GET", "/api/cache/"+key) || peerRoute("POST", "/api/cache/"+key) {
		t.Fatal("incorrect cache peer route")
	}
	if err := os.WriteFile(filepath.Join(g.options.Home, "cache", key, "SKILL.md"), []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/cache/"+key, nil)
	r.SetPathValue("cache_key", key)
	r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "trusted"))
	w := httptest.NewRecorder()
	g.serveCache(w, r)
	if w.Code != 409 {
		t.Fatalf("served changed cache: %d %s", w.Code, w.Body.String())
	}
}

func TestLocalGatewayRelaysApprovedPeerCacheAndMissingGatewayFailsFast(t *testing.T) {
	source, _ := identityGateway(t, t.TempDir(), nil)
	if err := source.auth.store.BindNode(t.Context(), "source"); err != nil {
		t.Fatal(err)
	}
	source.options.Library.Store = replicaStore(t)
	if err := source.options.Library.Store.EnrollPeer("receiver"); err != nil {
		t.Fatal(err)
	}
	key, hash := seedPeerCache(t, source)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/cache/{cache_key}", source.serveCache)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "receiver"))
		mux.ServeHTTP(w, r)
	}))
	defer remote.Close()
	home := t.TempDir()
	receiver, _ := identityGateway(t, home, nil)
	receiver.options.Library.Store = replicaStore(t)
	if err := receiver.options.Library.Store.EnrollPeer("source"); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.auth.store.DB.Exec("INSERT INTO cache_locations(cache_key,node_id,content_hash,size,verified_at) VALUES (?,'source',?,12,1)", key, hash); err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(remote.URL)
	localMux := http.NewServeMux()
	localMux.HandleFunc("GET /api/cache/{cache_key}", func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(withActor(r.Context(), Actor{Kind: "local"}))
		receiver.serveCacheFrom(w, r, func(context.Context, string) (*url.URL, error) { return target, nil })
	})
	local := httptest.NewServer(localMux)
	defer local.Close()
	if err := os.MkdirAll(filepath.Join(home, "network"), 0700); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(nodeRecord{Address: local.Listener.Addr().String(), Secret: strings.Repeat("x", 64)})
	if err := os.WriteFile(filepath.Join(home, "network", "node.json"), record, 0600); err != nil {
		t.Fatal(err)
	}
	pkg := lockfile.Package{CacheKey: key, ContentHash: hash}
	out := t.TempDir()
	if err := FetchCache(t.Context(), home, pkg, out); err != nil {
		t.Fatal(err)
	}
	if actual, err := cache.TreeDigest(out); err != nil || actual != hash {
		t.Fatalf("relayed package changed: %s %v", actual, err)
	}
	if err := os.Remove(filepath.Join(home, "network", "node.json")); err != nil {
		t.Fatal(err)
	}
	if err := FetchCache(t.Context(), home, pkg, t.TempDir()); err == nil {
		t.Fatal("missing gateway produced a peer package")
	}
}
