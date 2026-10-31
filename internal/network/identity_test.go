package network

import (
	"bytes"
	"context"
	"encoding/json"
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
)

func identityGateway(t *testing.T, home string, start func(context.Context, SessionInfo) (string, error)) (*Gateway, *url.URL) {
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
	t.Setenv("PATH", t.TempDir())
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
	g, origin := identityGateway(t, home, func(_ context.Context, info SessionInfo) (string, error) { started = info; return "session-test", nil })
	r := httptest.NewRequest("POST", origin.String()+"/api/sessions", bytes.NewBufferString(`{"directory":`+strconv.Quote(dir)+`,"harness":"claude","mode":"default"}`))
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	r.Header.Set("X-Agentpack-Actor", `{"kind":"peer","user":"mallory@example.com"}`)
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if started.StartedBy == nil || started.StartedBy.Kind != "browser" || started.StartedBy.User != browserUser {
		t.Fatalf("forged header changed actor: %+v", started.StartedBy)
	}
}

func TestStartSessionRecordsMCPActor(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	var started SessionInfo
	g, _ := identityGateway(t, home, func(_ context.Context, info SessionInfo) (string, error) { started = info; return "session-test", nil })
	info, err := g.startSession(context.Background(), LocalActor("mcp"), SessionInfo{Directory: dir, Harness: "claude", Mode: "default"})
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
	g, origin := identityGateway(t, home, func(_ context.Context, info SessionInfo) (string, error) { started = info; return "session-test", nil })
	r := httptest.NewRequest("POST", origin.String()+"/api/sessions", bytes.NewBufferString(`{"directory":`+strconv.Quote(dir)+`,"harness":"claude","mode":"default"}`))
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	r = r.WithContext(withActor(r.Context(), Actor{Kind: "peer", User: "alice@example.com", Node: "laptop"}))
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if started.StartedBy == nil || *started.StartedBy != (Actor{Kind: "peer", User: "alice@example.com", Node: "laptop"}) {
		t.Fatalf("context actor: %+v", started.StartedBy)
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
