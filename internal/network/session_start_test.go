package network

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestReaperSettlesAChatWhoseWorkerDied(t *testing.T) {
	home := t.TempDir()
	g, _ := identityGateway(t, home, nil)
	stuckChat(t, g, seedFolder(t, home), "session-dead")
	g.reapStoppedChats(context.Background())
	g.reapStoppedChats(context.Background()) // settled once, not again
	stored, err := storedSession(context.Background(), g.auth.store.Reader, "session-dead")
	if err != nil || stored.ConversationState != "idle" || stored.State.Running || len(stored.Subagents) != 0 {
		t.Fatalf("settled row: %+v, %v", stored, err)
	}
	entries, _, _, err := storedConversationTail(context.Background(), g.auth.store.Reader, "session-dead", 0)
	if err != nil || len(entries) != 2 || entries[0].Kind != "notice" || entries[1].Kind != "agent_exit" {
		t.Fatalf("transcript %+v, %v", entries, err)
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
	info, err := g.resumeSession(context.Background(), "session-dead", "")
	if err != nil || info.ID != "session-dead" || info.ConversationState != "starting" {
		t.Fatalf("resume: %+v, %v", info, err)
	}
	g.starting.Wait()
	if got := <-launched; got.ID != "session-dead" || got.AgentSessionID != "agent-1" || got.ConversationState != "starting" {
		t.Fatalf("worker launch %+v", got)
	}
	if _, err := g.resumeSession(context.Background(), "session-dead", ""); !errors.Is(err, errResumeConflict) {
		t.Fatalf("second resume: %v", err)
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
