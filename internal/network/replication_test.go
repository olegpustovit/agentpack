package network

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/registry"
)

func replicaStore(t *testing.T) *registry.Store {
	t.Helper()
	store, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func registryGateway(t *testing.T, node, caller string) (*Gateway, *httptest.Server) {
	t.Helper()
	g, _ := identityGateway(t, t.TempDir(), nil)
	if err := g.auth.store.BindNode(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	store, err := registry.New(g.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	g.options.Library.Store = store
	mux := http.NewServeMux()
	g.registerStateSync(mux)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, caller)))
	}))
	t.Cleanup(server.Close)
	return g, server
}

func pullRegistry(t *testing.T, to *Gateway, server *httptest.Server) error {
	t.Helper()
	target, _ := url.Parse(server.URL)
	_, _, err := pullState(t.Context(), to.auth.store, to.options.Library.Store, server.Client(), target)
	return err
}

func TestLibraryUsesStatePagesAndConcurrentConflictResolution(t *testing.T) {
	a, aServer := registryGateway(t, "a", "b")
	b, bServer := registryGateway(t, "b", "a")
	pack, revision, err := a.options.Library.Store.Capture(registry.CaptureInput{Name: "Example", Project: "/a", Manifest: []byte("name='example'\n"), Lock: []byte("lockfile_version=2\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.options.Library.Store.EnrollPeer("a"); err != nil {
		t.Fatal(err)
	}
	if err := pullRegistry(t, b, aServer); err == nil {
		t.Fatal("source accepted an unenrolled peer")
	}
	if err := a.options.Library.Store.EnrollPeer("b"); err != nil {
		t.Fatal(err)
	}
	if err := pullRegistry(t, b, aServer); err != nil {
		t.Fatal(err)
	}
	got, revisions, err := b.options.Library.Store.Get(pack.ID)
	if err != nil || got.Name != "Example" || len(revisions) != 1 || revisions[0].ID != revision.ID {
		t.Fatalf("replica: %+v %+v %v", got, revisions, err)
	}
	for name, g := range map[string]*Gateway{"left": a, "right": b} {
		if _, _, err := g.options.Library.Store.Capture(registry.CaptureInput{ID: pack.ID, Project: "/" + name, Manifest: []byte("name='" + name + "'\n"), Lock: []byte("lockfile_version=2\n")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := pullRegistry(t, b, aServer); err != nil {
		t.Fatal(err)
	}
	if err := pullRegistry(t, a, bServer); err != nil {
		t.Fatal(err)
	}
	left, err := a.options.Library.Store.Conflict(pack.ID)
	if err != nil || len(left.Fields) == 0 {
		t.Fatalf("conflict: %+v %v", left, err)
	}
	right, err := b.options.Library.Store.Conflict(pack.ID)
	if err != nil || !reflect.DeepEqual(left, right) {
		t.Fatalf("conflicts differ: %+v %+v %v", left, right, err)
	}
	choices := map[string]string{}
	for _, field := range left.Fields {
		choices[field.Path] = left.Heads[0]
	}
	if _, err := b.options.Library.Store.Resolve(pack.ID, left.Heads, choices, "b"); err != nil {
		t.Fatal(err)
	}
	if err := pullRegistry(t, a, bServer); err != nil {
		t.Fatal(err)
	}
	if heads, err := a.options.Library.Store.Heads(pack.ID); err != nil || len(heads) != 1 {
		t.Fatalf("resolved heads: %v %v", heads, err)
	}
	if _, err := b.options.Library.Store.DeleteAgentpack(pack.ID); err != nil {
		t.Fatal(err)
	}
	if err := pullRegistry(t, a, bServer); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.options.Library.Store.Get(pack.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatal("deletion did not replicate", err)
	}
}

func TestLibraryStatePageRejectsChangedDigestBeforeCursor(t *testing.T) {
	a, server := registryGateway(t, "a", "b")
	b, _ := registryGateway(t, "b", "a")
	if err := a.options.Library.Store.EnrollPeer("b"); err != nil {
		t.Fatal(err)
	}
	if err := b.options.Library.Store.EnrollPeer("a"); err != nil {
		t.Fatal(err)
	}
	_, revision, err := a.options.Library.Store.Capture(registry.CaptureInput{Name: "Digest", Project: "/a", Manifest: []byte("name='digest'\n"), Lock: []byte("lockfile_version=2\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := pullRegistry(t, b, server); err != nil {
		t.Fatal(err)
	}
	site, err := a.auth.store.SiteID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := b.auth.store.Cursor(t.Context(), site)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.auth.store.DB.Exec("UPDATE registry_objects SET body='{}' WHERE digest=?", revision.ID); err != nil {
		t.Fatal(err)
	}
	if err := pullRegistry(t, b, server); err == nil {
		t.Fatal("changed object digest accepted")
	}
	if after, err := b.auth.store.Cursor(t.Context(), site); err != nil || after != cursor {
		t.Fatalf("failed validation advanced cursor: %d %d %v", cursor, after, err)
	}
	var body string
	if err := b.auth.store.Reader.QueryRow("SELECT body FROM registry_objects WHERE digest=?", revision.ID).Scan(&body); err != nil || !strings.Contains(body, "digest") {
		t.Fatalf("valid local object overwritten: %s %v", body, err)
	}
}

func TestReplicationIdentityCannotBeSuppliedInHeaders(t *testing.T) {
	g, _ := registryGateway(t, "local-node", "claimed")
	if err := g.options.Library.Store.EnrollPeer("claimed"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	g.registerStateSync(mux)
	request := httptest.NewRequest("GET", "/api/sync/changes?schema=1", nil)
	request.Header.Set("X-Agentpack-Peer", "claimed")
	request.Header.Set("Authorization", "Bearer local-secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("invented identity admitted", response.Code)
	}
	for _, method := range []string{"GET", "POST", "DELETE"} {
		if peerRoute(method, replicationPrefix+"peers") || peerRoute(method, replicationPrefix+"sync") {
			t.Fatal("peer administration exposed to peer channel")
		}
	}
}
