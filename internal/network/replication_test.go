package network

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/olegpustovit/agentpack/internal/registry"
)

func replicaStore(t *testing.T) *registry.Store {
	t.Helper()
	store, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRegistryExchangeRequiresEnrollmentAndVerifiesTransfers(t *testing.T) {
	a, b := replicaStore(t), replicaStore(t)
	pack, revision, err := a.Capture(registry.CaptureInput{Name: "Example", Project: t.TempDir(), Manifest: []byte("name = 'example'\n[dependencies]\n"), Lock: []byte("lockfile_version = 2\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EnrollPeer("b"); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{options: GatewayOptions{Library: LibraryOptions{Store: b}}}
	mux := http.NewServeMux()
	g.registerRegistryReplication(mux)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Production attaches this identity only after daemon whois and sharing checks.
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "a")))
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	if err := exchangeRegistry(context.Background(), a, "b", target, server.Client()); err == nil {
		t.Fatal("receiver did not require its own explicit enrollment")
	}
	if err := b.EnrollPeer("a"); err != nil {
		t.Fatal(err)
	}
	if err := exchangeRegistry(context.Background(), a, "b", target, server.Client()); err != nil {
		t.Fatal(err)
	}
	replicated, revisions, err := b.Get(pack.ID)
	if err != nil || replicated.Name != "Example" || len(revisions) != 1 || revisions[0].ID != revision.ID {
		t.Fatalf("replica: %#v %+v %v", replicated, revisions, err)
	}
	peers, err := a.ListPeers()
	if err != nil || len(peers) != 1 || peers[0].ReceiptCount != 2 {
		t.Fatalf("receipt: %+v %v", peers, err)
	}
	before, err := b.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	bad := []byte(`{"arbitrary":"secret"}`)
	body, _ := json.Marshal(replicaTransfer{Objects: map[string][]byte{revision.ID: bad}})
	response, err := server.Client().Post(server.URL+replicationPrefix+"objects", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("digest mismatch accepted: %d", response.StatusCode)
	}
	after, err := b.Inventory()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed transfer changed inventory", err)
	}
	if _, err := b.DeleteAgentpack(pack.ID); err != nil {
		t.Fatal(err)
	}
	if err := exchangeRegistry(context.Background(), a, "b", target, server.Client()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Get(pack.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatal("tombstone did not propagate", err)
	}
	if err := exchangeRegistry(context.Background(), a, "b", target, server.Client()); err != nil {
		t.Fatal("repeated exchange resurrected or rejected deleted revisions", err)
	}
}

func TestReplicationIdentityCannotBeSuppliedInHeaders(t *testing.T) {
	store := replicaStore(t)
	if err := store.EnrollPeer("claimed"); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{options: GatewayOptions{Library: LibraryOptions{Store: store}}}
	mux := http.NewServeMux()
	g.registerRegistryReplication(mux)
	r := httptest.NewRequest("GET", replicationPrefix+"inventory", nil)
	r.Header.Set("X-Agentpack-Peer", "claimed")
	r.Header.Set("Authorization", "Bearer local-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("invented identity admitted", w.Code)
	}
	sum := sha256.Sum256([]byte("object"))
	for _, test := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", replicationPrefix + "inventory", true},
		{"GET", replicationPrefix + "objects/" + hex.EncodeToString(sum[:]), true},
		{"POST", replicationPrefix + "objects", true},
		{"POST", replicationPrefix + "ack", true},
		{"POST", replicationPrefix + "peers", false},
		{"GET", replicationPrefix + "peers", false},
		{"POST", replicationPrefix + "sync", false},
		{"GET", replicationPrefix + "objects/../../bindings", false},
	} {
		if got := peerRoute(test.method, test.path); got != test.allowed {
			t.Fatalf("%s %s allowed=%v", test.method, test.path, got)
		}
	}
}
