package network

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"

	"tailscale.com/client/local"

	"github.com/olegpustovit/agentpack/internal/paths"
)

func identityGateway(t *testing.T, home string, start func(context.Context, session.SessionInfo, string) (string, error)) (*Gateway, *url.URL) {
	t.Helper()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	return &Gateway{store: auth.store, auth: auth, origin: origin, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home, Version: "test", Start: start}}, origin
}

// seedFolder registers a fresh directory as a recent folder for the gateway's folder check.
func seedFolder(t *testing.T, home string) string {
	t.Helper()
	if err := paths.EnsurePrivateDirectory(filepath.Join(home, "network")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	seedTestState(t, home)
	if err := remember(home, session.SessionInfo{Directory: dir, Harness: "claude", Mode: "default"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestIdentityWithoutTailscaleReportsSignedInEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer server.Close()
	tailscaleLocal = local.Client{UseSocketOnly: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	t.Cleanup(func() { tailscaleLocal = local.Client{} })
	home := t.TempDir()
	g, origin := identityGateway(t, home, nil)
	r := httptest.NewRequest("GET", origin.String()+"/api/identity", nil)
	testBrowserSession(t, g.auth, testBrowserEmail, "identity")
	r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: "identity"})
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	var got identityResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if got.Auth != testBrowserEmail || got.TailscaleUser != "" || got.Node.Version != "test" {
		t.Fatalf("identity without tailscale: %+v", got)
	}
}

func TestBrowserActorIgnoresForgedActorHeader(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	var started session.SessionInfo
	g, origin := identityGateway(t, home, func(_ context.Context, info session.SessionInfo, _ string) (string, error) {
		started = info
		return "session-test", nil
	})
	r := httptest.NewRequest("POST", origin.String()+"/api/sessions", bytes.NewBufferString(`{"directory":`+strconv.Quote(dir)+`,"harness":"claude","mode":"default"}`))
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	r.Header.Set("X-Agentpack-Actor", `{"kind":"peer","user":"mallory@example.com"}`)
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	// The authenticated local transport maps to the owner; caller headers have no authority.
	if started.StartedBy == nil || started.StartedBy.Kind != "local" || started.StartedBy.User != testBrowserEmail || !started.StartedBy.Account {
		t.Fatalf("forged header changed local account actor: %+v", started.StartedBy)
	}
}

func TestStartSessionRecordsMCPActor(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	var started session.SessionInfo
	g, _ := identityGateway(t, home, func(_ context.Context, info session.SessionInfo, _ string) (string, error) {
		started = info
		return "session-test", nil
	})
	info, err := g.startSession(context.Background(), session.LocalActor("mcp"), session.SessionInfo{Directory: dir, Harness: "claude", Mode: "default"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "session-test" || started.StartedBy == nil || started.StartedBy.Kind != "mcp" {
		t.Fatalf("mcp actor: id=%q started=%+v", info.ID, started.StartedBy)
	}
}

func TestContextActorBecomesStartedBy(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	var started session.SessionInfo
	g, origin := identityGateway(t, home, func(_ context.Context, info session.SessionInfo, _ string) (string, error) {
		started = info
		return "session-test", nil
	})
	r := httptest.NewRequest("POST", origin.String()+"/api/sessions", bytes.NewBufferString(`{"directory":`+strconv.Quote(dir)+`,"harness":"claude","mode":"default"}`))
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	r = r.WithContext(session.WithActor(r.Context(), session.Actor{Kind: "peer", User: "alice@example.com", Node: "laptop", StableNodeID: "stable-laptop"}))
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if started.StartedBy == nil || *started.StartedBy != (session.Actor{Kind: "peer", User: "alice@example.com", Node: "laptop", StableNodeID: "stable-laptop"}) {
		t.Fatalf("context actor: %+v", started.StartedBy)
	}
	if started.StartedBy.StableNodeID == "" {
		t.Fatal("private worker launch lost the stable peer identity")
	}
	var public session.SessionInfo
	if err := json.Unmarshal(w.Body.Bytes(), &public); err != nil {
		t.Fatal(err)
	}
	if public.StartedBy == nil || public.StartedBy.StableNodeID != "" {
		t.Fatalf("public session leaked stable peer identity: %+v", public.StartedBy)
	}
	if strings.Contains(w.Body.String(), `"stable_node_id"`) {
		t.Fatalf("public session JSON included stable_node_id: %s", w.Body.String())
	}
}
func TestPeerSessionOwnerRequiresStableNodeIdentity(t *testing.T) {
	owner := session.Actor{Kind: "peer", User: "alice@example.com", Node: "old-name", StableNodeID: "stable-owner"}
	info := session.SessionInfo{StartedBy: &owner}
	if !(session.Actor{Kind: "peer", User: owner.User, Node: "new-name", StableNodeID: owner.StableNodeID}).CanWriteSession(info) {
		t.Fatal("same stable peer identity lost write access after a name change")
	}
	for _, actor := range []session.Actor{
		{Kind: "peer", User: owner.User, Node: owner.Node, StableNodeID: "stable-other"},
		{Kind: "peer", User: owner.User, Node: owner.Node},
		{Kind: "peer", User: "mallory@example.com", Node: owner.Node, StableNodeID: owner.StableNodeID},
	} {
		if actor.CanWriteSession(info) {
			t.Fatalf("non-owner peer gained write access: %+v", actor)
		}
	}
	legacyOwner := session.Actor{Kind: "peer", User: owner.User, Node: owner.Node}
	if (session.Actor{Kind: "peer", User: owner.User, Node: owner.Node, StableNodeID: "stable-other"}).CanWriteSession(session.SessionInfo{StartedBy: &legacyOwner}) {
		t.Fatal("a reused peer name matched an owner without a stable ID")
	}
	localOwner := session.LocalActor("cli")
	localBrowser := session.BrowserActor(testBrowserEmail)
	if localBrowser.CanWriteSession(session.SessionInfo{StartedBy: &localOwner}) ||
		localOwner.CanWriteSession(session.SessionInfo{StartedBy: &localBrowser}) {
		t.Fatal("host alone granted access across account identities")
	}
	localPeer := session.Actor{Kind: "peer", User: localOwner.User, Node: localOwner.Node, StableNodeID: "stable-peer"}
	if localPeer.CanWriteSession(session.SessionInfo{StartedBy: &localOwner}) {
		t.Fatal("peer identity crossed into a local owner session")
	}
	peerOwner := session.Actor{Kind: "peer", User: localOwner.User, Node: localOwner.Node, StableNodeID: "stable-peer"}
	if localBrowser.CanWriteSession(session.SessionInfo{StartedBy: &peerOwner}) {
		t.Fatal("local identity crossed into a peer owner session")
	}
}

func TestACPSessionWritesRequireOwner(t *testing.T) {
	home := t.TempDir()
	g, origin := identityGateway(t, home, nil)
	requests := make(chan string, 32)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		if r.URL.Path == "/state" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"running":true,"rows":24,"columns":80}`)
			return
		}
		if r.URL.Path == "/stop" || r.URL.Path == "/restart" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	sessionID := "session-answer"
	owner := session.BrowserActor(testBrowserEmail)
	record := WorkerRecord{
		ID: sessionID, Address: strings.TrimPrefix(upstream.URL, "http://"), Secret: strings.Repeat("s", 64),
		Session: session.SessionInfo{ID: sessionID, Transport: "acp", StartedBy: &owner},
	}
	if err := session.UpsertSession(t.Context(), g.store.DB, record.Session); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(home, "network", "sessions", sessionID)
	if err := paths.EnsurePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	request := func(method, path, body string, actor *session.Actor) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, origin.String()+"/api/sessions/"+sessionID+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		if actor != nil {
			r = r.WithContext(session.WithActor(r.Context(), *actor))
		}
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w
	}
	for _, account := range []struct {
		email, token string
		status       int
	}{{testBrowserEmail, "browser-owner", 204}, {"other@example.com", "browser-other", 403}} {
		testBrowserSession(t, g.auth, account.email, account.token)
		r := httptest.NewRequest(http.MethodPost, origin.String()+"/api/sessions/"+sessionID+"/conversation/prompt", strings.NewReader(`{}`))
		r.Header.Set("Origin", origin.String())
		r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: account.token})
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != account.status {
			t.Fatalf("browser account %s: %d %s", account.email, w.Code, w.Body.String())
		}
		if account.status == 204 {
			if got := <-requests; got != "/conversation/prompt" {
				t.Fatal(got)
			}
		} else if len(requests) != 0 {
			t.Fatal("another browser account reached the worker")
		}
	}

	readOnly := session.Actor{Kind: "peer", User: owner.User, Node: "another-node"}
	mutations := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/conversation/prompt", `{}`},
		{http.MethodPost, "/conversation/prompt/images", `{"text":"x","images":[]}`},
		{http.MethodPost, "/conversation/permission", `{}`},
		{http.MethodPost, "/conversation/cancel", `{}`},
		{http.MethodPost, "/conversation/elicitation", `{}`},
		{http.MethodPost, "/conversation/config", `{}`},
		{http.MethodPost, "/conversation/blobs", `{}`},
		{http.MethodPost, "/stop", `{}`},
		{http.MethodPost, "/restart", `{"operation_id":"operation-123456"}`},
		{http.MethodPatch, "", `{"name":"Nope"}`},
		{http.MethodPost, "/input", "no"},
	}
	for _, mutation := range mutations {
		if got := request(mutation.method, mutation.path, mutation.body, &readOnly).Code; got != http.StatusForbidden {
			t.Fatalf("%s %s from another node: status %d", mutation.method, mutation.path, got)
		}
		select {
		case got := <-requests:
			t.Fatalf("%s %s from another node reached worker: %s", mutation.method, mutation.path, got)
		default:
		}
	}

	if got := request(http.MethodGet, "/conversation", "", &readOnly).Code; got != http.StatusOK {
		t.Fatalf("conversation read from another node: status %d", got)
	}
	select {
	case got := <-requests:
		t.Fatalf("snapshot reached worker at %q", got)
	default:
	}

	localCLI := session.Actor{Kind: "cli", User: owner.User, Node: owner.Node}
	for _, path := range []string{"prompt", "permission", "cancel", "elicitation", "config", "blobs"} {
		if got := request(http.MethodPost, "/conversation/"+path, `{}`, &localCLI).Code; got != http.StatusNoContent {
			t.Fatalf("owner write %s: status %d", path, got)
		}
		if got := <-requests; got != "/conversation/"+path {
			t.Fatalf("owner write %s reached worker path %q", path, got)
		}
	}
	if got := request(http.MethodPost, "/stop", `{}`, &localCLI).Code; got != http.StatusOK {
		t.Fatalf("owner stop: status %d", got)
	}
	if got := <-requests; got != "/stop" {
		t.Fatalf("owner stop reached worker path %q", got)
	}
	if got := request(http.MethodPost, "/restart", `{"operation_id":"operation-123456"}`, &localCLI).Code; got != http.StatusOK {
		t.Fatalf("owner restart: status %d", got)
	}
	if got := <-requests; got != "/restart" {
		t.Fatalf("owner restart reached worker path %q", got)
	}
	if got := request(http.MethodPatch, "", `{"name":"Owner"}`, &localCLI).Code; got != http.StatusOK {
		t.Fatalf("owner rename: status %d body=%s", got, "not shown")
	}
	if got := <-requests; got != "/state" {
		t.Fatalf("owner rename reached worker path %q", got)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if err := session.EndSession(t.Context(), g.store.DB, sessionID, session.TerminalState{}); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodPatch, "", `{"name":"Nope"}`, &readOnly).Code; got != http.StatusForbidden {
		t.Fatalf("stopped session lost ownership: %d", got)
	}
	if got := request(http.MethodPatch, "", `{"name":"Finished"}`, &localCLI).Code; got != http.StatusOK {
		t.Fatalf("owner cannot rename stopped session: %d", got)
	}
}
func TestPeerACPResumeRequiresRecordedOwner(t *testing.T) {
	home := t.TempDir()
	directory := seedFolder(t, home)
	folderID, err := FolderIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	owner := session.Actor{Kind: "peer", User: "alice@example.com", Node: "old-name", StableNodeID: "stable-owner"}
	if err := testRememberACPConversationOwner(t, home, session.SessionInfo{
		Directory: directory, FolderID: folderID, Harness: "claude", Mode: "default",
		Transport: "acp", AgentSessionID: "prior-1", StartedBy: &owner,
	}, ""); err != nil {
		t.Fatal(err)
	}
	var started []session.SessionInfo
	g, origin := identityGateway(t, home, func(_ context.Context, info session.SessionInfo, _ string) (string, error) {
		started = append(started, info)
		return "session-new", nil
	})
	resume := func(agentSessionID string, actor session.Actor) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]string{
			"directory": directory, "harness": "claude", "mode": "default",
			"transport": "acp", "agent_session_id": agentSessionID,
		})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, origin.String()+"/api/sessions", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		r = r.WithContext(session.WithActor(r.Context(), actor))
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		g.starting.Wait()
		return w
	}

	otherPeer := session.Actor{Kind: "peer", User: owner.User, Node: owner.Node, StableNodeID: "stable-other"}
	if got := resume("prior-1", otherPeer).Code; got != http.StatusForbidden || len(started) != 0 {
		t.Fatalf("non-owner resume status=%d starts=%d", got, len(started))
	}
	renamedOwner := session.Actor{Kind: "peer", User: owner.User, Node: "new-name", StableNodeID: owner.StableNodeID}
	if got := resume("prior-1", renamedOwner).Code; got != http.StatusOK || len(started) != 1 {
		t.Fatalf("owner resume status=%d starts=%d", got, len(started))
	}
	if started[0].StartedBy == nil || *started[0].StartedBy != renamedOwner {
		t.Fatalf("resumed owner = %+v, want %+v", started[0].StartedBy, renamedOwner)
	}

	store := testSessionStore(t, home)
	if _, err := store.DB.Exec("UPDATE acp_session_owners SET started_by='{' WHERE folder_id=?", folderID); err != nil {
		t.Fatal(err)
	}
	if got := resume("prior-1", renamedOwner).Code; got != http.StatusInternalServerError || len(started) != 1 {
		t.Fatalf("corrupt owner record status=%d starts=%d", got, len(started))
	}
	if got := resume("unowned", otherPeer).Code; got != http.StatusForbidden || len(started) != 1 {
		t.Fatalf("unowned peer resume status=%d starts=%d", got, len(started))
	}
	if got := resume("external-history", session.BrowserActor(testBrowserEmail)).Code; got != http.StatusOK || len(started) != 2 {
		t.Fatalf("local unowned resume status=%d starts=%d", got, len(started))
	}
}

func TestPeerACPForkRequiresSourceOwnerAndRecordsForkedSession(t *testing.T) {
	home := t.TempDir()
	directory := seedFolder(t, home)
	folderID, err := FolderIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	owner := session.Actor{Kind: "peer", User: "alice@example.com", Node: "old-name", StableNodeID: "stable-owner"}
	sourceSession := session.SessionInfo{
		ID: "session-source", Directory: directory, FolderID: folderID, Harness: "claude",
		Mode: "default", Transport: "acp", AgentSessionID: "source-session", StartedBy: &owner,
	}
	if err := testRememberACPConversationOwner(t, home, sourceSession, ""); err != nil {
		t.Fatal(err)
	}
	sourceRecordDir := filepath.Join(home, "network", "sessions", sourceSession.ID)
	if err := paths.EnsurePrivateDirectory(sourceRecordDir); err != nil {
		t.Fatal(err)
	}
	store := testSessionStore(t, home)
	saveSourceRecord := func(info session.SessionInfo) {
		t.Helper()
		if err := session.UpsertSession(t.Context(), store.DB, info); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(WorkerRecord{
			Session: info, ID: sourceSession.ID, Address: "127.0.0.1:7420", Secret: strings.Repeat("s", 64),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourceRecordDir, "worker.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	saveSourceRecord(sourceSession)
	var started session.SessionInfo
	var receivedForkID string
	starts := 0
	g, origin := identityGateway(t, home, func(_ context.Context, info session.SessionInfo, forkSessionID string) (string, error) {
		starts++
		started, receivedForkID = info, forkSessionID
		return "session-forked", nil
	})
	request := func(actor session.Actor, requestID ...string) *httptest.ResponseRecorder {
		t.Helper()
		body := ""
		if len(requestID) != 0 {
			body = string(mustRawJSON(map[string]string{"id": requestID[0]}))
		}
		r := httptest.NewRequest(http.MethodPost, origin.String()+"/api/sessions/"+sourceSession.ID+"/fork", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		r = r.WithContext(session.WithActor(r.Context(), actor))
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		g.starting.Wait()
		return w
	}
	renamedOwner := session.Actor{Kind: "peer", User: owner.User, Node: "new-name", StableNodeID: owner.StableNodeID}
	for _, test := range []struct {
		name      string
		transport string
		sessionID string
	}{
		{name: "non-ACP", transport: "terminal", sessionID: "source-session"},
		{name: "missing native ID", transport: "acp"},
	} {
		invalid := sourceSession
		invalid.Transport, invalid.AgentSessionID = test.transport, test.sessionID
		saveSourceRecord(invalid)
		if got := request(renamedOwner).Code; got != http.StatusBadRequest || starts != 0 {
			t.Fatalf("%s source status=%d starts=%d", test.name, got, starts)
		}
	}
	saveSourceRecord(sourceSession)
	otherPeer := session.Actor{Kind: "peer", User: owner.User, Node: owner.Node, StableNodeID: "stable-other"}
	if got := request(otherPeer).Code; got != http.StatusForbidden || starts != 0 {
		t.Fatalf("non-owner fork status=%d starts=%d", got, starts)
	}
	response := request(renamedOwner)
	if response.Code != http.StatusOK || starts != 1 {
		t.Fatalf("owner fork status=%d starts=%d body=%s", response.Code, starts, response.Body.String())
	}
	if receivedForkID != "source-session" || started.AgentSessionID != "" || started.StartedBy == nil || *started.StartedBy != renamedOwner {
		t.Fatalf("private fork launch: forkID=%q session=%+v", receivedForkID, started)
	}
	if strings.Contains(response.Body.String(), "source-session") {
		t.Fatalf("source session ID leaked into fork response: %s", response.Body.String())
	}
	forked := started
	forked.AgentSessionID = "forked-session"
	if err := testRememberACPConversationOwner(t, home, forked, ""); err != nil {
		t.Fatal(err)
	}
	gotOwner, found, err := readACPConversationOwner(t.Context(), testSessionStore(t, home), folderID, "claude", "forked-session")
	if err != nil || !found || gotOwner != renamedOwner {
		t.Fatalf("forked native session owner=%+v found=%t err=%v", gotOwner, found, err)
	}
	gotOwner, found, err = readACPConversationOwner(t.Context(), testSessionStore(t, home), folderID, "claude", "source-session")
	if err != nil || !found || gotOwner != owner {
		t.Fatalf("source native session owner changed: owner=%+v found=%t err=%v", gotOwner, found, err)
	}

	const id = "session-0123456789abcdef0123456789abcdef"
	first, retry := request(renamedOwner, id), request(renamedOwner, id)
	if first.Code != http.StatusOK || retry.Code != http.StatusOK || starts != 2 || started.ID != id {
		t.Fatalf("fork retry: first=%d retry=%d starts=%d", first.Code, retry.Code, starts)
	}
	if response := request(otherPeer, id); response.Code != http.StatusForbidden || starts != 2 {
		t.Fatalf("non-owner fork replay: status=%d starts=%d", response.Code, starts)
	}
}

func TestStartWorkerStampsStartedAtOnlyWhenZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub worker")
	}
	stub := filepath.Join(t.TempDir(), "stub-agentpack")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat > \"$AGENTPACK_TEST_CAPTURE\"\nprintf '%s' \"$AGENTPACK_TRACE\" > \"$AGENTPACK_TEST_TRACE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "launch.json")
	t.Setenv("AGENTPACK_TEST_CAPTURE", capture)
	traceCapture := filepath.Join(t.TempDir(), "trace")
	t.Setenv("AGENTPACK_TEST_TRACE", traceCapture)
	const traceID = "0123456789abcdef0123456789abcdef"
	ctx := obs.WithTrace(context.Background(), "00-"+traceID+"-0123456789abcdef-01")
	captured := func(t *testing.T, launch WorkerLaunch) WorkerLaunch {
		t.Helper()
		// The stub never registers a worker, so StartWorker returns an error after capturing stdin.
		_, _ = StartWorker(ctx, t.TempDir(), stub, launch)
		trace, err := os.ReadFile(traceCapture)
		if err != nil || string(trace) != traceID {
			t.Fatalf("child trace=%q err=%v", trace, err)
		}
		body, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal(err)
		}
		var got WorkerLaunch
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := captured(t, WorkerLaunch{Session: session.SessionInfo{ID: "a"}}); got.Session.StartedAt.IsZero() || time.Since(got.Session.StartedAt) > time.Minute {
		t.Fatalf("zero StartedAt not stamped: %v", got.Session.StartedAt)
	}
	fixed := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	if got := captured(t, WorkerLaunch{Session: session.SessionInfo{ID: "b", StartedAt: fixed}}); !got.Session.StartedAt.Equal(fixed) {
		t.Fatalf("preset StartedAt overwritten: %v", got.Session.StartedAt)
	}
	fork := captured(t, WorkerLaunch{Session: session.SessionInfo{ID: "c"}, ForkSessionID: "source-session"})
	if fork.ForkSessionID != "source-session" || fork.Session.AgentSessionID != "" {
		t.Fatalf("fork source was not kept private in worker launch: %+v", fork)
	}
}

func TestRunWorkerPersistsCLIStartedBy(t *testing.T) {
	home := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	actor := session.LocalActor("cli")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := RunWorker(ctx, home, "test", WorkerLaunch{Session: session.SessionInfo{StartedBy: &actor, StartedAt: time.Now()}, Program: executable, Directory: home, Arguments: []string{"-test.run=^TestTerminalChild$"}, Environment: append(os.Environ(), "AGENTPACK_TEST_TERMINAL_CHILD=1"), Rows: 24, Columns: 80}, func(id string) error { ready <- id; return nil })
		done <- err
	}()
	var id string
	select {
	case id = <-ready:
	case err := <-done:
		t.Fatalf("worker startup failed: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("worker startup timeout")
	}
	store := testSessionStore(t, home)
	info, err := session.StoredSession(t.Context(), store.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if info.StartedBy == nil || info.StartedBy.Kind != "cli" || info.StartedBy.User == "" || info.StartedAt.IsZero() {
		t.Fatalf("worker record lost actor: %+v", info)
	}
	cancel()
	<-done
}

func TestForwardedAccountRequiresExplicitMeshAndOwnsOnlyItsChats(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	library := replicaStore(t)
	g.options.Library.Store = library
	peer := session.Actor{Kind: "peer", User: "tailscale@example.com", StableNodeID: "remote"}
	if err := library.EnrollOwnMachine("remote"); err != nil {
		t.Fatal(err)
	}
	ignored, err := g.forwardedAccount(t.Context(), peer, testBrowserEmail)
	if err != nil || ignored != peer {
		t.Fatalf("library-only peer asserted an account: %+v %v", ignored, err)
	}
	if err := library.EnrollPeer("remote"); err != nil {
		t.Fatal(err)
	}
	actor, err := g.forwardedAccount(t.Context(), peer, testBrowserEmail)
	if err != nil || !actor.Account || actor.User != testBrowserEmail {
		t.Fatalf("mesh account refused: %+v %v", actor, err)
	}
	own := session.BrowserActor(testBrowserEmail)
	if !actor.CanWriteSession(session.SessionInfo{StartedBy: &own}) {
		t.Fatal("same account could not continue its chat through another node")
	}
	other := session.BrowserActor("other@example.com")
	if actor.CanWriteSession(session.SessionInfo{StartedBy: &other}) {
		t.Fatal("peer account wrote someone else's chat")
	}
	body, err := json.Marshal(actor)
	if err != nil {
		t.Fatal(err)
	}
	var saved session.Actor
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if !own.CanWriteSession(session.SessionInfo{StartedBy: &saved}) {
		t.Fatal("account ownership did not survive session persistence")
	}
	if _, err := g.forwardedAccount(t.Context(), peer, "missing@example.com"); err == nil {
		t.Fatal("nonexistent account accepted")
	}
	if _, err := g.store.DB.Exec("UPDATE users SET disabled=1 WHERE email=?", testBrowserEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := g.forwardedAccount(t.Context(), peer, testBrowserEmail); err == nil {
		t.Fatal("disabled account accepted")
	}
}

func TestProxyUsesAuthenticatedAccountInsteadOfCallerHeader(t *testing.T) {
	for _, account := range []bool{false, true} {
		got := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got <- r.Header.Get("X-Agentpack-User")
			w.WriteHeader(204)
		}))
		target, _ := url.Parse(server.URL)
		g, _ := identityGateway(t, t.TempDir(), nil)
		r := httptest.NewRequest("POST", "http://localhost/api/sessions/session-example/stop", nil)
		r.Header.Set("X-Agentpack-User", "forged@example.com")
		if account {
			r = r.WithContext(session.WithActor(r.Context(), session.BrowserActor(testBrowserEmail)))
		}
		g.proxyTailnetTo(httptest.NewRecorder(), r, "remote", target)
		email := <-got
		server.Close()
		want := ""
		if account {
			want = testBrowserEmail
		}
		if email != want {
			t.Fatalf("forwarded account=%q want %q", email, want)
		}
	}
}

func TestGatewayCSPAllowsOnlySelfAndBlobImages(t *testing.T) {
	g, origin := identityGateway(t, t.TempDir(), nil)
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, httptest.NewRequest("GET", origin.String()+"/api/status", nil))
	// Pasted images are measured from a blob: URL before they are sent.
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' blob:;") || !strings.Contains(csp, "default-src 'self';") {
		t.Fatalf("CSP = %q", csp)
	}
}

func TestTailnetWhoisIsCachedButFailuresAreNot(t *testing.T) {
	var calls atomic.Int32
	var failing atomic.Bool
	failing.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if failing.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		_, _ = w.Write([]byte(`{"Node":{"StableID":"n1","ComputedName":"peer"},"UserProfile":{"LoginName":"owner@example.com"}}`))
	}))
	defer server.Close()
	tailscaleLocal = local.Client{UseSocketOnly: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	t.Cleanup(func() { tailscaleLocal = local.Client{} })
	g := &Gateway{}
	address := netip.MustParseAddr("100.64.0.1")
	if _, err := g.cachedTailnetWhois(t.Context(), address); err == nil {
		t.Fatal("failure was not reported")
	}
	failing.Store(false)
	for range 3 {
		identity, err := g.cachedTailnetWhois(t.Context(), address)
		if err != nil || identity.Node.StableID != "n1" {
			t.Fatalf("identity %+v err %v", identity, err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("Tailscale asked %d times, want one failure and one cached success", got)
	}
}

func TestClientChatIDIsAnInsertOnlyClaim(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	starts := make(chan session.SessionInfo, 2)
	g, origin := identityGateway(t, home, func(_ context.Context, info session.SessionInfo, _ string) (string, error) {
		starts <- info
		return info.ID, nil
	})
	const id = "session-b7148a740d2c495991c792576427836d"
	body := chatStartRequest{ID: id, Directory: dir, Harness: "claude", Mode: "default", Transport: "acp"}
	call := func(input chatStartRequest, user string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, origin.String()+"/api/sessions", bytes.NewReader(mustRawJSON(input)))
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		r = r.WithContext(session.WithActor(r.Context(), session.BrowserActor(user)))
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("start status = %d, want %d: %s", w.Code, want, w.Body.String())
		}
		if want == http.StatusOK {
			var info session.SessionInfo
			if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || info.ID != input.ID || info.ConversationState != "starting" {
				t.Fatalf("invalid claimed session: %+v (%v)", info, err)
			}
		}
	}
	call(body, testBrowserEmail, http.StatusOK)
	call(body, testBrowserEmail, http.StatusOK)
	call(body, "different@example.com", http.StatusConflict)
	changed := body
	changed.Harness = "codex"
	call(changed, testBrowserEmail, http.StatusConflict)
	unclaimed := body
	unclaimed.ID = "session-f7148a740d2c495991c792576427836d"
	if err := session.UpsertSession(t.Context(), g.store.DB, session.SessionInfo{ID: unclaimed.ID, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	call(unclaimed, testBrowserEmail, http.StatusConflict)
	g.starting.Wait()
	if len(starts) != 1 || (<-starts).ID != id {
		t.Fatal("repeated or conflicting claim started a second worker")
	}
}
