package network

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"tailscale.com/client/local"
)

func identityGateway(t *testing.T, home string, start func(context.Context, SessionInfo, string) (string, error)) (*Gateway, *url.URL) {
	t.Helper()
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	return &Gateway{auth: auth, origin: origin, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home, Version: "test", Start: start}}, origin
}

// seedFolder registers a fresh directory as a recent folder for the gateway's folder check.
func seedFolder(t *testing.T, home string) string {
	t.Helper()
	if err := EnsurePrivateDirectory(filepath.Join(home, "network")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := remember(home, SessionInfo{Directory: dir, Harness: "claude", Mode: "default"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestIdentityWithoutTailscaleReportsRootAuthOnly(t *testing.T) {
	tailscaleLocal = local.Client{Socket: filepath.Join(t.TempDir(), "missing"), UseSocketOnly: true}
	t.Cleanup(func() { tailscaleLocal = local.Client{} })
	home := t.TempDir()
	g, origin := identityGateway(t, home, nil)
	r := httptest.NewRequest("GET", origin.String()+"/api/identity", nil)
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	var got identityResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if got.Auth != "root" || got.TailscaleUser != "" || got.Node.Version != "test" {
		t.Fatalf("identity without tailscale: %+v", got)
	}
}

func TestBrowserActorIgnoresForgedActorHeader(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	var started SessionInfo
	g, origin := identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) {
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
	// The node secret makes this the machine's own OS user; the header is ignored.
	if want := LocalActor("local"); started.StartedBy == nil || *started.StartedBy != want {
		t.Fatalf("forged header changed actor: %+v, want %+v", started.StartedBy, want)
	}
}

func TestStartSessionRecordsMCPActor(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	var started SessionInfo
	g, _ := identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) {
		started = info
		return "session-test", nil
	})
	info, err := g.startSession(context.Background(), LocalActor("mcp"), SessionInfo{Directory: dir, Harness: "claude", Mode: "default"}, "")
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
	var started SessionInfo
	g, origin := identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) {
		started = info
		return "session-test", nil
	})
	r := httptest.NewRequest("POST", origin.String()+"/api/sessions", bytes.NewBufferString(`{"directory":`+strconv.Quote(dir)+`,"harness":"claude","mode":"default"}`))
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	r = r.WithContext(withActor(r.Context(), Actor{Kind: "peer", User: "alice@example.com", Node: "laptop", StableNodeID: "stable-laptop"}))
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if started.StartedBy == nil || *started.StartedBy != (Actor{Kind: "peer", User: "alice@example.com", Node: "laptop", StableNodeID: "stable-laptop"}) {
		t.Fatalf("context actor: %+v", started.StartedBy)
	}
	if started.StartedBy.StableNodeID == "" {
		t.Fatal("private worker launch lost the stable peer identity")
	}
	var public SessionInfo
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
	owner := Actor{Kind: "peer", User: "alice@example.com", Node: "old-name", StableNodeID: "stable-owner"}
	session := SessionInfo{StartedBy: &owner}
	if !(Actor{Kind: "peer", User: owner.User, Node: "new-name", StableNodeID: owner.StableNodeID}).canWriteSession(session) {
		t.Fatal("same stable peer identity lost write access after a name change")
	}
	for _, actor := range []Actor{
		{Kind: "peer", User: owner.User, Node: owner.Node, StableNodeID: "stable-other"},
		{Kind: "peer", User: owner.User, Node: owner.Node},
		{Kind: "peer", User: "mallory@example.com", Node: owner.Node, StableNodeID: owner.StableNodeID},
	} {
		if actor.canWriteSession(session) {
			t.Fatalf("non-owner peer gained write access: %+v", actor)
		}
	}
	legacyOwner := Actor{Kind: "peer", User: owner.User, Node: owner.Node}
	if (Actor{Kind: "peer", User: owner.User, Node: owner.Node, StableNodeID: "stable-other"}).canWriteSession(SessionInfo{StartedBy: &legacyOwner}) {
		t.Fatal("a reused peer name matched an owner without a stable ID")
	}
	localOwner := LocalActor("cli")
	localBrowser := browserActor()
	if !localBrowser.canWriteSession(SessionInfo{StartedBy: &localOwner}) ||
		!localOwner.canWriteSession(SessionInfo{StartedBy: &localBrowser}) {
		t.Fatal("browser and CLI actors on the same host lost owner access")
	}
	localPeer := Actor{Kind: "peer", User: localOwner.User, Node: localOwner.Node, StableNodeID: "stable-peer"}
	if localPeer.canWriteSession(SessionInfo{StartedBy: &localOwner}) {
		t.Fatal("peer identity crossed into a local owner session")
	}
	peerOwner := Actor{Kind: "peer", User: localOwner.User, Node: localOwner.Node, StableNodeID: "stable-peer"}
	if localBrowser.canWriteSession(SessionInfo{StartedBy: &peerOwner}) {
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
	owner := browserActor()
	record := WorkerRecord{
		ID: sessionID, Address: strings.TrimPrefix(upstream.URL, "http://"), Secret: strings.Repeat("s", 64),
		Session: SessionInfo{ID: sessionID, Transport: "acp", StartedBy: &owner},
	}
	directory := filepath.Join(home, "network", "sessions", sessionID)
	if err := EnsurePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	request := func(method, path, body string, actor *Actor) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, origin.String()+"/api/sessions/"+sessionID+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		if actor != nil {
			r = r.WithContext(withActor(r.Context(), *actor))
		}
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w
	}

	readOnly := Actor{Kind: "peer", User: owner.User, Node: "another-node"}
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

	if got := request(http.MethodGet, "/conversation", "", &readOnly).Code; got != http.StatusNoContent {
		t.Fatalf("conversation read from another node: status %d", got)
	}
	if got := <-requests; got != "/conversation" {
		t.Fatalf("read worker path = %q", got)
	}

	localCLI := Actor{Kind: "cli", User: owner.User, Node: owner.Node}
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
}
func TestPeerACPResumeRequiresRecordedOwner(t *testing.T) {
	home := t.TempDir()
	directory := seedFolder(t, home)
	folderID, err := FolderIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	owner := Actor{Kind: "peer", User: "alice@example.com", Node: "old-name", StableNodeID: "stable-owner"}
	if err := rememberACPConversationOwner(home, SessionInfo{
		Directory: directory, FolderID: folderID, Harness: "claude", Mode: "default",
		Transport: "acp", AgentSessionID: "prior-1", StartedBy: &owner,
	}, ""); err != nil {
		t.Fatal(err)
	}
	var started []SessionInfo
	g, origin := identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) {
		started = append(started, info)
		return "session-new", nil
	})
	resume := func(agentSessionID string, actor Actor) *httptest.ResponseRecorder {
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
		r = r.WithContext(withActor(r.Context(), actor))
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w
	}

	otherPeer := Actor{Kind: "peer", User: owner.User, Node: owner.Node, StableNodeID: "stable-other"}
	if got := resume("prior-1", otherPeer).Code; got != http.StatusForbidden || len(started) != 0 {
		t.Fatalf("non-owner resume status=%d starts=%d", got, len(started))
	}
	renamedOwner := Actor{Kind: "peer", User: owner.User, Node: "new-name", StableNodeID: owner.StableNodeID}
	if got := resume("prior-1", renamedOwner).Code; got != http.StatusOK || len(started) != 1 {
		t.Fatalf("owner resume status=%d starts=%d", got, len(started))
	}
	if started[0].StartedBy == nil || *started[0].StartedBy != renamedOwner {
		t.Fatalf("resumed owner = %+v, want %+v", started[0].StartedBy, renamedOwner)
	}

	_, ownerPath, _, err := acpConversationOwnerPath(home, folderID, "claude", "prior-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownerPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resume("prior-1", renamedOwner).Code; got != http.StatusInternalServerError || len(started) != 1 {
		t.Fatalf("corrupt owner record status=%d starts=%d", got, len(started))
	}
	if got := resume("unowned", otherPeer).Code; got != http.StatusForbidden || len(started) != 1 {
		t.Fatalf("unowned peer resume status=%d starts=%d", got, len(started))
	}
	if got := resume("external-history", browserActor()).Code; got != http.StatusOK || len(started) != 2 {
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
	owner := Actor{Kind: "peer", User: "alice@example.com", Node: "old-name", StableNodeID: "stable-owner"}
	sourceSession := SessionInfo{
		ID: "session-source", Directory: directory, FolderID: folderID, Harness: "claude",
		Mode: "default", Transport: "acp", AgentSessionID: "source-session", StartedBy: &owner,
	}
	if err := rememberACPConversationOwner(home, sourceSession, ""); err != nil {
		t.Fatal(err)
	}
	sourceRecordDir := filepath.Join(home, "network", "sessions", sourceSession.ID)
	if err := EnsurePrivateDirectory(sourceRecordDir); err != nil {
		t.Fatal(err)
	}
	saveSourceRecord := func(session SessionInfo) {
		t.Helper()
		data, err := json.Marshal(WorkerRecord{
			Session: session, ID: sourceSession.ID, Address: "127.0.0.1:7420", Secret: strings.Repeat("s", 64),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourceRecordDir, "worker.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	saveSourceRecord(sourceSession)
	var started SessionInfo
	var receivedForkID string
	starts := 0
	g, origin := identityGateway(t, home, func(_ context.Context, info SessionInfo, forkSessionID string) (string, error) {
		starts++
		started, receivedForkID = info, forkSessionID
		return "session-forked", nil
	})
	request := func(actor Actor) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, origin.String()+"/api/sessions/"+sourceSession.ID+"/fork", nil)
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		r = r.WithContext(withActor(r.Context(), actor))
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w
	}
	renamedOwner := Actor{Kind: "peer", User: owner.User, Node: "new-name", StableNodeID: owner.StableNodeID}
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
	otherPeer := Actor{Kind: "peer", User: owner.User, Node: owner.Node, StableNodeID: "stable-other"}
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
	if err := rememberACPConversationOwner(home, forked, ""); err != nil {
		t.Fatal(err)
	}
	gotOwner, found, err := readACPConversationOwner(home, folderID, "claude", "forked-session")
	if err != nil || !found || gotOwner != renamedOwner {
		t.Fatalf("forked native session owner=%+v found=%t err=%v", gotOwner, found, err)
	}
	gotOwner, found, err = readACPConversationOwner(home, folderID, "claude", "source-session")
	if err != nil || !found || gotOwner != owner {
		t.Fatalf("source native session owner changed: owner=%+v found=%t err=%v", gotOwner, found, err)
	}
}

func TestStartWorkerStampsStartedAtOnlyWhenZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub worker")
	}
	stub := filepath.Join(t.TempDir(), "stub-agentpack")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat > \"$AGENTPACK_TEST_CAPTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "launch.json")
	t.Setenv("AGENTPACK_TEST_CAPTURE", capture)
	captured := func(t *testing.T, launch WorkerLaunch) WorkerLaunch {
		t.Helper()
		// The stub never registers a worker, so StartWorker returns an error after capturing stdin.
		_, _ = StartWorker(context.Background(), t.TempDir(), stub, launch)
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
	if got := captured(t, WorkerLaunch{Session: SessionInfo{ID: "a"}}); got.Session.StartedAt.IsZero() || time.Since(got.Session.StartedAt) > time.Minute {
		t.Fatalf("zero StartedAt not stamped: %v", got.Session.StartedAt)
	}
	fixed := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	if got := captured(t, WorkerLaunch{Session: SessionInfo{ID: "b", StartedAt: fixed}}); !got.Session.StartedAt.Equal(fixed) {
		t.Fatalf("preset StartedAt overwritten: %v", got.Session.StartedAt)
	}
	fork := captured(t, WorkerLaunch{Session: SessionInfo{ID: "c"}, ForkSessionID: "source-session"})
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
	actor := LocalActor("cli")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := RunWorker(ctx, home, WorkerLaunch{Session: SessionInfo{StartedBy: &actor, StartedAt: time.Now()}, Program: executable, Directory: home, Arguments: []string{"-test.run=^TestTerminalChild$"}, Environment: append(os.Environ(), "AGENTPACK_TEST_TERMINAL_CHILD=1"), Rows: 24, Columns: 80}, func(id string) error { ready <- id; return nil })
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
	record, err := workerRecord(home, id)
	if err != nil {
		t.Fatal(err)
	}
	if record.Session.StartedBy == nil || record.Session.StartedBy.Kind != "cli" || record.Session.StartedBy.User == "" || record.Session.StartedAt.IsZero() {
		t.Fatalf("worker record lost actor: %+v", record.Session)
	}
	cancel()
	<-done
}
