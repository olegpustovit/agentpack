package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
	"github.com/olegpustovit/agentpack/internal/state/statetest"
)

func remember(home string, info session.SessionInfo) error {
	info.Directory = filepath.Clean(info.Directory)
	var err error
	info.FolderID, err = network.FolderIdentity(info.Directory)
	if err != nil {
		return err
	}
	info.ID, info.LastUsed = "session-"+rand.Text(), time.Now().UTC()
	store, err := state.Open(home)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := session.UpsertSession(context.Background(), store.DB, info); err != nil {
		return err
	}
	return session.EndSession(context.Background(), store.DB, info.ID, info.State)
}

func TestOpenWorkerFollowsStableNodeBinding(t *testing.T) {
	home, directory := t.TempDir(), t.TempDir()
	worker, gateway := statetest.Open(t, home), statetest.Open(t, home)
	owner := session.Actor{Kind: "browser", User: testBrowserEmail, Node: "host"}
	info := session.SessionInfo{ID: "session-live", Directory: directory, Harness: "codex", Transport: "acp", StartedBy: &owner}
	if err := session.UpsertSession(t.Context(), worker.DB, info); err != nil {
		t.Fatal(err)
	}
	if err := gateway.BindNode(t.Context(), "stable-node"); err != nil {
		t.Fatal(err)
	}
	info.AgentSessionID = "native"
	if err := network.RememberACPConversationOwner(t.Context(), worker, info, ""); err != nil {
		t.Fatal("already-open worker lost its native claim", err)
	}
	if err := network.PatchWorkerConversationState(t.Context(), worker.DB, info.ID, "working"); err != nil {
		t.Fatal(err)
	}
	if err := session.UpsertSession(t.Context(), worker.DB, info); err != nil {
		t.Fatal(err)
	}
	if err := session.EndSession(t.Context(), worker.DB, info.ID, session.TerminalState{}); err != nil {
		t.Fatal(err)
	}
	var wrong int
	if err := gateway.Reader.QueryRow("SELECT count(*) FROM sessions WHERE node_id<>'stable-node'").Scan(&wrong); err != nil || wrong != 0 {
		t.Fatalf("worker wrote under stale identity: %d %v", wrong, err)
	}
	if got, err := session.StoredSession(t.Context(), gateway.Reader, info.ID); err != nil || got.AgentSessionID != "native" {
		t.Fatalf("bound session unavailable: %+v %v", got, err)
	}
}

func TestSessionMetadataRoundTripAndUpdatesKeepNameAndVisibility(t *testing.T) {
	store := statetest.Open(t, t.TempDir())
	ctx := context.Background()
	now := time.Now().UTC()
	info := session.SessionInfo{ID: "session-test", LastUsed: now, StartedAt: now, Directory: "/project",
		FolderID: "folder", Name: "Review", DefinitionRoot: "/definitions", Harness: "codex",
		Mode: "review", Transport: "acp", AgentSessionID: "native", ConversationState: "idle",
		AgentCapabilities: &session.AgentCapabilities{LoadSession: true}, StartedBy: &session.Actor{Kind: "browser", User: testBrowserEmail, Node: "host"},
		State: session.TerminalState{Running: true, Rows: 24, Columns: 80, ExitCode: -1}}
	if err := session.UpsertSession(ctx, store.DB, info); err != nil {
		t.Fatal(err)
	}
	info.NodeID, _ = store.NodeID(ctx)
	got, err := session.StoredSession(ctx, store.Reader, info.ID)
	if err != nil || !reflect.DeepEqual(got, info) {
		t.Fatalf("round trip: %+v want %+v err=%v", got, info, err)
	}
	if _, err := store.DB.Exec("UPDATE sessions SET hidden=1 WHERE id=?", info.ID); err != nil {
		t.Fatal(err)
	}
	info.Name, info.ConversationState = "", "working"
	if err := session.UpsertSession(ctx, store.DB, info); err != nil {
		t.Fatal(err)
	}
	var name, email string
	var hidden int
	if err := store.Reader.QueryRow("SELECT name,hidden,owner_email FROM sessions WHERE id=?", info.ID).Scan(&name, &hidden, &email); err != nil || name != "Review" || hidden != 1 || email != testBrowserEmail {
		t.Fatalf("metadata update lost user changes: name=%q hidden=%d owner=%q err=%v", name, hidden, email, err)
	}
	if err := session.EndSession(ctx, store.DB, info.ID, info.State); err != nil {
		t.Fatal(err)
	}
	got, err = session.StoredSession(ctx, store.Reader, info.ID)
	var ended int64
	if err != nil || got.State.Running || store.Reader.QueryRow("SELECT ended_at FROM sessions WHERE id=?", info.ID).Scan(&ended) != nil || ended == 0 {
		t.Fatalf("session not ended: %+v ended=%d err=%v", got, ended, err)
	}
}

func TestEndedChatWithoutMessagesLeavesTheList(t *testing.T) {
	store := statetest.Open(t, t.TempDir())
	ctx := t.Context()
	for _, id := range []string{"session-empty", "session-used", "session-terminal"} {
		transport := "acp"
		if id == "session-terminal" {
			transport = ""
		}
		if err := session.UpsertSession(ctx, store.DB, session.SessionInfo{ID: id, Transport: transport, Directory: "/project", LastUsed: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB.Exec(`INSERT INTO conversation_events(session_id,seq,body) VALUES ('session-empty',1,'{"seq":1,"kind":"available_commands_update"}'),('session-used',1,'{"seq":1,"kind":"prompt","payload":{}}')`); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"session-empty": 1, "session-used": 0, "session-terminal": 0}
	for id, hidden := range want {
		if err := session.EndSession(ctx, store.DB, id, session.TerminalState{}); err != nil {
			t.Fatal(err)
		}
		var got int
		if err := store.Reader.QueryRow("SELECT hidden FROM sessions WHERE id=?", id).Scan(&got); err != nil || got != hidden {
			t.Fatalf("%s hidden=%d want %d err=%v", id, got, hidden, err)
		}
	}
}

func TestWorkerEndpointRequiresCanonicalSessionMetadata(t *testing.T) {
	home := t.TempDir()
	store := statetest.Open(t, home)
	g := &Gateway{store: store, auth: &gatewayAuth{store: store}, options: GatewayOptions{Home: home}}
	id := "session-test"
	directory := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(network.WorkerRecord{ID: id, Address: "127.0.0.1:1234", Secret: strings.Repeat("s", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := g.workerRecord(t.Context(), id); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("endpoint without metadata accepted: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/stop", nil)
	w := httptest.NewRecorder()
	if g.authorizeACPWriteByID(w, r, id) || w.Code != http.StatusNotFound {
		t.Fatalf("missing metadata allowed a write: status=%d", w.Code)
	}
	owner := session.BrowserActor(testBrowserEmail)
	info := session.SessionInfo{ID: id, Name: "Durable label", Transport: "acp", AgentSessionID: "native", StartedBy: &owner}
	info.NodeID, err = store.NodeID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.UpsertSession(t.Context(), store.DB, info); err != nil {
		t.Fatal(err)
	}
	record, err := g.workerRecord(t.Context(), id)
	if err != nil || !reflect.DeepEqual(record.Session, info) {
		t.Fatalf("gateway did not use canonical metadata: %+v %v", record.Session, err)
	}
}

func TestStoppingAStoppedWorkerSucceeds(t *testing.T) {
	if err := network.StopWorker(t.Context(), t.TempDir(), "session-test"); err != nil {
		t.Fatalf("a worker without an endpoint is already stopped: %v", err)
	}
}
