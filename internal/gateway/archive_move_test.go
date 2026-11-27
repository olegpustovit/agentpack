package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

func archiveRoles(t *testing.T, g *Gateway, main, archive string) {
	t.Helper()
	if _, err := g.store.DB.Exec(`INSERT INTO mesh_settings(key,value) VALUES ('main_node',?),('archive_node',?)
 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, main, archive); err != nil {
		t.Fatal(err)
	}
}

func archiveFixture(t *testing.T, g *Gateway, id string, at time.Time, last int64, held ...int64) {
	t.Helper()
	if _, err := g.store.DB.Exec(`INSERT INTO sessions(id,node_id,transport,harness,last_seq,last_event_at)
 VALUES (?,`+state.LocalNodeSQL+`,'acp','codex',?,?)`, id, last, at.UnixNano()); err != nil {
		t.Fatal(err)
	}
	for _, seq := range held {
		body, _ := json.Marshal(acp.LogEntry{Seq: seq, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"saved history"}}`)})
		if _, err := g.store.DB.Exec("INSERT INTO conversation_events(session_id,seq,body) VALUES (?,?,?)", id, seq, string(body)); err != nil {
			t.Fatal(err)
		}
	}
}

func syncArchiveFixture(t *testing.T, from, to *state.Store) {
	t.Helper()
	site, err := from.SiteID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	exclude, err := to.SiteID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	since, err := to.Replicator().Cursor(t.Context(), site)
	if err != nil {
		t.Fatal(err)
	}
	for {
		changes, next, err := from.Replicator().Changes(t.Context(), since, exclude, 4<<20)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := to.Replicator().ApplyPage(t.Context(), changes, site, next); err != nil {
			t.Fatal(err)
		}
		if next == since {
			return
		}
		since = next
	}
}

func TestArchiveMovesOnlyCompleteOldChatsAndReplicatesPruning(t *testing.T) {
	main, _ := identityGateway(t, t.TempDir(), nil)
	peer, _ := identityGateway(t, t.TempDir(), nil)
	if err := main.auth.store.BindNode(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, main, "main", "main")
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	archiveFixture(t, main, "session-complete", now.Add(-8*24*time.Hour), 3, 1, 3)
	if _, err := main.store.DB.Exec("UPDATE sessions SET ended_at=?,closed_at=? WHERE id='session-complete'", now.Add(-8*24*time.Hour).UnixNano(), now.Add(-7*24*time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	archiveFixture(t, main, "session-incomplete", now.Add(-8*24*time.Hour), 3, 1)
	archiveFixture(t, main, "session-young", now.Add(-6*24*time.Hour), 1, 1)
	archiveFixture(t, main, "session-image", now.Add(-8*24*time.Hour), 1, 1)
	archiveFixture(t, main, "session-working", now.Add(-8*24*time.Hour), 1, 1)
	archiveFixture(t, main, "session-needs-you", now.Add(-8*24*time.Hour), 1, 1)
	if _, err := main.auth.store.DB.Exec("UPDATE sessions SET conversation_state='working' WHERE id='session-working'; UPDATE sessions SET conversation_state='needs_you' WHERE id='session-needs-you'"); err != nil {
		t.Fatal(err)
	}
	if _, err := main.auth.store.DB.Exec(`UPDATE conversation_events SET body='{"seq":1,"kind":"user_prompt","payload":{"images":[{"blob":"sha256:abc"}]}}' WHERE session_id='session-image'`); err != nil {
		t.Fatal(err)
	}
	syncArchiveFixture(t, main.auth.store, peer.auth.store)
	count, err := main.archivePass(t.Context(), now)
	if err != nil || count != 1 {
		t.Fatalf("archive pass: %d %v", count, err)
	}
	syncArchiveFixture(t, main.auth.store, peer.auth.store)
	for _, store := range []*state.Store{main.auth.store, peer.auth.store} {
		for _, test := range []struct {
			id       string
			rows     int
			archived bool
		}{{"session-complete", 0, true}, {"session-incomplete", 1, false}, {"session-young", 1, false}, {"session-image", 1, false}, {"session-working", 1, false}, {"session-needs-you", 1, false}} {
			var rows int
			var archived, closed bool
			if err := store.Reader.QueryRow("SELECT count(*) FROM conversation_events WHERE session_id=?", test.id).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if err := store.Reader.QueryRow("SELECT archived_at>0,closed_at>0 FROM sessions WHERE id=?", test.id).Scan(&archived, &closed); err != nil {
				t.Fatal(err)
			}
			if rows != test.rows || archived != test.archived || closed != (test.id == "session-complete") {
				t.Fatalf("%s rows=%d archived=%v closed=%v", test.id, rows, archived, closed)
			}
		}
	}
	archive, err := state.ReadArchive(main.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var rows int
	if err := archive.QueryRow("SELECT count(*) FROM conversation_events WHERE session_id='session-complete'").Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("durable archive: %d %v", rows, err)
	}
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 0 {
		t.Fatalf("repeat pass: %d %v", count, err)
	}
}

func TestArchiveRequiresBothRolesAndRechecksBeforeEachSession(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	if err := g.store.BindNode(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	archiveFixture(t, g, "session-first", now.Add(-8*24*time.Hour), 1, 1)
	archiveFixture(t, g, "session-second", now.Add(-8*24*time.Hour), 1, 1)
	for _, roles := range [][2]string{{"", ""}, {"other", "other"}, {"main", "other"}, {"other", "main"}} {
		archiveRoles(t, g, roles[0], roles[1])
		if count, err := g.archivePass(t.Context(), now); err != nil || count != 0 {
			t.Fatalf("roles %v: %d %v", roles, count, err)
		}
		if _, err := os.Stat(filepath.Join(g.options.Home, "archive.db")); !os.IsNotExist(err) {
			t.Fatalf("non-main opened archive: %v", err)
		}
	}
	archiveRoles(t, g, "main", "main")
	if _, err := g.store.DB.Exec(`CREATE TRIGGER switch_main AFTER UPDATE OF archived_at ON sessions
 WHEN new.id='session-first' BEGIN UPDATE mesh_settings SET value='other' WHERE key='main_node'; END`); err != nil {
		t.Fatal(err)
	}
	if count, err := g.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("role switched during pass: %d %v", count, err)
	}
	var archived bool
	if err := g.store.Reader.QueryRow("SELECT archived_at>0 FROM sessions WHERE id='session-second'").Scan(&archived); err != nil || archived {
		t.Fatalf("second session moved after role switch: %v %v", archived, err)
	}
}

func TestArchiveCrashAfterCopyRetriesWithoutLosingChangedEvents(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	local, err := g.store.NodeID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, g, local, local)
	now := time.Now()
	archiveFixture(t, g, "session-crash", now.Add(-8*24*time.Hour), 1, 1)
	if _, err := g.store.DB.Exec(`CREATE TRIGGER crash_after_copy BEFORE DELETE ON conversation_events BEGIN SELECT RAISE(ABORT,'simulated crash'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := g.archivePass(t.Context(), now); err == nil {
		t.Fatal("crash did not stop pruning")
	}
	archive, err := state.ReadArchive(g.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := archive.QueryRow("SELECT count(*) FROM conversation_events").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("copy not committed before prune: %d %v", rows, err)
	}
	archive.Close()
	if err := g.store.Reader.QueryRow("SELECT count(*) FROM conversation_events").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("crash removed replicated history: %d %v", rows, err)
	}
	if _, err := g.store.DB.Exec(`DROP TRIGGER crash_after_copy;
 UPDATE conversation_events SET body='{"seq":1,"kind":"message","payload":{"text":"new coalesced body"}}'`); err != nil {
		t.Fatal(err)
	}
	if count, err := g.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("retry: %d %v", count, err)
	}
	archive, err = state.ReadArchive(g.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var body string
	if err := archive.QueryRow("SELECT body FROM conversation_events WHERE session_id='session-crash'").Scan(&body); err != nil || !strings.Contains(body, "new coalesced body") {
		t.Fatalf("retry preserved stale copy: %s %v", body, err)
	}
}

func TestArchivedConversationReadsThroughMainAndHandlesUnavailableArchive(t *testing.T) {
	main, _ := identityGateway(t, t.TempDir(), nil)
	peer, _ := identityGateway(t, t.TempDir(), nil)
	if err := main.auth.store.BindNode(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, main, "main", "main")
	if _, err := main.auth.store.DB.Exec("INSERT INTO mesh_nodes(node_id,name) VALUES ('main','Archive computer')"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	archiveFixture(t, main, "session-archived-read", now.Add(-8*24*time.Hour), 2, 1, 2)
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("archive fixture: %d %v", count, err)
	}
	syncArchiveFixture(t, main.auth.store, peer.auth.store)
	peer.options.Library.Store = replicaStore(t)
	if err := peer.options.Library.Store.EnrollPeer("main"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}/conversation", main.ServeConversation)
	server := httptest.NewServer(mux)
	defer server.Close()
	target, _ := url.Parse(server.URL)
	targetFor := func(context.Context, string) (*url.URL, error) { return target, nil }
	info, err := session.StoredSession(t.Context(), peer.auth.store.Reader, "session-archived-read")
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "?since=1", "?stream=1"} {
		r := httptest.NewRequest("GET", "/api/sessions/"+info.ID+"/conversation"+suffix, nil)
		w := httptest.NewRecorder()
		peer.serveArchivedConversation(w, r, info, targetFor)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"seq":2`) || w.Header().Get("X-Agentpack-Archive-Main") != "Archive computer" {
			t.Fatalf("proxy %s: %d %s", suffix, w.Code, w.Body.String())
		}
		if suffix == "?since=1" && strings.Contains(w.Body.String(), `"seq":1`) {
			t.Fatal("archive ignored since")
		}
	}
	server.Close()
	r := httptest.NewRequest("GET", "/api/sessions/"+info.ID+"/conversation", nil)
	w := httptest.NewRecorder()
	peer.serveArchivedConversation(w, r, info, targetFor)
	if w.Code != 503 || strings.TrimSpace(w.Body.String()) != "archive unavailable" {
		t.Fatalf("main offline: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/api/sessions/"+info.ID+"/conversation?node=main", nil)
	w = httptest.NewRecorder()
	r.Pattern = "GET /api/sessions/{id}/conversation"
	r.SetPathValue("id", info.ID)
	_, handled, err := peer.routeMeshRequest(w, r)
	if err != nil || !handled || w.Code != 503 || strings.TrimSpace(w.Body.String()) != "archive unavailable" {
		t.Fatalf("mesh routing bypassed archive: handled=%v %d %s %v", handled, w.Code, w.Body.String(), err)
	}
	if err := os.Remove(filepath.Join(main.options.Home, "archive.db")); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r.SetPathValue("id", info.ID)
	main.ServeConversation(w, r)
	if w.Code != 503 || strings.TrimSpace(w.Body.String()) != "archive unavailable" {
		t.Fatalf("missing archive: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(main.options.Home, "archive.db")); !os.IsNotExist(err) {
		t.Fatalf("read created an empty archive: %v", err)
	}
}

func TestArchiveCountMismatchKeepsReplicatedHistory(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	local, err := g.store.NodeID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, g, local, local)
	now := time.Now()
	archiveFixture(t, g, "session-count", now.Add(-8*24*time.Hour), 1, 1)
	archive, err := state.OpenArchive(g.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Exec(`CREATE TRIGGER reject_copy BEFORE INSERT ON conversation_events BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	archive.Close()
	if _, err := g.archivePass(t.Context(), now); err == nil || !strings.Contains(err.Error(), "row count") {
		t.Fatalf("unverified copy accepted: %v", err)
	}
	var rows int
	var archived bool
	if err := g.store.Reader.QueryRow("SELECT count(*) FROM conversation_events").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("lost history: %d %v", rows, err)
	}
	if err := g.store.Reader.QueryRow("SELECT archived_at>0 FROM sessions WHERE id='session-count'").Scan(&archived); err != nil || archived {
		t.Fatalf("marked an incomplete copy: %v %v", archived, err)
	}
}

func TestArchiveLoopUsesInjectedClockAndInterval(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	local, err := g.store.NodeID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, g, local, local)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	archiveFixture(t, g, "session-clock", now.Add(-6*24*time.Hour), 1, 1)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	started := make(chan struct{}, 1)
	go func() {
		defer close(done)
		g.runArchive(ctx, 2*time.Millisecond, func() time.Time {
			select {
			case started <- struct{}{}:
			default:
			}
			return time.Unix(0, clock.Load())
		})
	}()
	defer func() { cancel(); <-done }()
	<-started
	clock.Store(now.Add(2 * 24 * time.Hour).UnixNano())
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for {
		var archived int64
		if err := g.store.Reader.QueryRow("SELECT archived_at FROM sessions WHERE id='session-clock'").Scan(&archived); err != nil {
			t.Fatal(err)
		}
		if archived != 0 {
			if archived != clock.Load() {
				t.Fatalf("used wall clock: %d", archived)
			}
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatal("archive loop ignored the interval or clock")
		}
	}
}

func TestArchivePreservesFlushBeforeRemoteReceivesMarker(t *testing.T) {
	main, _ := identityGateway(t, t.TempDir(), nil)
	worker, _ := identityGateway(t, t.TempDir(), nil)
	if err := main.auth.store.BindNode(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	if err := worker.auth.store.BindNode(t.Context(), "worker"); err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, main, "main", "main")
	now := time.Now()
	archiveFixture(t, worker, "session-late-flush", now.Add(-8*24*time.Hour), 1, 1)
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("initial archive: %d %v", count, err)
	}
	// The worker has not pulled Main's marker yet, so an in-flight flush still succeeds.
	changes := acp.LogChanges{LastSeq: 2, Entries: []acp.LogEntry{
		{Seq: 1, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"late coalesced text"}}`)},
		{Seq: 2, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"late final event"}}`)},
	}}
	if err := network.PersistConversationChangesSince(t.Context(), worker.auth.store.DB, "session-late-flush", changes, 0, false); err != nil {
		t.Fatal(err)
	}
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	r := httptest.NewRequest("GET", "/api/sessions/session-late-flush/conversation", nil)
	r.SetPathValue("id", "session-late-flush")
	w := httptest.NewRecorder()
	main.ServeConversation(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "late coalesced text") || !strings.Contains(w.Body.String(), "late final event") {
		t.Fatalf("late events missing: %d %s", w.Code, w.Body.String())
	}
	syncArchiveFixture(t, main.auth.store, worker.auth.store)
	for _, store := range []*state.Store{main.auth.store, worker.auth.store} {
		var count int
		var archived bool
		if err := store.Reader.QueryRow("SELECT count(*) FROM conversation_events WHERE session_id='session-late-flush'").Scan(&count); err != nil || count != 0 {
			t.Fatalf("late rows not pruned: %d %v", count, err)
		}
		if err := store.Reader.QueryRow("SELECT archived_at>0 FROM sessions WHERE id='session-late-flush'").Scan(&archived); err != nil || !archived {
			t.Fatalf("late flush restored archived chat: %v %v", archived, err)
		}
	}
	if err := network.PersistConversationChangesSince(t.Context(), worker.auth.store.DB, "session-late-flush", changes, 0, false); err == nil {
		t.Fatal("worker wrote after receiving archive marker")
	}
}

func TestArchiveLateCoalescingStaleGossipAndMainHandover(t *testing.T) {
	main, _ := identityGateway(t, t.TempDir(), nil)
	worker, _ := identityGateway(t, t.TempDir(), nil)
	stale, _ := identityGateway(t, t.TempDir(), nil)
	if err := main.auth.store.BindNode(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	if err := worker.auth.store.BindNode(t.Context(), "worker"); err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, main, "main", "main")
	now := time.Now()
	archiveFixture(t, worker, "session-late-merge", now.Add(-8*24*time.Hour), 2, 1, 2)
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	syncArchiveFixture(t, worker.auth.store, stale.auth.store)
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("initial archive: %d %v", count, err)
	}
	flush := func(last int64, removed int64, text string) {
		t.Helper()
		merged, _ := json.Marshal(map[string]any{"text": map[string]string{"text": text}})
		if err := network.PersistConversationChangesSince(t.Context(), worker.auth.store.DB, "session-late-merge", acp.LogChanges{
			LastSeq: last, Removed: []int64{removed}, Entries: []acp.LogEntry{
				{Seq: 1, Kind: "agent_message_chunk", Payload: merged},
				{Seq: last, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"final event"}}`)},
			},
		}, 0, false); err != nil {
			t.Fatal(err)
		}
	}
	read := func(g *Gateway, want string, absent int64) {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/sessions/session-late-merge/conversation", nil)
		r.SetPathValue("id", "session-late-merge")
		w := httptest.NewRecorder()
		g.ServeConversation(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) || strings.Contains(w.Body.String(), `"seq":`+strconv.FormatInt(absent, 10)) {
			t.Fatalf("archive contents: %d %s", w.Code, w.Body.String())
		}
	}
	flush(3, 2, "merged before marker")
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	read(main, "merged before marker", 2)
	syncArchiveFixture(t, stale.auth.store, main.auth.store)
	read(main, "merged before marker", 2)
	next, _ := identityGateway(t, t.TempDir(), nil)
	if err := next.auth.store.BindNode(t.Context(), "next"); err != nil {
		t.Fatal(err)
	}
	syncArchiveFixture(t, main.auth.store, next.auth.store)
	data, err := os.ReadFile(filepath.Join(main.options.Home, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(next.options.Home, "archive.db"), data, 0600); err != nil {
		t.Fatal(err)
	}
	roles, err := next.auth.store.Main(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := next.auth.store.SetMain(t.Context(), roles, next.options.Home); err != nil {
		t.Fatal(err)
	}
	if err := next.auth.store.ArchiveReady(t.Context(), next.options.Home, false); err != nil {
		t.Fatal(err)
	}
	flush(4, 3, "merged after main handover")
	syncArchiveFixture(t, worker.auth.store, next.auth.store)
	read(next, "merged after main handover", 3)
	syncArchiveFixture(t, stale.auth.store, next.auth.store)
	read(next, "merged after main handover", 2)
}

func TestArchivePendingHandoverRetainsUnacknowledgedWorkerEdit(t *testing.T) {
	main, _ := identityGateway(t, t.TempDir(), nil)
	worker, _ := identityGateway(t, t.TempDir(), nil)
	gossip, _ := identityGateway(t, t.TempDir(), nil)
	next, _ := identityGateway(t, t.TempDir(), nil)
	for node, g := range map[string]*Gateway{"main": main, "worker": worker, "next": next} {
		if err := g.store.BindNode(t.Context(), node); err != nil {
			t.Fatal(err)
		}
	}
	archiveRoles(t, main, "main", "main")
	now := time.Now()
	archiveFixture(t, worker, "session-pending-copy", now.Add(-8*24*time.Hour), 1, 1)
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("archive: %d %v", count, err)
	}
	syncArchiveFixture(t, main.auth.store, gossip.auth.store)
	syncArchiveFixture(t, main.auth.store, next.auth.store)
	data, err := os.ReadFile(filepath.Join(main.options.Home, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(next.options.Home, "archive.db"), data, 0600); err != nil {
		t.Fatal(err)
	}
	roles, err := next.auth.store.Main(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := next.auth.store.SetMain(t.Context(), roles, next.options.Home); err != nil {
		t.Fatal(err)
	}
	changes := acp.LogChanges{LastSeq: 2, Entries: []acp.LogEntry{
		{Seq: 1, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"late while copying"}}`)},
		{Seq: 2, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"new final event"}}`)},
	}}
	if err := network.PersistConversationChangesSince(t.Context(), worker.auth.store.DB, "session-pending-copy", changes, 0, false); err != nil {
		t.Fatal(err)
	}
	applyOne := func(from, to *state.Store) error {
		t.Helper()
		site, err := from.SiteID(t.Context())
		if err != nil {
			return err
		}
		exclude, err := to.SiteID(t.Context())
		if err != nil {
			return err
		}
		since, err := to.Replicator().Cursor(t.Context(), site)
		if err != nil {
			return err
		}
		rows, next, err := from.Replicator().Changes(t.Context(), since, exclude, 4<<20)
		if err != nil {
			return err
		}
		_, err = to.Replicator().ApplyPage(t.Context(), rows, site, next)
		return err
	}
	if err := applyOne(worker.auth.store, next.auth.store); err != nil {
		t.Fatalf("unready Main did not retain late edit: %v", err)
	}
	version, err := worker.auth.store.DBVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := applyOne(main.auth.store, worker.auth.store); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := worker.auth.store.Reader.QueryRow("SELECT body FROM archive_edits WHERE json_extract(edit,'$.session')='session-pending-copy'").Scan(&body); err != nil || !strings.Contains(body, "late while copying") {
		t.Fatalf("worker edit lost: %s %v", body, err)
	}
	workerSite, err := worker.auth.store.SiteID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cursor, err := next.auth.store.Replicator().Cursor(t.Context(), workerSite); err != nil {
		t.Fatal(err)
	} else {
		if cursor != version {
			t.Fatal("durably backed up archive page did not advance the cursor")
		}
	}
	if err := next.auth.store.ArchiveReady(t.Context(), next.options.Home, false); err != nil {
		t.Fatal(err)
	}
	syncArchiveFixture(t, worker.auth.store, next.auth.store)
	read := func() {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/sessions/session-pending-copy/conversation", nil)
		r.SetPathValue("id", "session-pending-copy")
		w := httptest.NewRecorder()
		next.ServeConversation(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "late while copying") || !strings.Contains(w.Body.String(), "new final event") {
			t.Fatalf("handover dropped history: %d %s", w.Code, w.Body.String())
		}
	}
	read()
	syncArchiveFixture(t, gossip.auth.store, next.auth.store)
	read()
	syncArchiveFixture(t, next.auth.store, worker.auth.store)
	var count int
	if err := worker.auth.store.Reader.QueryRow("SELECT count(*) FROM conversation_events WHERE session_id='session-pending-copy'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("committed archive did not release worker history: %d %v", count, err)
	}
}

func TestArchiveMarkerWaitsForActiveTurnAndCompleteFreshTail(t *testing.T) {
	main, _ := identityGateway(t, t.TempDir(), nil)
	worker, _ := identityGateway(t, t.TempDir(), nil)
	if err := main.auth.store.BindNode(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	if err := worker.auth.store.BindNode(t.Context(), "worker"); err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, main, "main", "main")
	now := time.Now()
	archiveFixture(t, worker, "session-fresh-tail", now.Add(-8*24*time.Hour), 1, 1)
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("archive: %d %v", count, err)
	}
	apply := func() error {
		site, err := worker.auth.store.SiteID(t.Context())
		if err != nil {
			return err
		}
		changes, _, err := main.auth.store.Replicator().Changes(t.Context(), 0, site, 4<<20)
		if err != nil {
			return err
		}
		return worker.auth.store.Replicator().Apply(t.Context(), changes)
	}
	if err := network.PatchWorkerConversationState(t.Context(), worker.auth.store.DB, "session-fresh-tail", "working"); err != nil {
		t.Fatal(err)
	}
	if err := apply(); !errors.Is(err, state.ErrArchivePending) {
		t.Fatalf("pruned an active turn: %v", err)
	}
	if err := network.PersistConversationChangesSince(t.Context(), worker.auth.store.DB, "session-fresh-tail", acp.LogChanges{LastSeq: 2, Entries: []acp.LogEntry{{Seq: 2, Kind: "turn_end", Payload: json.RawMessage(`{}`)}}}, 0, false); err != nil {
		t.Fatal(err)
	}
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 0 {
		t.Fatalf("finalized an active late turn: %d %v", count, err)
	}
	if err := network.PatchWorkerConversationState(t.Context(), worker.auth.store.DB, "session-fresh-tail", "idle"); err != nil {
		t.Fatal(err)
	}
	if err := apply(); err != nil {
		t.Fatalf("idle tail did not survive the archive marker: %v", err)
	}
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("complete tail: %d %v", count, err)
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := worker.auth.store.Reader.QueryRow("SELECT count(*) FROM conversation_events WHERE session_id='session-fresh-tail'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("tail not released: %d %v", count, err)
	}
}

func TestNewMainCapturesItsOwnNewerBodyBeforeOldPruning(t *testing.T) {
	main, _ := identityGateway(t, t.TempDir(), nil)
	owner, _ := identityGateway(t, t.TempDir(), nil)
	if err := main.auth.store.BindNode(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	if err := owner.auth.store.BindNode(t.Context(), "owner"); err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, main, "main", "main")
	archiveRoles(t, owner, "main", "main")
	now := time.Now()
	archiveFixture(t, owner, "session-new-main-owned", now.Add(-8*24*time.Hour), 1, 1)
	syncArchiveFixture(t, owner.auth.store, main.auth.store)
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("archive: %d %v", count, err)
	}
	if err := network.PersistConversationChangesSince(t.Context(), owner.auth.store.DB, "session-new-main-owned", acp.LogChanges{LastSeq: 2, Entries: []acp.LogEntry{
		{Seq: 1, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"new owner's coalesced text"}}`)},
		{Seq: 2, Kind: "turn_end", Payload: json.RawMessage(`{}`)},
	}}, 0, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(main.options.Home, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner.options.Home, "archive.db"), data, 0600); err != nil {
		t.Fatal(err)
	}
	roles, err := owner.auth.store.Main(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.auth.store.SetMain(t.Context(), roles, owner.options.Home); err != nil {
		t.Fatal(err)
	}
	if err := owner.auth.store.ArchiveReady(t.Context(), owner.options.Home, false); err != nil {
		t.Fatal(err)
	}
	syncArchiveFixture(t, main.auth.store, owner.auth.store)
	r := httptest.NewRequest("GET", "/api/sessions/session-new-main-owned/conversation", nil)
	r.SetPathValue("id", "session-new-main-owned")
	w := httptest.NewRecorder()
	owner.ServeConversation(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "new owner's coalesced text") || !strings.Contains(w.Body.String(), `"seq":2`) {
		t.Fatalf("new Main lost its own body: %d %s", w.Code, w.Body.String())
	}
}

func TestArchivedEditsRelayThroughIntermediatePeer(t *testing.T) {
	main, _ := identityGateway(t, t.TempDir(), nil)
	worker, _ := identityGateway(t, t.TempDir(), nil)
	relay, _ := identityGateway(t, t.TempDir(), nil)
	if err := main.auth.store.BindNode(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	if err := worker.auth.store.BindNode(t.Context(), "worker"); err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, main, "main", "main")
	now := time.Now()
	const id = "session-archive-relay"
	archiveFixture(t, worker, id, now.Add(-8*24*time.Hour), 2, 1, 2)
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("archive: %d %v", count, err)
	}
	syncArchiveFixture(t, main.auth.store, relay.auth.store)
	if err := network.PersistConversationChangesSince(t.Context(), worker.auth.store.DB, id, acp.LogChanges{
		LastSeq: 3, Removed: []int64{2}, Entries: []acp.LogEntry{
			{Seq: 1, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"relayed coalesced edit"}}`)},
			{Seq: 3, Kind: "turn_end", Payload: json.RawMessage(`{}`)},
		},
	}, 0, false); err != nil {
		t.Fatal(err)
	}
	syncArchiveFixture(t, worker.auth.store, relay.auth.store)
	var edits int
	if err := relay.auth.store.Reader.QueryRow("SELECT count(*) FROM archive_edits").Scan(&edits); err != nil || edits != 2 {
		t.Fatalf("relay did not preserve edit and delete: %d %v", edits, err)
	}
	late, _, err := worker.auth.store.Replicator().Changes(t.Context(), 0, nil, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.auth.store.Close(); err != nil {
		t.Fatal(err)
	}
	syncArchiveFixture(t, relay.auth.store, main.auth.store)
	if _, err := main.auth.store.DB.Exec("CREATE TRIGGER crash_archive_ack BEFORE DELETE ON archive_edits BEGIN SELECT RAISE(ABORT,'crash after archive commit'); END"); err != nil {
		t.Fatal(err)
	}
	if err := main.drainArchiveEdits(t.Context(), main.tailnetTarget); err == nil {
		t.Fatal("failed acknowledgement was accepted")
	}
	if err := main.auth.store.Reader.QueryRow("SELECT count(*) FROM archive_edits").Scan(&edits); err != nil || edits != 2 {
		t.Fatalf("failed acknowledgement lost journal: %d %v", edits, err)
	}
	if _, err := main.auth.store.DB.Exec("DROP TRIGGER crash_archive_ack"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/sessions/"+id+"/conversation", nil)
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	main.ServeConversation(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "relayed coalesced edit") || !strings.Contains(w.Body.String(), `"seq":3`) || strings.Contains(w.Body.String(), `"seq":2`) {
		t.Fatalf("relay lost edits: %d %s", w.Code, w.Body.String())
	}
	syncArchiveFixture(t, main.auth.store, relay.auth.store)
	for _, table := range []string{"archive_edits", "conversation_events"} {
		var count int
		if err := relay.auth.store.Reader.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("relay retained acknowledged %s: %d %v", table, count, err)
		}
	}
	// Replaying an already acknowledged loser must not resurrect the journal.
	if err := relay.auth.store.Replicator().Apply(t.Context(), late); err != nil {
		t.Fatal(err)
	}
	if err := relay.auth.store.Reader.QueryRow("SELECT count(*) FROM archive_edits").Scan(&edits); err != nil || edits != 0 {
		t.Fatalf("replay resurrected a journal row: %d %v", edits, err)
	}
	syncArchiveFixture(t, relay.auth.store, main.auth.store)
	if err := main.auth.store.Reader.QueryRow("SELECT count(*) FROM archive_edits").Scan(&edits); err != nil || edits != 0 {
		t.Fatalf("acknowledged edits returned: %d %v", edits, err)
	}
}

func TestArchiveCopyAllowsWorkerWriteAndRetainsEditedLiveRows(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	ctx := t.Context()
	local, err := g.store.NodeID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, g, local, local)
	now := time.Now()
	const id = "session-concurrent-edit"
	archiveFixture(t, g, id, now.Add(-8*24*time.Hour), 1, 1)
	archive, err := state.OpenArchive(g.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	conn, err := archive.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(raw any) error {
		return raw.(*sqlite3.SQLiteConn).RegisterFunc("edit_live_history", func() (int, error) {
			writeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			_, err := g.store.DB.ExecContext(writeCtx, `UPDATE conversation_events
 SET body='{"seq":1,"kind":"message","payload":{"text":"new body"}}' WHERE session_id=? AND seq=1`, id)
			return 1, err
		}, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TEMP TRIGGER edit_during_copy BEFORE INSERT ON conversation_events
 BEGIN SELECT edit_live_history(); END`); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if moved, err := g.archiveSession(ctx, archive, id, now); err != nil || moved {
		t.Fatalf("archive moved changed history: %v %v", moved, err)
	}
	var live string
	if err := g.store.Reader.QueryRowContext(ctx, "SELECT body FROM conversation_events WHERE session_id=?", id).Scan(&live); err != nil || !strings.Contains(live, "new body") {
		t.Fatalf("worker change lost: %q %v", live, err)
	}
}

func TestArchiveMarkerAppliesToEndedSessionWithStaleWorkingState(t *testing.T) {
	main, _ := identityGateway(t, t.TempDir(), nil)
	worker, _ := identityGateway(t, t.TempDir(), nil)
	local, err := main.auth.store.NodeID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, main, local, local)
	now := time.Now()
	const id = "session-ended-working"
	archiveFixture(t, worker, id, now.Add(-8*24*time.Hour), 1, 1)
	if _, err := worker.auth.store.DB.Exec("UPDATE sessions SET ended_at=1,conversation_state='working' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("archive: %d %v", count, err)
	}
	syncArchiveFixture(t, main.auth.store, worker.auth.store)
	var archived bool
	if err := worker.auth.store.Reader.QueryRow("SELECT archived_at>0 FROM sessions WHERE id=?", id).Scan(&archived); err != nil || !archived {
		t.Fatalf("worker refused the archive marker: %v %v", archived, err)
	}
}

// One wake after a changing pull runs the hourly pass early, and repeated wakes coalesce.
func TestArchivePassWakesAfterAChangingPull(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	g.archiveWake = make(chan struct{}, 1)
	passes := make(chan struct{}, 8)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.runArchive(ctx, time.Hour, func() time.Time { passes <- struct{}{}; return time.Now() })
	}()
	defer func() { cancel(); <-done }()
	<-passes
	g.wakeArchive()
	g.wakeArchive() // never blocks a puller
	<-passes
}

func TestArchiveGuardDefersOnlyTheLiveSessionAndReplaysAfterRestart(t *testing.T) {
	main, _ := identityGateway(t, t.TempDir(), nil)
	worker, _ := identityGateway(t, t.TempDir(), nil)
	if err := main.auth.store.BindNode(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	if err := worker.auth.store.BindNode(t.Context(), "worker"); err != nil {
		t.Fatal(err)
	}
	archiveRoles(t, main, "main", "main")
	now := time.Now()
	const id = "session-guarded"
	archiveFixture(t, worker, id, now.Add(-8*24*time.Hour), 1, 1)
	syncArchiveFixture(t, worker.auth.store, main.auth.store)
	if count, err := main.archivePass(t.Context(), now); err != nil || count != 1 {
		t.Fatalf("archive: %d %v", count, err)
	}
	if err := network.PatchWorkerConversationState(t.Context(), worker.auth.store.DB, id, "working"); err != nil {
		t.Fatal(err)
	}
	// A later version on the same chat must wait behind the archive marker; other chats apply.
	if _, err := main.store.DB.Exec("UPDATE sessions SET title='renamed on main' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	archiveFixture(t, main, "session-unrelated", now, 1, 1)
	syncArchiveFixture(t, main.auth.store, worker.auth.store)
	mainSite, err := main.auth.store.SiteID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	high, err := main.auth.store.DBVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cursor, err := worker.auth.store.Replicator().Cursor(t.Context(), mainSite); err != nil || cursor != high {
		t.Fatalf("guarded version held the peer cursor: %d of %d %v", cursor, high, err)
	}
	expect := func(store *state.Store, events, archived, deferred int, title string) {
		t.Helper()
		var gotEvents, gotArchived, gotDeferred, unrelated int
		var gotTitle string
		if err := store.Reader.QueryRow(`SELECT (SELECT count(*) FROM conversation_events WHERE session_id=?),
 (SELECT archived_at>0 FROM sessions WHERE id=?),(SELECT count(*) FROM sync_deferred),
 (SELECT count(*) FROM sessions WHERE id='session-unrelated'),(SELECT title FROM sessions WHERE id=?)`, id, id, id).Scan(&gotEvents, &gotArchived, &gotDeferred, &unrelated, &gotTitle); err != nil {
			t.Fatal(err)
		}
		if gotEvents != events || gotArchived != archived || gotDeferred != deferred || unrelated != 1 || gotTitle != title {
			t.Fatalf("events %d archived %d deferred %d unrelated %d title %q", gotEvents, gotArchived, gotDeferred, unrelated, gotTitle)
		}
	}
	expect(worker.auth.store, 1, 0, 2, "")
	// The deferral is durable: a fresh store over the same database still holds and replays it.
	restarted, err := state.Open(worker.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	syncArchiveFixture(t, main.auth.store, restarted)
	expect(restarted, 1, 0, 2, "")
	if err := network.PatchWorkerConversationState(t.Context(), restarted.DB, id, "idle"); err != nil {
		t.Fatal(err)
	}
	syncArchiveFixture(t, main.auth.store, restarted)
	expect(restarted, 0, 1, 0, "renamed on main")
}
