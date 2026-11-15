package network

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
)

func writeLegacySession(t *testing.T, home, id string, pid int, info SessionInfo) string {
	t.Helper()
	directory := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(WorkerRecord{ID: id, PID: pid, Session: info, Address: "127.0.0.1:0", Secret: strings.Repeat("s", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func writeLegacyOwner(t *testing.T, home string, info SessionInfo, owner Actor) string {
	t.Helper()
	directory, path, canonical, err := acpConversationOwnerPath(home, info.FolderID, info.Harness, info.AgentSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(acpConversationOwnerRecord{FolderID: info.FolderID, Harness: canonical, Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLegacySessionImportIsIdempotentAndPreservesCanonicalRows(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	store := testSessionStore(t, home)
	folderID, err := FolderIdentity(workspace)
	if err != nil {
		t.Fatal(err)
	}
	owner := Actor{Kind: "peer", User: "owner@example.com", Node: "host", StableNodeID: "stable-owner"}
	info := SessionInfo{Directory: workspace, FolderID: folderID, Harness: "agent", Transport: "acp", AgentSessionID: "legacy-native", LastUsed: time.Now().Add(-8 * 24 * time.Hour).UTC()}
	write := func() (string, string) {
		directory := writeLegacySession(t, home, "session-legacy", 0, info)
		for name, data := range map[string]string{
			"name": "Saved label",
			"conversation.ndjson": `{"seq":4,"ts":1000,"kind":"agent_message_chunk","payload":{"text":{"text":"saved"}}}` + "\n" +
				`{"seq":7,"ts":1001,"kind":"turn_end","payload":{}}` + "\n",
			"image.png": "image bytes remain",
		} {
			if err := os.WriteFile(filepath.Join(directory, name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return directory, writeLegacyOwner(t, home, info, owner)
	}
	directory, ownerPath := write()
	g := &Gateway{store: store, options: GatewayOptions{Home: home}}
	g.importLegacySessions(t.Context())
	stored, err := storedSession(t.Context(), store.Reader, "session-legacy")
	if err != nil || stored.Name != "Saved label" || stored.StartedBy == nil || *stored.StartedBy != owner || stored.State.Running {
		t.Fatalf("legacy metadata changed: %+v %v", stored, err)
	}
	entries, lastSeq, truncated, err := storedConversationTail(t.Context(), store.Reader, stored.ID, 0)
	if err != nil || len(entries) != 2 || entries[0].Seq != 4 || lastSeq != 7 || !truncated {
		t.Fatalf("legacy history changed: %+v seq=%d truncated=%t err=%v", entries, lastSeq, truncated, err)
	}
	for _, path := range []string{ownerPath, filepath.Join(directory, "worker.json"), filepath.Join(directory, "name"), filepath.Join(directory, "conversation.ndjson")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("imported file retained: %s %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, "image.png")); err != nil {
		t.Fatal("import removed image data", err)
	}
	if _, err := store.DB.Exec("UPDATE sessions SET name='Renamed in SQL' WHERE id='session-legacy'"); err != nil {
		t.Fatal(err)
	}
	write()
	g.importLegacySessions(t.Context())
	g.importLegacySessions(t.Context())
	stored, err = storedSession(t.Context(), store.Reader, "session-legacy")
	var count int
	if err != nil || stored.Name != "Renamed in SQL" || store.Reader.QueryRow("SELECT count(*) FROM conversation_events").Scan(&count) != nil || count != 2 {
		t.Fatalf("repeat import changed canonical data: %+v rows=%d err=%v", stored, count, err)
	}
	got, found, err := readACPConversationOwner(t.Context(), store, folderID, "cursor", info.AgentSessionID)
	if err != nil || !found || got != owner {
		t.Fatalf("imported owner unavailable: %+v %t %v", got, found, err)
	}
}

func TestLegacyImportSkipsLiveWorkersAndLeavesUnreadableFiles(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	store := testSessionStore(t, home)
	folderID, err := FolderIdentity(workspace)
	if err != nil {
		t.Fatal(err)
	}
	info := SessionInfo{Directory: workspace, FolderID: folderID, Harness: "claude", Transport: "acp", AgentSessionID: "live-native"}
	owner := Actor{Kind: "browser", User: "owner@example.com", Node: "host"}
	live := writeLegacySession(t, home, "session-live", os.Getpid(), info)
	ownerPath := writeLegacyOwner(t, home, info, owner)
	bad := writeLegacySession(t, home, "session-unreadable", 0, info)
	if err := os.Mkdir(filepath.Join(bad, "conversation.ndjson"), 0700); err != nil {
		t.Fatal(err)
	}
	queue := obs.NewQueue()
	ctx := obs.WithSink(t.Context(), queue, obs.BuildMetadata("test", localStateNode, "gateway"))
	g := &Gateway{store: store, options: GatewayOptions{Home: home}}
	g.importLegacySessions(ctx)
	for _, directory := range []string{live, bad} {
		if _, err := os.Stat(filepath.Join(directory, "worker.json")); err != nil {
			t.Fatal("non-imported worker file removed", err)
		}
	}
	for _, id := range []string{"session-live", "session-unreadable"} {
		if _, err := storedSession(t.Context(), store.Reader, id); !errors.Is(err, errSessionNotFound) {
			t.Fatalf("unsafe session imported: %s %v", id, err)
		}
	}
	if _, err := os.Stat(ownerPath); err != nil {
		t.Fatal("live legacy worker lost its owner file", err)
	}
	if got, found, err := readACPConversationOwner(t.Context(), store, folderID, info.Harness, info.AgentSessionID); err != nil || !found || got != owner {
		t.Fatalf("live legacy owner unavailable in SQL: %+v %t %v", got, found, err)
	}
	if len(queue.Events) == 0 {
		t.Fatal("unreadable file was not logged")
	}
	event := <-queue.Events
	if event.Level != "error" || event.Fingerprint == "" {
		t.Fatalf("unreadable file lost diagnostic context: %+v", event)
	}
}

func TestLegacyOwnerWithoutWorkerMigratesAndBadOwnerStaysReadOnly(t *testing.T) {
	home, directory := t.TempDir(), t.TempDir()
	store := testSessionStore(t, home)
	folderID, err := FolderIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	owner := Actor{Kind: "browser", User: "owner@example.com", Node: "host"}
	info := SessionInfo{FolderID: folderID, Harness: "codex", AgentSessionID: "orphan-native"}
	path := writeLegacyOwner(t, home, info, owner)
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: store, options: GatewayOptions{Home: home}}
	g.importLegacySessions(t.Context())
	if _, _, err := readACPConversationOwner(t.Context(), store, folderID, info.Harness, info.AgentSessionID); !errors.Is(err, errACPResumeOwnerStorage) {
		t.Fatalf("bad legacy owner became claimable: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("bad owner file removed", err)
	}
	writeLegacyOwner(t, home, info, owner)
	g.importLegacySessions(t.Context())
	got, found, err := readACPConversationOwner(t.Context(), store, folderID, info.Harness, info.AgentSessionID)
	if err != nil || !found || got != owner {
		t.Fatalf("fixed owner did not migrate: %+v %t %v", got, found, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("migrated orphan owner file retained", err)
	}
}

func TestLegacyRecentImportPreservesFolderAuthorization(t *testing.T) {
	home, directory := t.TempDir(), t.TempDir()
	store := testSessionStore(t, home)
	path := filepath.Join(home, "network", "recent.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal([]SessionInfo{{Directory: directory, Harness: "codex", FolderID: "old-folder-identity", LastUsed: time.Now().UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: store, options: GatewayOptions{Home: home}}
	g.importLegacySessions(context.Background())
	folders, err := RecentFolders(home)
	if err != nil || len(folders) != 1 || folders[0].Available || folders[0].FolderID != "old-folder-identity" {
		t.Fatalf("import promoted a replacement folder: %+v %v", folders, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("legacy recent file retained", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	g.importLegacySessions(t.Context())
	var count int
	if err := store.Reader.QueryRow("SELECT count(*) FROM sessions WHERE directory=?", directory).Scan(&count); err != nil || count != 1 {
		t.Fatalf("repeat recent import duplicated rows: %d %v", count, err)
	}
}

func TestLegacyUnreadableOwnerDirectoryBlocksResumeUntilImportCanFinish(t *testing.T) {
	home := t.TempDir()
	store := testSessionStore(t, home)
	path := filepath.Join(home, "network", "acp-session-owners")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: store, options: GatewayOptions{Home: home}}
	g.importLegacySessions(t.Context())
	if _, _, err := readACPConversationOwner(t.Context(), store, "folder", "claude", "native"); !errors.Is(err, errACPResumeOwnerStorage) {
		t.Fatalf("unreadable ownership directory allowed an unknown claim: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	g.importLegacySessions(t.Context())
	if _, found, err := readACPConversationOwner(t.Context(), store, "folder", "claude", "native"); err != nil || found {
		t.Fatalf("completed empty import retained its guard: found=%t err=%v", found, err)
	}
}

func TestGatewayStartsWithUnreadableLegacySessionData(t *testing.T) {
	home := t.TempDir()
	directory := writeLegacySession(t, home, "session-unreadable", 0, SessionInfo{Harness: "claude", Transport: "acp"})
	if err := os.Mkdir(filepath.Join(directory, "conversation.ndjson"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeGateway(ctx, GatewayOptions{Home: home, Address: "127.0.0.1:0", Version: "test"}) }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	ready, stopReady := context.WithTimeout(ctx, 2*time.Second)
	defer stopReady()
	for {
		if _, err := NodeRequest(ready, home, "GET", "/healthz"); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("unreadable legacy data stopped gateway startup: %v", err)
		case <-ticker.C:
		case <-ready.Done():
			t.Fatal(ready.Err())
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gateway did not stop")
	}
	if _, err := os.Stat(filepath.Join(directory, "worker.json")); err != nil {
		t.Fatal("unreadable legacy session was removed", err)
	}
}
