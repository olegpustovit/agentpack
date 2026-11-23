package network

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/olegpustovit/agentpack/internal/state"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPendingJoinClosesClaimWindowAndInheritsAccounts(t *testing.T) {
	home := t.TempDir()
	auth, err := testUnclaimedAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := PrepareMeshJoin(t.Context(), auth.store, "source"); err != nil {
		t.Fatal(err)
	}
	if err := PrepareMeshJoin(t.Context(), auth.store, "source"); err != nil {
		t.Fatal("same join is not retryable", err)
	}
	if err := PrepareMeshJoin(t.Context(), auth.store, "other"); err == nil {
		t.Fatal("changed pending join without a reset")
	}
	if won, err := claimOwner(t.Context(), auth.store, "scanner@example.com", []byte(dummyPasswordHash)); err != nil || won {
		t.Fatalf("pending join was claimable: %v %v", won, err)
	}
	origin, _ := url.Parse("http://joining.local")
	joining := &Gateway{auth: auth, store: auth.store, origin: origin, local: nodeRecord{Secret: strings.Repeat("j", 64)}, options: GatewayOptions{Home: home, Library: LibraryOptions{Store: replicaStore(t)}}}
	if err := joining.options.Library.Store.EnrollPeer("source"); err != nil {
		t.Fatal(err)
	}
	handler := joining.Handler()
	request := func(path string, peer bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", origin.String()+path, nil)
		if peer {
			r.Header.Set("Authorization", "Bearer "+joining.local.Secret)
			r = r.WithContext(withActor(context.WithValue(r.Context(), replicaPeerKey{}, "source"), Actor{Kind: "peer", StableNodeID: "source"}))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("/api/status", false)
	var status AccountStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || !status.Joining || !status.Claimed {
		t.Fatalf("pending status: %+v %v", status, err)
	}
	if w := request("/api/sessions", true); w.Code != 403 {
		t.Fatalf("joining peer served application data: %d", w.Code)
	}
	if w := request("/api/config", false); w.Code != 200 {
		t.Fatalf("joining browser could not load its UI configuration: %d", w.Code)
	}
	joining.events.topics = []eventTopic{}
	joining.events.nodes = func(context.Context) ([]TailnetNode, error) { return nil, nil }
	events := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = origin.Host
		r.Header.Set("Authorization", "Bearer "+joining.local.Secret)
		r = r.WithContext(withActor(context.WithValue(r.Context(), replicaPeerKey{}, "source"), Actor{Kind: "peer", StableNodeID: "source"}))
		handler.ServeHTTP(w, r)
	}))
	defer events.Close()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(events.URL + "/api/sync/events")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		response.Body.Close()
		t.Fatalf("joining peer could not receive sync events: %d %s", response.StatusCode, body)
	}
	response.Body.Close()
	if w := request("/api/sync/changes?schema=0", true); w.Code != 409 {
		t.Fatalf("verified sync did not reach schema check: %d %s", w.Code, w.Body.String())
	}
	source, _, _ := syncGateway(t, "source")
	if err := source.options.Library.Store.EnrollPeer("joining"); err != nil {
		t.Fatal(err)
	}
	source.origin, _ = url.Parse("http://source.local")
	source.local.Secret = strings.Repeat("s", 64)
	sourceHandler := source.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+source.local.Secret)
		r = r.WithContext(withActor(context.WithValue(r.Context(), replicaPeerKey{}, "joining"), Actor{Kind: "peer", StableNodeID: "joining"}))
		sourceHandler.ServeHTTP(w, r)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	if _, _, err := pullState(t.Context(), auth.store, nil, server.Client(), endpoint, nil); err != nil {
		t.Fatal(err)
	}
	w = request("/api/status", false)
	// Decode into a fresh value: an omitted false field must not preserve the earlier true value.
	status = AccountStatus{}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || status.Joining || !status.Claimed {
		t.Fatalf("inherited status: %+v %v", status, err)
	}
	users, err := ListUsers(t.Context(), auth.store)
	if err != nil || len(users) != 1 || users[0].Email != testBrowserEmail {
		t.Fatalf("inherited accounts: %+v %v", users, err)
	}
	if won, err := claimOwner(t.Context(), auth.store, "second-owner@example.com", []byte(dummyPasswordHash)); err != nil || won {
		t.Fatalf("inherited mesh reopened claim: %v %v", won, err)
	}
}

func TestLocalAccountBindingPreservesOwnershipAndRevocation(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	if _, err := g.store.DB.Exec("INSERT INTO users(email,role) VALUES ('member@example.com','member')"); err != nil {
		t.Fatal(err)
	}
	actor, err := g.localActor(t.Context())
	if err != nil || actor.User != testBrowserEmail || !actor.Account {
		t.Fatalf("default owner mapping: %+v %v", actor, err)
	}
	owner := browserActor(testBrowserEmail)
	if !actor.canWriteSession(SessionInfo{StartedBy: &owner}) {
		t.Fatal("local owner cannot continue its browser chat")
	}
	legacy := LocalActor("local")
	if !actor.canWriteSession(SessionInfo{StartedBy: &legacy}) {
		t.Fatal("mapping lost this OS user's native history")
	}
	if err := BindLocalAccount(t.Context(), g.store, " MEMBER@example.com "); err != nil {
		t.Fatal(err)
	}
	actor, err = g.localActor(t.Context())
	if err != nil || actor.User != "member@example.com" {
		t.Fatalf("bound account: %+v %v", actor, err)
	}
	if actor.canWriteSession(SessionInfo{StartedBy: &owner}) {
		t.Fatal("bound member wrote owner's chat")
	}
	member := browserActor("member@example.com")
	if !actor.canWriteSession(SessionInfo{StartedBy: &member}) {
		t.Fatal("bound member cannot write its own chat")
	}
	if err := BindLocalAccount(t.Context(), g.store, "missing@example.com"); err == nil {
		t.Fatal("missing account bound")
	}
	if err := DisableUser(t.Context(), g.store, "member@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.localActor(t.Context()); err == nil {
		t.Fatal("disabled binding silently reverted to owner")
	}
	if err := BindLocalAccount(t.Context(), g.store, "member@example.com"); err == nil {
		t.Fatal("disabled account bound")
	}
	if err := BindLocalAccount(t.Context(), g.store, testBrowserEmail); err != nil {
		t.Fatal(err)
	}
}

func TestJoinPreparationAndOwnerClaimCannotBothWin(t *testing.T) {
	home := t.TempDir()
	a, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	start := make(chan struct{})
	prepared := make(chan error, 1)
	claimed := make(chan bool, 1)
	go func() { <-start; prepared <- PrepareMeshJoin(t.Context(), a, "source") }()
	go func() {
		<-start
		won, err := claimOwner(t.Context(), b, "owner@example.com", []byte(dummyPasswordHash))
		if err != nil {
			t.Error(err)
		}
		claimed <- won
	}()
	close(start)
	prepareErr, won := <-prepared, <-claimed
	if (prepareErr == nil) == won {
		t.Fatalf("join and claim outcomes overlap: prepare=%v claim=%v", prepareErr, won)
	}
	var peer string
	var count int
	if err := a.Reader.QueryRow("SELECT join_peer FROM local_node WHERE id=1").Scan(&peer); err != nil {
		t.Fatal(err)
	}
	if err := a.Reader.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if peer != "" && count != 0 || peer == "" && count != 1 {
		t.Fatalf("inconsistent race outcome: peer=%q users=%d", peer, count)
	}
}

func TestMeshOwnerResetCannotRevivePeerLoginsForRecreatedEmail(t *testing.T) {
	sourceHome, peerHome := t.TempDir(), t.TempDir()
	source, err := testGatewayAuth(t, sourceHome)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := testGatewayAuth(t, peerHome)
	if err != nil {
		t.Fatal(err)
	}
	const token = "existing-peer-browser"
	testBrowserSession(t, peer, testBrowserEmail, token)
	// A fast peer clock must not let an old session pass the new account's time cutoff.
	if _, err := peer.store.DB.Exec("UPDATE auth_sessions SET created_at=?", time.Now().Add(365*24*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://peer.local/api/status", nil)
	request.AddCookie(&http.Cookie{Name: "agentpack_session", Value: token})
	if !peer.authenticated(request) {
		t.Fatal("fixture login was not initially valid")
	}
	grant := oauthRecord{Email: testBrowserEmail, SessionEpoch: 0}
	checkGrant := func() error {
		tx, err := peer.store.Reader.BeginTx(t.Context(), nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		return oauthAccountValid(tx, grant)
	}
	if err := checkGrant(); err != nil {
		t.Fatal("fixture OAuth grant was not valid", err)
	}
	if err := ResetOwner(t.Context(), source.store, sourceHome); err != nil {
		t.Fatal(err)
	}
	if won, err := claimOwner(t.Context(), source.store, testBrowserEmail, []byte(dummyPasswordHash)); err != nil || !won {
		t.Fatalf("reclaim: %v %v", won, err)
	}
	changes, next, err := source.store.Changes(t.Context(), 0, nil, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	site, err := source.store.SiteID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.store.ApplyPage(t.Context(), changes, site, next); err != nil {
		t.Fatal(err)
	}
	if peer.authenticated(request) {
		t.Fatal("reset/reclaim revived the old peer browser session")
	}
	if err := checkGrant(); !errors.Is(err, errOAuthGrant) {
		t.Fatalf("reset/reclaim revived the old peer OAuth grant: %v", err)
	}
	var epoch int64
	if err := peer.store.Reader.QueryRow("SELECT sessions_valid_after FROM users WHERE email=?", testBrowserEmail).Scan(&epoch); err != nil || epoch == 0 {
		t.Fatalf("reclaim lost mesh revocation epoch: %d %v", epoch, err)
	}
	response := httptest.NewRecorder()
	peer.issueSession(response, httptest.NewRequest("POST", "http://peer.local/api/login", nil), User{Email: testBrowserEmail, SessionsValidAfter: epoch})
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("new login refused: %d %s", response.Code, response.Body.String())
	}
	fresh := httptest.NewRequest("GET", "http://peer.local/api/status", nil)
	fresh.AddCookie(cookies[0])
	if !peer.authenticated(fresh) {
		t.Fatal("new login after reclaim was refused")
	}
}
