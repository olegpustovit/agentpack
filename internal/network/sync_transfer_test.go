package network

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/state"
)

func TestFailedStateTransferRemovesItsTemporaryFile(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, temporary)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	g := &Gateway{auth: auth}
	if _, err := g.newStateTransfer(ctx, "peer", 1, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled transfer: %v", err)
	}
	files, err := os.ReadDir(temporary)
	if err != nil || len(files) != 0 {
		t.Fatalf("failed transfer left private data behind: %d files, %v", len(files), err)
	}
}

func TestOversizedStateVersionTransfersAtomicallyInBoundedChunks(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	library := replicaStore(t)
	for _, peer := range []string{"trusted", "other"} {
		if err := library.EnrollPeer(peer); err != nil {
			t.Fatal(err)
		}
	}
	g := &Gateway{auth: auth, options: GatewayOptions{Library: LibraryOptions{Store: library}}}
	t.Cleanup(func() {
		if err := g.cleanStateTransfers(time.Time{}); err != nil {
			t.Error(err)
		}
	})
	mux := http.NewServeMux()
	g.registerStateSync(mux)
	target, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	sourceSite, err := auth.store.SiteID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var corrupt atomic.Bool
	var chunks atomic.Int32
	var expectedCursor atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "trusted"))
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, r)
		if recorder.Body.Len() > syncPageLimit {
			t.Errorf("response exceeded bound: %d", recorder.Body.Len())
		}
		if r.URL.Query().Has("transfer") && recorder.Code == http.StatusOK {
			chunks.Add(1)
			cursor, err := target.Cursor(r.Context(), sourceSite)
			if err != nil || cursor != expectedCursor.Load() {
				t.Errorf("partial transfer advanced cursor: %d %v", cursor, err)
			}
			var count int
			if err := target.Reader.QueryRow("SELECT count(*) FROM conversation_events WHERE session_id='oversized'").Scan(&count); err != nil || count != 0 {
				t.Errorf("partial transaction visible: %d %v", count, err)
			}
			if corrupt.Load() {
				var page stateChangePage
				if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
					t.Error(err)
				}
				page.Data[len(page.Data)-1] ^= 1
				recorder.Body.Reset()
				if err := json.NewEncoder(recorder.Body).Encode(page); err != nil {
					t.Error(err)
				}
			}
		}
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	if _, _, err := pullState(t.Context(), target, nil, server.Client(), endpoint); err != nil {
		t.Fatal(err)
	}
	cursor, err := target.Cursor(t.Context(), sourceSite)
	if err != nil {
		t.Fatal(err)
	}
	expectedCursor.Store(cursor)
	// One valid event can exceed the wire bound after nested JSON quoting, and its metadata is atomic with it.
	body := `{"seq":1,"type":"text","text":"` + strings.Repeat(`\"`, 1<<20) + `"}`
	if !json.Valid([]byte(body)) {
		t.Fatal("invalid event fixture")
	}
	if err := state.Transaction(t.Context(), auth.store.DB, func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO sessions(id,node_id,last_seq) VALUES ('oversized','source',1)"); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT INTO conversation_events(session_id,seq,body) VALUES ('oversized',1,?)", body)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	corrupt.Store(true)
	if _, _, err := pullState(t.Context(), target, nil, server.Client(), endpoint); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("corrupt transfer admitted: %v", err)
	}
	if got, _ := target.Cursor(t.Context(), sourceSite); got != cursor {
		t.Fatal("failed transfer changed cursor")
	}
	corrupt.Store(false)
	chunks.Store(0)
	if _, changed, err := pullState(t.Context(), target, nil, server.Client(), endpoint); err != nil || !changed {
		t.Fatalf("retry: changed=%v err=%v", changed, err)
	}
	if chunks.Load() < 2 {
		t.Fatalf("fixture did not span chunks: %d", chunks.Load())
	}
	var got string
	if err := target.Reader.QueryRow("SELECT body FROM conversation_events WHERE session_id='oversized'").Scan(&got); err != nil || got != body {
		t.Fatalf("event changed or lost: %v", err)
	}
	var seq int
	if err := target.Reader.QueryRow("SELECT last_seq FROM sessions WHERE id='oversized'").Scan(&seq); err != nil || seq != 1 {
		t.Fatalf("metadata lost: %d %v", seq, err)
	}
	if got, _ := target.Cursor(t.Context(), sourceSite); got <= cursor {
		t.Fatal("complete transfer did not advance cursor")
	}

	version, _ := auth.store.DBVersion(t.Context())
	transfer, err := g.newStateTransfer(t.Context(), "trusted", version, nil)
	if err != nil {
		t.Fatal(err)
	}
	originalHash := transfer.Hash
	if _, err := auth.store.DB.Exec("UPDATE conversation_events SET body='newer' WHERE session_id='oversized'"); err != nil {
		t.Fatal(err)
	}
	initial := stateChangePage{Schema: state.SchemaVersion(), SiteID: sourceSite, Next: version, Transfer: transfer}
	// A private transfer ID cannot be replayed by another approved peer.
	request := httptest.NewRequest("GET", "/api/sync/changes?schema="+strconv.Itoa(state.SchemaVersion())+"&transfer="+transfer.ID+"&offset=0", nil)
	other := httptest.NewRecorder()
	mux.ServeHTTP(other, request.WithContext(context.WithValue(request.Context(), replicaPeerKey{}, "other")))
	if other.Code != http.StatusNotFound {
		t.Fatalf("another peer read snapshot: %d", other.Code)
	}
	// Read the frozen transfer directly, after the source changed.
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "trusted")))
	}))
	defer direct.Close()
	directURL, _ := url.Parse(direct.URL)
	changes, err := fetchStateTransfer(t.Context(), direct.Client(), directURL, initial)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range changes {
		if change.Table == "conversation_events" && change.Column == "body" {
			var value string
			if err := json.Unmarshal(change.Value.Data, &value); err != nil {
				t.Fatal(err)
			}
			found = value == body
		}
	}
	if !found || transfer.Hash != originalHash {
		t.Fatal("snapshot changed during transfer")
	}
	if err := g.cleanStateTransfers(time.Now().Add(3 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchStateTransfer(t.Context(), direct.Client(), directURL, initial); err == nil || errors.Is(err, errSyncNeedsUpdate) {
		t.Fatalf("expired transfer: %v", err)
	}
}

func TestOversizedArchivedEditReachesMainThroughPeer(t *testing.T) {
	main, _ := identityGateway(t, t.TempDir(), nil)
	worker, _ := identityGateway(t, t.TempDir(), nil)
	relay, _ := identityGateway(t, t.TempDir(), nil)
	for id, g := range map[string]*Gateway{"main": main, "worker": worker, "relay": relay} {
		if err := g.auth.store.BindNode(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	archiveRoles(t, main, "main", "main")
	now := time.Now()
	const id = "session-large-archive-relay"
	archiveFixture(t, worker, id, now.Add(-8*24*time.Hour), 1, 1)
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("archive: %d %v", count, err)
	}
	syncArchiveFixture(t, main.auth.store, relay.auth.store)
	payload := json.RawMessage(`{"text":{"text":"` + strings.Repeat(`\\\"`, 700000) + `"}}`)
	if !json.Valid(payload) {
		t.Fatal("invalid large journal fixture")
	}
	if err := persistConversationChanges(t.Context(), worker.auth.store.DB, id, acp.LogChanges{LastSeq: 1, Entries: []acp.LogEntry{{Seq: 1, Kind: "agent_message_chunk", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	for _, link := range []struct{ source, target *Gateway }{{worker, relay}, {relay, main}} {
		link.source.options.Library.Store = replicaStore(t)
		if err := link.source.options.Library.Store.EnrollPeer("reader"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := link.source.cleanStateTransfers(time.Time{}); err != nil {
				t.Error(err)
			}
		})
		mux := http.NewServeMux()
		link.source.registerStateSync(mux)
		var chunks atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Has("transfer") {
				chunks.Add(1)
			}
			mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "reader")))
		}))
		endpoint, _ := url.Parse(server.URL)
		_, _, err := pullState(t.Context(), link.target.auth.store, nil, server.Client(), endpoint)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if chunks.Load() < 2 {
			t.Fatalf("large event or relayed journal did not use chunks: %d", chunks.Load())
		}
	}
	if err := main.drainArchiveEdits(t.Context(), main.tailnetTarget); err != nil {
		t.Fatal(err)
	}
	archive, err := state.ReadArchive(main.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var body string
	if err := archive.QueryRow("SELECT body FROM conversation_events WHERE session_id=? AND seq=1", id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var got acp.LogEntry
	if err := json.Unmarshal([]byte(body), &got); err != nil || string(got.Payload) != string(payload) {
		t.Fatalf("large archived edit lost: %v", err)
	}
}
