package network

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestChatStartAnswersBeforeTheAgentIsUp(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	release := make(chan struct{})
	launched := make(chan SessionInfo, 1)
	g, _ := identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) {
		launched <- info
		<-release
		return info.ID, nil
	})
	defer close(release)
	info, err := g.startSession(context.Background(), LocalActor("local"), SessionInfo{Directory: dir, Harness: "claude", Mode: "default", Transport: "acp"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.ID, "session-") || info.ConversationState != "starting" {
		t.Fatalf("start returned %+v", info)
	}
	stored, err := storedSession(context.Background(), g.auth.store.Reader, info.ID)
	if err != nil || stored.ConversationState != "starting" {
		t.Fatalf("starting row: %+v, %v", stored, err)
	}
	select {
	case got := <-launched:
		if got.ID != info.ID {
			t.Fatalf("worker got ID %q, want %q", got.ID, info.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("the launch did not run in the background")
	}
	// The starting row is now the folder's newest recent entry; it must not lock the folder.
	if _, err := g.startSession(context.Background(), LocalActor("local"), SessionInfo{Directory: dir, Harness: "claude", Mode: "default", Transport: "acp"}, ""); err != nil {
		t.Fatalf("second start while the first is starting: %v", err)
	}
	<-launched
}

func TestFailedChatStartKeepsTheErrorAndTheRow(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	var g *Gateway
	g, _ = identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) {
		// The worker failed after registering: it ended (and hid) its prompt-less row first.
		if err := endSession(context.Background(), g.auth.store.DB, info.ID, TerminalState{ExitCode: 1}); err != nil {
			t.Error(err)
		}
		return "", errors.New("pack sync failed")
	})
	info, err := g.startSession(context.Background(), LocalActor("local"), SessionInfo{Directory: dir, Harness: "claude", Mode: "default", Transport: "acp"}, "")
	if err != nil {
		t.Fatal(err)
	}
	g.starting.Wait()
	stored, err := storedSession(context.Background(), g.auth.store.Reader, info.ID)
	if err != nil || stored.EndedAt.IsZero() || stored.ConversationState != "idle" {
		t.Fatalf("failed start not ended: %+v", stored)
	}
	var hidden bool
	var body string
	if err := g.auth.store.Reader.QueryRow("SELECT hidden FROM sessions WHERE id=?", info.ID).Scan(&hidden); err != nil || hidden {
		t.Fatalf("failed start hidden=%v err=%v", hidden, err)
	}
	if err := g.auth.store.Reader.QueryRow("SELECT group_concat(body) FROM conversation_events WHERE session_id=?", info.ID).Scan(&body); err != nil || !strings.Contains(body, "pack sync failed") {
		t.Fatalf("transcript %q, %v", body, err)
	}
}

func TestGatewayStartEndsLeftoverStartingRows(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	g, _ := identityGateway(t, home, nil)
	if err := upsertSession(context.Background(), g.auth.store.DB, SessionInfo{ID: "session-leftover", Directory: dir, Harness: "claude", Transport: "acp", ConversationState: "starting"}); err != nil {
		t.Fatal(err)
	}
	g.endLeftoverStarts(context.Background())
	stored, err := storedSession(context.Background(), g.auth.store.Reader, "session-leftover")
	if err != nil || stored.EndedAt.IsZero() || stored.ConversationState != "idle" {
		t.Fatalf("leftover start: %+v, %v", stored, err)
	}
}

func TestEndingAChatSettlesAStaleState(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	g, _ := identityGateway(t, home, nil)
	if err := upsertSession(context.Background(), g.auth.store.DB, SessionInfo{ID: "session-busy", Directory: dir, Harness: "claude", Transport: "acp", ConversationState: "needs_you"}); err != nil {
		t.Fatal(err)
	}
	if err := endSession(context.Background(), g.auth.store.DB, "session-busy", TerminalState{}); err != nil {
		t.Fatal(err)
	}
	stored, err := storedSession(context.Background(), g.auth.store.Reader, "session-busy")
	if err != nil || stored.ConversationState != "idle" {
		t.Fatalf("ended chat: %+v, %v", stored, err)
	}
}

func TestWorkerThatExitsEarlyReportsItsOwnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture checks Unix stderr file semantics")
	}
	home := t.TempDir()
	script := filepath.Join(t.TempDir(), "worker")
	body := "#!/bin/sh\ncat >/dev/null\n[ -p /dev/stderr ] && { echo 'stderr is a pipe that dies with the gateway' >&2; exit 1; }\nprintf '          \\n   \\033[1mERROR\\033[0m  \\n          \\n  Acp session/new: Authentication required.\\n' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := StartWorker(context.Background(), home, script, WorkerLaunch{Transport: "acp"})
	if err == nil || err.Error() != "Acp session/new: Authentication required." {
		t.Fatalf("start error: %v", err)
	}
}

// stuckChat is a chat whose worker died mid-turn: ended, yet still working with a subagent.
func stuckChat(t *testing.T, g *Gateway, dir, id string) {
	t.Helper()
	ctx := context.Background()
	info := SessionInfo{ID: id, Directory: dir, Harness: "claude", Mode: "default", Transport: "acp", AgentSessionID: "agent-1", ConversationState: "working", State: TerminalState{Running: true, ExitCode: -1}}
	if err := upsertSession(ctx, g.auth.store.DB, info); err != nil {
		t.Fatal(err)
	}
	if _, err := g.auth.store.DB.Exec(`UPDATE sessions SET ended_at=1,subagents='[{"id":"a"}]' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
}

func TestReaperSettlesAChatWhoseWorkerDiedAndResumesItOnce(t *testing.T) {
	home := t.TempDir()
	launched := make(chan SessionInfo, 2)
	var g *Gateway
	g, _ = identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) {
		launched <- info
		// The resumed agent dies at once, like a machine that drops off again.
		_, err := g.auth.store.DB.Exec(`UPDATE sessions SET ended_at=2,conversation_state='working' WHERE id=?`, info.ID)
		return info.ID, err
	})
	stuckChat(t, g, seedFolder(t, home), "session-dead")
	g.reapStoppedChats(context.Background())
	g.starting.Wait()
	if got := <-launched; got.ID != "session-dead" || got.AgentSessionID != "agent-1" {
		t.Fatalf("resumed %+v", got)
	}
	// Dying again inside the window settles the chat and leaves it for the person.
	g.reapStoppedChats(context.Background())
	g.starting.Wait()
	select {
	case got := <-launched:
		t.Fatalf("resumed twice: %+v", got)
	default:
	}
	stored, err := storedSession(context.Background(), g.auth.store.Reader, "session-dead")
	if err != nil || stored.ConversationState != "idle" || stored.State.Running || len(stored.Subagents) != 0 {
		t.Fatalf("settled row: %+v, %v", stored, err)
	}
	var body string
	if err := g.auth.store.Reader.QueryRow("SELECT group_concat(body) FROM conversation_events WHERE session_id=?", "session-dead").Scan(&body); err != nil || !strings.Contains(body, "resumes by itself") || !strings.Contains(body, "Send a message to resume") {
		t.Fatalf("transcript %q, %v", body, err)
	}
}

func TestResumeRestartsAStoppedChatUnderItsOwnID(t *testing.T) {
	home := t.TempDir()
	launched := make(chan SessionInfo, 2)
	g, _ := identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) {
		launched <- info
		return info.ID, nil
	})
	stuckChat(t, g, seedFolder(t, home), "session-dead")
	info, err := g.resumeSession(context.Background(), "session-dead", "", "")
	if err != nil || info.ID != "session-dead" || info.ConversationState != "starting" || !info.Reachable {
		t.Fatalf("resume: %+v, %v", info, err)
	}
	g.starting.Wait()
	if got := <-launched; got.ID != "session-dead" || got.AgentSessionID != "agent-1" || got.ConversationState != "starting" {
		t.Fatalf("worker launch %+v", got)
	}
	if _, err := g.resumeSession(context.Background(), "session-dead", "", ""); !errors.Is(err, errResumeConflict) {
		t.Fatalf("second resume: %v", err)
	}
}

func TestSwitchHarnessContinuesTheChatWithAFreshAgent(t *testing.T) {
	home := t.TempDir()
	launched := make(chan SessionInfo, 1)
	g, _ := identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) {
		launched <- info
		return info.ID, nil
	})
	stuckChat(t, g, seedFolder(t, home), "session-dead")
	info, err := g.switchHarness(context.Background(), "session-dead", "codex")
	if err != nil || info.ID != "session-dead" || info.Harness != "codex" || info.AgentSessionID != "" {
		t.Fatalf("switch: %+v, %v", info, err)
	}
	g.starting.Wait()
	if got := <-launched; got.Harness != "codex" || got.AgentSessionID != "" {
		t.Fatalf("worker launch %+v", got)
	}
}

func TestResumedWorkerReclaimsItsDirectoryOnlyWhenNoWorkerOwnsIt(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "session-old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := createSessionDirectory(root, "session-old"); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "session-old", "worker.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := createSessionDirectory(root, "session-old"); err == nil {
		t.Fatal("reclaimed a directory a worker owns")
	}
}

// A chat whose agent was not signed in records a sign-in notice, and after the sign-in it starts
// fresh under its own ID even though the agent never made a session.
func TestSignInFailureMarksTheNoticeAndTheChatStartsLater(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	launches := 0
	launched := make(chan SessionInfo, 1)
	g, _ := identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) {
		if launches++; launches == 1 {
			return "", errors.New("Antigravity" + notSignedIn + "gigacomp.")
		}
		launched <- info
		return info.ID, nil
	})
	info, err := g.startSession(context.Background(), LocalActor("local"), SessionInfo{Directory: dir, Harness: "agy", Mode: "default", Transport: "acp"}, "")
	if err != nil {
		t.Fatal(err)
	}
	g.starting.Wait()
	var body string
	if err := g.auth.store.Reader.QueryRow("SELECT group_concat(body) FROM conversation_events WHERE session_id=?", info.ID).Scan(&body); err != nil || !strings.Contains(body, `"code":"auth_required"`) || !strings.Contains(body, `"harness":"agy"`) || !strings.Contains(body, `"id":"google"`) {
		t.Fatalf("transcript %q, %v", body, err)
	}
	if _, err := g.resumeSession(context.Background(), info.ID, "hello", "resume-prompt-0001"); err != nil {
		t.Fatalf("resume after sign-in: %v", err)
	}
	if _, err := g.resumeSession(context.Background(), info.ID, "hello", "resume-prompt-0001"); err != nil {
		t.Fatalf("retry resume after sign-in: %v", err)
	}
	g.starting.Wait()
	if got := <-launched; got.ID != info.ID || got.AgentSessionID != "" {
		t.Fatalf("worker launch %+v", got)
	}
}

func TestStartingChatPersistsThreePromptsBeforeTheWorkerExists(t *testing.T) {
	home := t.TempDir()
	dir := seedFolder(t, home)
	release := make(chan struct{})
	g, origin := identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) {
		<-release
		return info.ID, nil
	})
	defer close(release)
	actor := browserActor(testBrowserEmail)
	input := chatStartRequest{ID: "session-d7148a740d2c495991c792576427836d", Directory: dir,
		Harness: "claude", Mode: "default", Transport: "acp", FirstMessage: "one"}
	if _, err := g.claimChatStart(t.Context(), actor, input); err != nil {
		t.Fatal(err)
	}
	submit := func(text, key string) {
		r := httptest.NewRequest(http.MethodPost, origin.String()+"/api/sessions/"+input.ID+"/conversation/prompt", strings.NewReader(string(mustRawJSON(map[string]string{"text": text}))))
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		r.Header.Set("Idempotency-Key", key)
		r = r.WithContext(withActor(r.Context(), actor))
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		var result struct {
			Queued string `json:"queued"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || result.Queued != key {
			t.Fatalf("queue response = %d %s", w.Code, w.Body.String())
		}
	}
	submit("two", "request-000000002")
	submit("three", "request-000000003")
	submit("two", "request-000000002")
	var title string
	if err := g.auth.store.Reader.QueryRowContext(t.Context(), "SELECT title FROM sessions WHERE id=?", input.ID).Scan(&title); err != nil || title != "one" {
		t.Fatalf("title = %q, err = %v", title, err)
	}
	entries, seq, _, err := storedConversationTail(t.Context(), g.auth.store.Reader, input.ID, 0)
	if err != nil || seq != 3 || len(entries) != 3 {
		t.Fatalf("persisted queue = %+v seq=%d err=%v", entries, seq, err)
	}
	session, agent := newFakeAgentSession(t, entries...)
	session.ResumeTurns()
	for _, want := range []string{"one", "two", "three"} {
		if got := agent.nextPrompt(t); got.Text != want {
			t.Fatalf("drained %q, want %q", got.Text, want)
		}
		agent.turns <- fakeTurn{}
	}
	var prompts int
	for _, entry := range agent.log.Snapshot() {
		if entry.Kind == "prompt" {
			prompts++
			var body acp.PromptInput
			if body, err = promptInputFromLog(entry.Payload); err != nil || body.QueueID == "" {
				t.Fatalf("prompt omitted its queue ID: %s", entry.Payload)
			}
		}
	}
	if prompts != 3 {
		t.Fatalf("agent received %d prompts", prompts)
	}
}

func TestStopRetryDoesNotStopReplacementWorker(t *testing.T) {
	home := t.TempDir()
	g, _ := identityGateway(t, home, nil)
	var stops atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/stop" {
			t.Errorf("unexpected worker request %s %s", r.Method, r.URL.Path)
		}
		stops.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer worker.Close()
	const id = "session-stop"
	const requestID = "stop-intent-0001"
	directory := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	record := WorkerRecord{ID: id, Address: strings.TrimPrefix(worker.URL, "http://")}
	writeWorker := func(secret string) {
		t.Helper()
		record.Secret = strings.Repeat(secret, 64)
		if err := publishWorkerEndpoint(directory, record); err != nil {
			t.Fatal(err)
		}
	}
	writeWorker("a")
	if err := g.stopSession(t.Context(), id, requestID); err != nil {
		t.Fatal(err)
	}
	writeWorker("b")
	if err := g.stopSession(t.Context(), id, requestID); err != nil || stops.Load() != 1 {
		t.Fatalf("stop retry sent to replacement: stops=%d err=%v", stops.Load(), err)
	}
	if err := g.stopSession(t.Context(), id, "stop-intent-0002"); err != nil || stops.Load() != 2 {
		t.Fatalf("new request did not stop replacement: stops=%d err=%v", stops.Load(), err)
	}
}

func TestReaperSettlesInterruptedChatStart(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	ctx := t.Context()
	old := time.Now().Add(-3 * time.Minute)
	for _, id := range []string{"session-orphan", "session-in-flight"} {
		if err := upsertSession(ctx, g.auth.store.DB, SessionInfo{ID: id, Harness: "codex", Transport: "acp", ConversationState: "starting", StartedAt: old, LastUsed: old}); err != nil {
			t.Fatal(err)
		}
	}
	g.startingChats.Store("session-in-flight", struct{}{})
	g.reapStoppedChats(ctx)
	orphan, err := storedSession(ctx, g.auth.store.Reader, "session-orphan")
	if err != nil || orphan.EndedAt.IsZero() {
		t.Fatalf("interrupted start stayed active: %v", err)
	}
	active, err := storedSession(ctx, g.auth.store.Reader, "session-in-flight")
	if err != nil || !active.EndedAt.IsZero() {
		t.Fatalf("reaper settled an active start: %v", err)
	}
}

func TestOwnerCanArchiveStoppedImportedRecent(t *testing.T) {
	g, origin := identityGateway(t, t.TempDir(), nil)
	ctx := t.Context()
	id := "session-legacy-recent-example"
	if err := upsertSession(ctx, g.auth.store.DB, SessionInfo{ID: id, Harness: "codex", Transport: "acp", StartedBy: &Actor{Kind: "peer", User: "old-owner"}}); err != nil {
		t.Fatal(err)
	}
	if err := endSession(ctx, g.auth.store.DB, id, TerminalState{}); err != nil {
		t.Fatal(err)
	}
	// The same recent replicated from a peer is archived on this gateway's copy of the row.
	peerID := id + "-peer"
	if _, err := g.auth.store.DB.ExecContext(ctx, `INSERT INTO sessions(id,harness,transport,started_by,node_id,ended_at)
		SELECT ?,harness,transport,started_by,'peer-node',ended_at FROM sessions WHERE id=?`, peerID, id); err != nil {
		t.Fatal(err)
	}
	for index, test := range []struct {
		id   string
		user string
		body string
		want int
	}{
		{id, "member@example.com", `{"hidden":true}`, http.StatusForbidden},
		{id, testBrowserEmail, `{"name":"renamed"}`, http.StatusForbidden},
		{id, testBrowserEmail, `{"hidden":true}`, http.StatusOK},
		{peerID, testBrowserEmail, `{"hidden":true}`, http.StatusOK},
	} {
		id := test.id
		token := test.user + string(rune('0'+index))
		testBrowserSession(t, g.auth, test.user, token)
		req := httptest.NewRequest(http.MethodPatch, origin.String()+"/api/sessions/"+id, strings.NewReader(test.body))
		req.Header.Set("Origin", origin.String())
		req.AddCookie(&http.Cookie{Name: "agentpack_session", Value: token})
		res := httptest.NewRecorder()
		g.Handler().ServeHTTP(res, req)
		if res.Code != test.want {
			t.Fatalf("%s %s: status %d, want %d (%s)", test.user, test.body, res.Code, test.want, res.Body.String())
		}
	}
}
