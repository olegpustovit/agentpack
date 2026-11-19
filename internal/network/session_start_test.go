package network

import (
	"context"
	"errors"
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
	g, _ := identityGateway(t, home, func(context.Context, SessionInfo, string) (string, error) {
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
