package network

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/registry"
)

func TestLibraryRoutesPreserveTrustAndPersistRename(t *testing.T) {
	home, directory := t.TempDir(), t.TempDir()
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	store, err := registry.New(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := remember(home, SessionInfo{Directory: directory, Harness: "claude"}); err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	called := 0
	bootstrapped := 0
	g := &Gateway{auth: auth, origin: origin, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home, Library: LibraryOptions{
		Store:     store,
		Bootstrap: func() error { bootstrapped++; return nil },
		Save: func(_ context.Context, input SaveAgentpackInput) (SavedAgentpack, error) {
			called++
			pack, revision, err := store.Capture(registry.CaptureInput{Project: input.Directory, Name: input.Name,
				Manifest: []byte("name = 'fixture'\n[dependencies]\n"), Lock: []byte("lockfile_version = 2\n")})
			return SavedAgentpack{Agentpack: pack, Revision: revision}, err
		},
		DraftState: func(_ context.Context, draft registry.Draft) (registry.DraftState, error) {
			return registry.DraftState{Pins: map[string]registry.PackagePin{}, CurrentDiff: &registry.DiffResult{Add: []string{"github.com/example/skill"}}}, nil
		},
	}}}
	handler := g.Handler()
	request := func(method, path string, body any, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, origin.String()+path, bytes.NewReader(data))
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		} else {
			r.Header.Set("Origin", origin.String())
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	input := SaveAgentpackInput{Directory: directory, Name: "Review"}
	if w := request("POST", "/api/library/agentpacks", input, false); w.Code != http.StatusUnauthorized || called != 0 {
		t.Fatalf("unauthenticated capture: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/api/library/agentpacks", SaveAgentpackInput{Directory: t.TempDir()}, true); w.Code != http.StatusBadRequest || called != 0 {
		t.Fatalf("untrusted capture: %d %s", w.Code, w.Body.String())
	}
	w := request("POST", "/api/library/agentpacks", input, true)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var saved SavedAgentpack
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	path := "/api/library/agentpacks/" + saved.Agentpack.ID
	if w := request("PATCH", path, map[string]string{"name": "Team review"}, true); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", path, nil, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Team review") || !strings.Contains(w.Body.String(), saved.Revision.ID) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/api/library/packages", nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("Library package route: %d %s", w.Code, w.Body.String())
	}
	w = request("GET", "/api/library/agentpacks", nil, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), saved.Agentpack.ID) {
		t.Fatal(w.Code, w.Body.String())
	}
	if bootstrapped != 2 {
		t.Fatalf("Library list routes did not bootstrap: %d", bootstrapped)
	}
	identity, err := FolderIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	w = request("POST", "/api/library/drafts", registry.DraftInput{Item: "github.com/example/skill@" + strings.Repeat("a", 40), Node: "local", Directory: directory, Reason: "Offline", FolderIdentity: identity}, true)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var draft registry.Draft
	if err := json.Unmarshal(w.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	w = request("POST", "/api/library/drafts/"+draft.ID+"/review", map[string]any{}, true)
	var review registry.DraftReview
	if err := json.Unmarshal(w.Body.Bytes(), &review); err != nil || w.Code != http.StatusOK || !review.Ready || review.Diff == nil {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if called != 1 {
		t.Fatal("review applied a draft")
	}
	draft.FolderIdentity = "replaced-directory"
	w = request("POST", "/api/library/draft-state", draft, true)
	var state registry.DraftState
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || w.Code != http.StatusOK || state.CurrentDiff != nil {
		t.Fatal("replaced folder was read", w.Code, w.Body.String(), err)
	}
	if w := request("DELETE", "/api/library/drafts/"+draft.ID, nil, true); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/api/library/drafts", nil, true)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), draft.ID) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestLibraryPeerRoutesAreBounded(t *testing.T) {
	for _, test := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/library/packages", true},
		{"POST", "/api/library/agentpacks", true},
		{"PATCH", "/api/library/agentpacks/pack-id", true},
		{"GET", "/api/library/agentpacks/pack-id/revisions/rev/restore-preview", true},
		{"POST", "/api/library/agentpacks/pack-id/revisions/rev/restore", true},
		{"POST", "/api/library/packages", false},
		{"DELETE", "/api/library/agentpacks/pack-id", true},
		{"POST", "/api/library/draft-state", true},
		{"POST", "/api/library/drafts", false},
		{"GET", "/api/library/agentpacks/../revisions/rev/restore-preview", false},
		{"GET", "/api/library/agentpacks/pack-id/revisions/rev/restore", false},
		{"POST", "/api/library/agentpacks/pack-id/revisions/rev/restore/extra", false},
		{"GET", "/api/library/bindings", false},
	} {
		if got := peerRoute(test.method, test.path); got != test.want {
			t.Errorf("%s %s allowed=%v", test.method, test.path, got)
		}
	}
}
