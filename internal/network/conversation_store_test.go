package network

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/acp/acptest"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
	"github.com/olegpustovit/agentpack/internal/state/statetest"
)

func TestConversationStoreFixtureRoundTripAndProgress(t *testing.T) {
	store := statetest.Open(t, t.TempDir())
	id := "session-fixture"
	if err := session.UpsertSession(t.Context(), store.DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	entries, err := loadEventLogFixtureEntries(filepath.Join("..", "..", "testdata", "acp", "full.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := NewConversationLog(t.Context(), store, id, func(err error) { t.Error(err) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		log.Append(entry.Kind, entry.TS, entry.Payload)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	stored, lastSeq, truncated, err := StoredConversationTail(t.Context(), store.Reader, id, 0)
	if err != nil || truncated || lastSeq != int64(len(entries)) || !reflect.DeepEqual(acp.ReduceLog(stored), acp.ReduceLog(entries)) {
		t.Fatalf("fixture changed: events=%d seq=%d truncated=%t err=%v", len(stored), lastSeq, truncated, err)
	}
	var lastEvent int64
	if err := store.Reader.QueryRow("SELECT last_event_at FROM sessions WHERE id=?", id).Scan(&lastEvent); err != nil || lastEvent == 0 {
		t.Fatalf("progress missing: timestamp=%d err=%v", lastEvent, err)
	}
	filtered, _, _, err := StoredConversationTail(t.Context(), store.Reader, id, 30)
	if err != nil || len(filtered) != 2 || filtered[0].Seq != 31 || filtered[1].Seq != 32 {
		t.Fatalf("since filter changed: %+v %v", filtered, err)
	}
}

func TestConversationStoreMergeAndResumeDoNotReuseSequence(t *testing.T) {
	store := statetest.Open(t, t.TempDir())
	id := "session-merged"
	if err := session.UpsertSession(t.Context(), store.DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	log := acp.NewEventLog(256)
	payload := json.RawMessage(`{"text":{"text":"` + strings.Repeat("x", 50) + `"}}`)
	log.Append("agent_message_chunk", 1, payload)
	log.Append("agent_message_chunk", 2, payload)
	if err := log.EnableSink(func(changes acp.LogChanges) error {
		return PersistConversationChangesSince(t.Context(), store.DB, id, changes, 0, false)
	}); err != nil {
		t.Fatal(err)
	}
	log.Append("agent_message_chunk", 3, payload)
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	entries, lastSeq, truncated, err := StoredConversationTail(t.Context(), store.Reader, id, 0)
	if err != nil || len(entries) != 1 || lastSeq != 3 || !truncated || !strings.Contains(string(entries[0].Payload), strings.Repeat("x", 150)) {
		t.Fatalf("merged row duplicated or incomplete: %+v seq=%d truncated=%t err=%v", entries, lastSeq, truncated, err)
	}
	resumed, err := NewConversationLog(t.Context(), store, id, func(err error) { t.Error(err) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := resumed.Append("turn_end", 4, json.RawMessage(`{}`)); got.Seq != 4 {
		t.Fatalf("resumed sequence reused: %d", got.Seq)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	entries, lastSeq, _, err = StoredConversationTail(t.Context(), store.Reader, id, 1)
	if err != nil || len(entries) != 1 || entries[0].Seq != 4 || lastSeq != 4 {
		t.Fatalf("resumed tail: %+v seq=%d err=%v", entries, lastSeq, err)
	}
}

func TestConversationStoreFailureKeepsWorkerAndRedactsDiagnostics(t *testing.T) {
	store := statetest.Open(t, t.TempDir())
	id := "session-failed"
	if err := session.UpsertSession(t.Context(), store.DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	queue := obs.NewQueue()
	ctx = obs.WithSink(ctx, queue, obs.BuildMetadata("test", LocalStateNode, "worker"))
	log, err := NewConversationLog(ctx, store, id, cancel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	log.Append("prompt", 1, json.RawMessage(`{"text":"SENTINEL-PRIVATE-CHAT"}`))
	if log.Close() == nil || context.Cause(ctx) != nil || len(queue.Events) != 1 {
		t.Fatalf("write failure hidden or fatal: cause=%v events=%d", context.Cause(ctx), len(queue.Events))
	}
	event := <-queue.Events
	data, err := json.Marshal(event)
	if err != nil || event.Level != "error" || event.SessionID != id || strings.Contains(string(data), "SENTINEL-PRIVATE-CHAT") {
		t.Fatalf("failed persistence diagnostic: %s %v", data, err)
	}
}

func TestConversationStoreTailIsBoundedByUTF8Bytes(t *testing.T) {
	store := statetest.Open(t, t.TempDir())
	id := "session-bounded"
	if err := session.UpsertSession(t.Context(), store.DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"text": strings.Repeat("é", 1<<18)})
	if err != nil {
		t.Fatal(err)
	}
	changes := acp.LogChanges{LastSeq: 8}
	for seq := int64(1); seq <= 8; seq++ {
		changes.Entries = append(changes.Entries, acp.LogEntry{Seq: seq, Kind: "tool_call_update", Payload: payload})
	}
	if err := PersistConversationChangesSince(t.Context(), store.DB, id, changes, 0, false); err != nil {
		t.Fatal(err)
	}
	entries, lastSeq, truncated, err := StoredConversationTail(t.Context(), store.Reader, id, 0)
	if err != nil || len(entries) != 7 || entries[0].Seq != 2 || lastSeq != 8 || !truncated {
		t.Fatalf("byte-bounded tail: entries=%d seq=%d truncated=%t err=%v", len(entries), lastSeq, truncated, err)
	}
	request := httptest.NewRequest(http.MethodGet, "/conversation", nil)
	page, _, more, err := StoredConversationRange(t.Context(), store.Reader, id, ConversationPage(request))
	if err != nil || len(page) != 1 || page[0].Seq != 8 || !more {
		t.Fatalf("first page lost the cursor for a large event: entries=%d more=%t err=%v", len(page), more, err)
	}
	log := acp.NewEventLog(MaxConversationSnapshotBytes)
	if err := log.Restore(entries, lastSeq, truncated); err != nil {
		t.Fatalf("stored tail cannot fit in memory: %v", err)
	}
}

func TestConversationStoreRejectedBatchRollsBackRowsAndProgress(t *testing.T) {
	store := statetest.Open(t, t.TempDir())
	id := "session-atomic"
	if err := session.UpsertSession(t.Context(), store.DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	first := acp.LogChanges{LastSeq: 1, Entries: []acp.LogEntry{{Seq: 1, Kind: "prompt", Payload: json.RawMessage(`{}`)}}}
	if err := PersistConversationChangesSince(t.Context(), store.DB, id, first, 0, false); err != nil {
		t.Fatal(err)
	}
	bad := acp.LogChanges{LastSeq: 2, Removed: []int64{1}, Entries: []acp.LogEntry{{Seq: 2, Kind: "prompt", Payload: json.RawMessage(`invalid`)}}}
	if err := PersistConversationChangesSince(t.Context(), store.DB, id, bad, 0, false); err == nil {
		t.Fatal("invalid batch accepted")
	}
	entries, lastSeq, _, err := StoredConversationTail(t.Context(), store.Reader, id, 0)
	if err != nil || lastSeq != 1 || len(entries) != 1 || entries[0].Seq != 1 {
		t.Fatalf("failed batch changed history: %+v seq=%d err=%v", entries, lastSeq, err)
	}
}

func TestConversationStoreTwoWritersAndSessionReader(t *testing.T) {
	home := t.TempDir()
	first := statetest.Open(t, home)
	second := statetest.Open(t, home)
	stores := []*state.Store{first, second}
	for i, id := range []string{"session-first", "session-second"} {
		if err := session.UpsertSession(t.Context(), stores[i].DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, stop := context.WithCancel(t.Context())
	readerDone := make(chan error, 1)
	readerStarted := make(chan struct{})
	go func() {
		close(readerStarted)
		for ctx.Err() == nil {
			if _, err := Sessions(ctx, home); err != nil && ctx.Err() == nil {
				readerDone <- err
				return
			}
		}
		readerDone <- nil
	}()
	<-readerStarted
	var writers sync.WaitGroup
	for i, id := range []string{"session-first", "session-second"} {
		writers.Go(func() {
			log, err := NewConversationLog(t.Context(), stores[i], id, func(err error) { t.Error(err) }, nil)
			if err != nil {
				t.Error(err)
				return
			}
			for seq := range 5000 {
				log.Append("agent_message_chunk", int64(seq), json.RawMessage(`{"text":{"text":"x"}}`))
			}
			if err := log.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	writers.Wait()
	stop()
	if err := <-readerDone; err != nil {
		t.Fatal(err)
	}
	// Streamed chunks are stored joined into a few rows; every character must be there once.
	var count, text int
	if err := first.Reader.QueryRow("SELECT count(*),sum(length(json_extract(body,'$.payload.text.text'))) FROM conversation_events").Scan(&count, &text); err != nil || text != 10000 || count > 100 {
		t.Fatalf("concurrent rows=%d text=%d err=%v", count, text, err)
	}
}

type historyLoadAgent struct {
	*authReplayAgent
	conn *sdkacp.AgentSideConnection
}

func (agent *historyLoadAgent) LoadSession(ctx context.Context, request sdkacp.LoadSessionRequest) (sdkacp.LoadSessionResponse, error) {
	for range 1000 {
		if err := agent.conn.SessionUpdate(ctx, sdkacp.SessionNotification{SessionId: request.SessionId, Update: sdkacp.UpdateAgentMessageText("old")}); err != nil {
			return sdkacp.LoadSessionResponse{}, err
		}
	}
	return sdkacp.LoadSessionResponse{}, nil
}

func TestConversationStoreNativeLoadDoesNotDuplicateHistory(t *testing.T) {
	store := statetest.Open(t, t.TempDir())
	id := "session-load"
	if err := session.UpsertSession(t.Context(), store.DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	log, err := NewConversationLog(t.Context(), store, id, func(err error) { t.Error(err) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	log.Append("agent_message_chunk", 1, json.RawMessage(`{"text":{"text":"old"}}`))
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	log, err = NewConversationLog(t.Context(), store, id, func(err error) { t.Error(err) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	log.SetReplaying(true)
	clientInput, agentOutput := io.Pipe()
	agentInput, clientOutput := io.Pipe()
	defer clientInput.Close()
	defer clientOutput.Close()
	defer agentInput.Close()
	defer agentOutput.Close()
	client := acp.NewHarnessClient(t.TempDir(), log, nil)
	connection := client.NewClientSideConnection(clientOutput, clientInput)
	agent := &historyLoadAgent{authReplayAgent: &authReplayAgent{ReplayAgent: acptest.NewReplayAgent(nil), agentCapabilities: sdkacp.AgentCapabilities{LoadSession: true}}}
	agent.conn = sdkacp.NewAgentSideConnection(agent, agentOutput, agentInput)
	agent.ReplayAgent.SetConnection(agent.conn)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := initializeACPSession(ctx, connection, client.Root, "native", "", "", chatTools{}, nil); err != nil {
		t.Fatal(err)
	}
	log.SetReplaying(false)
	if len(log.Snapshot()) != 1 {
		t.Fatal("native load duplicated the restored tail")
	}
	if err := agent.conn.SessionUpdate(ctx, sdkacp.SessionNotification{SessionId: "native", Update: sdkacp.UpdateAgentMessageText("new")}); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Prompt(ctx, sdkacp.PromptRequest{SessionId: "native", Prompt: []sdkacp.ContentBlock{sdkacp.TextBlock("go")}}); err != nil {
		t.Fatal(err)
	}
	if entries := log.Snapshot(); len(entries) != 2 || entries[1].Seq != 2 || !strings.Contains(string(entries[1].Payload), "new") {
		t.Fatalf("live events did not resume after replay: %+v", entries)
	}
}

func TestConversationStoreRestartAcceptsPromptAfterOldExit(t *testing.T) {
	store := statetest.Open(t, t.TempDir())
	id := "session-restart"
	if err := session.UpsertSession(t.Context(), store.DB, session.SessionInfo{ID: id, Transport: "acp", State: session.TerminalState{Running: true, ExitCode: -1}}); err != nil {
		t.Fatal(err)
	}
	if err := PersistConversationChangesSince(t.Context(), store.DB, id, acp.LogChanges{LastSeq: 1, Entries: []acp.LogEntry{{Seq: 1, Kind: "agent_exit", Payload: json.RawMessage(`{}`)}}}, 0, false); err != nil {
		t.Fatal(err)
	}
	if info, err := session.StoredSession(t.Context(), store.Reader, id); err != nil || info.State.Running {
		t.Fatalf("stored state after exit = %+v, err = %v", info.State, err)
	}
	log, err := NewConversationLog(t.Context(), store, id, func(err error) { t.Error(err) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	conversation, cleanup, err := startACPReplayAgent(t.Context(), WorkerLaunch{Directory: directory, Session: session.SessionInfo{Directory: directory}}, directory, nil, NewConversationSession(log, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if conversation.AgentExited() {
		t.Fatal("historical exit marked the restarted agent as dead")
	}
	if err := conversation.Prompt(t.Context(), acp.PromptInput{Text: "continue"}); err != nil {
		t.Fatal(err)
	}
	entries := log.Snapshot()
	var stored int64
	if err := store.Reader.QueryRow("SELECT last_seq FROM sessions WHERE id=?", id).Scan(&stored); err != nil || len(entries) == 0 || stored < entries[len(entries)-1].Seq {
		t.Fatalf("acknowledged an unflushed turn: stored=%d entries=%v %v", stored, entries, err)
	}
}

func TestConversationStoreStopKeepsFinalTurnEvents(t *testing.T) {
	store := statetest.Open(t, t.TempDir())
	id := "session-stop-turn"
	if err := session.UpsertSession(t.Context(), store.DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	log, err := NewConversationLog(t.Context(), store, id, func(err error) { t.Error(err) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := loadEventLogFixtureEntries(filepath.Join("..", "..", "testdata", "acp", "full.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	conversation, cleanup, err := startACPReplayAgent(t.Context(), WorkerLaunch{Directory: directory, Session: session.SessionInfo{Directory: directory}}, directory, fixture, NewConversationSession(log, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- conversation.Prompt(ctx, acp.PromptInput{Text: "go"}) }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for conversation.ConversationState() != "needs_you" {
		select {
		case err := <-done:
			t.Fatalf("prompt ended before permission: %v", err)
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	cleanup()
	if err := <-done; err == nil {
		t.Fatal("stopped prompt succeeded")
	}
	stored, _, _, err := StoredConversationTail(t.Context(), store.Reader, id, 0)
	if err != nil || !reflect.DeepEqual(stored, log.Snapshot()) {
		t.Fatalf("stop lost final turn events: stored=%d live=%d err=%v", len(stored), len(log.Snapshot()), err)
	}
}

func TestBypassingPermissionsFollowsTheAgentMode(t *testing.T) {
	log := acp.NewEventLog(0)
	session := NewConversationSession(log, nil)
	if session.BypassingPermissions() {
		t.Fatal("no mode reported yet")
	}
	log.Append("config_option_update", 1, json.RawMessage(`{"configOptions":[{"id":"mode","category":"mode","currentValue":"bypassPermissions"}]}`))
	if !session.BypassingPermissions() {
		t.Fatal("bypassPermissions must bypass")
	}
	log.Append("current_mode_update", 2, json.RawMessage(`{"currentModeId":"acceptEdits"}`))
	if session.BypassingPermissions() {
		t.Fatal("switching away from bypass must ask again")
	}
	log.Append("current_mode_update", 3, json.RawMessage(`{"currentModeId":"agent-full-access"}`))
	if !session.BypassingPermissions() {
		t.Fatal("Codex full access must bypass")
	}
}

// A reader whose position the replay tail already evicted gets the missing entries from state.db.
func TestConversationReaderBackfillsEvictedEntries(t *testing.T) {
	store := statetest.Open(t, t.TempDir())
	id := "session-gap"
	if err := session.UpsertSession(t.Context(), store.DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	log := acp.NewEventLog(512)
	if err := log.EnableSink(func(changes acp.LogChanges) error {
		return PersistConversationChangesSince(t.Context(), store.DB, id, changes, 0, false)
	}); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		log.Append("tool_call", int64(i), MustRawJSON(map[string]string{"toolCallId": strconv.Itoa(i), "title": strings.Repeat("x", 40)}))
	}
	conv := NewConversationSession(log, nil)
	if _, _, gap := conv.Attach(2); !gap {
		t.Fatal("memory still holds entry 3; make the entries larger")
	}
	if _, _, gap := (&WorkerHTTP{}).conversationSince(t.Context(), conv, 2); !gap {
		t.Fatal("without a store the hole must be reported")
	}
	worker := &WorkerHTTP{StateStore: store, SessionDir: filepath.Join(t.TempDir(), id)}
	entries, _, gap := worker.conversationSince(t.Context(), conv, 2)
	if gap || len(entries) != 18 || entries[0].Seq != 3 || entries[len(entries)-1].Seq != 20 {
		t.Fatalf("gap=%t, %d entries from %d", gap, len(entries), entries[0].Seq)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
}
