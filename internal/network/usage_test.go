package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/harness"
)

func TestUsageReplayedEventCountsOnce(t *testing.T) {
	store := testSessionStore(t, t.TempDir())
	id := "session-usage"
	if err := upsertSession(t.Context(), store.DB, SessionInfo{ID: id, Transport: "acp", Harness: "claude", FolderID: "folder-1"}); err != nil {
		t.Fatal(err)
	}
	const hour = 1_800_000_000_000 // ms, on an hour boundary
	changes := acp.LogChanges{LastSeq: 3, Entries: []acp.LogEntry{
		{Seq: 1, TS: hour + 1000, Kind: "prompt", Payload: json.RawMessage(`{"text":"hi"}`)},
		{Seq: 2, TS: hour + 2000, Kind: "usage_update", Payload: json.RawMessage(`{"_meta":{"_claude/model":"claude-opus-5-5"},"cost":{"amount":0.25,"currency":"USD"}}`)},
		{Seq: 3, TS: hour + 61_000, Kind: "turn_end", Payload: json.RawMessage(`{"stopReason":"end_turn","usage":{"inputTokens":2,"outputTokens":40,"cachedReadTokens":900,"cachedWriteTokens":10}}`)},
	}}
	// The worker's flush retry and a restored log both send already stored events again.
	for range 3 {
		if err := persistConversationChanges(t.Context(), store.DB, id, changes); err != nil {
			t.Fatal(err)
		}
	}
	var turns, output, cacheRead, cost int64
	var model string
	if err := store.Reader.QueryRow("SELECT turns,output_tokens,cache_read_tokens,cost_micros,model FROM sessions WHERE id=?", id).Scan(&turns, &output, &cacheRead, &cost, &model); err != nil {
		t.Fatal(err)
	}
	if turns != 1 || output != 40 || cacheRead != 900 || cost != 250_000 || model != "claude-opus-5-5" {
		t.Fatalf("session counters: turns=%d output=%d cache=%d cost=%d model=%q", turns, output, cacheRead, cost, model)
	}
	var rows, hourTurns, sessions, hourCost, agentMS int64
	var node string
	if err := store.Reader.QueryRow("SELECT count(*),sum(turns),sum(sessions),sum(cost_micros),sum(agent_ms),max(node_id) FROM usage_hourly WHERE hour=? AND harness='claude' AND folder_id='folder-1'", hour/1000).
		Scan(&rows, &hourTurns, &sessions, &hourCost, &agentMS, &node); err != nil {
		t.Fatal(err)
	}
	local, _ := store.NodeID(t.Context())
	if rows != 1 || hourTurns != 1 || sessions != 1 || hourCost != 250_000 || agentMS != 60_000 || node != local {
		t.Fatalf("hourly: rows=%d turns=%d sessions=%d cost=%d agent_ms=%d node=%q", rows, hourTurns, sessions, hourCost, agentMS, node)
	}
}

func TestUsageCostResetCountsTheNewTotal(t *testing.T) {
	store := testSessionStore(t, t.TempDir())
	id := "session-cost"
	if err := upsertSession(t.Context(), store.DB, SessionInfo{ID: id, Transport: "acp", Harness: "omp"}); err != nil {
		t.Fatal(err)
	}
	cost := func(amount string) json.RawMessage {
		return json.RawMessage(`{"sessionUpdate":"usage_update","cost":{"amount":` + amount + `,"currency":"USD"}}`)
	}
	// 0.50 then 0.80 adds 0.30; the adapter restarts and reports 0.10, then 0.30.
	for seq, amount := range []string{"0.5", "0.8", "0.1", "0.3"} {
		changes := acp.LogChanges{LastSeq: int64(seq + 1), Entries: []acp.LogEntry{{Seq: int64(seq + 1), TS: 3_600_000, Kind: "usage_update", Payload: cost(amount)}}}
		if err := persistConversationChanges(t.Context(), store.DB, id, changes); err != nil {
			t.Fatal(err)
		}
	}
	var session, hourly int64
	if err := store.Reader.QueryRow("SELECT cost_micros FROM sessions WHERE id=?", id).Scan(&session); err != nil {
		t.Fatal(err)
	}
	if err := store.Reader.QueryRow("SELECT sum(cost_micros) FROM usage_hourly WHERE harness='omp'").Scan(&hourly); err != nil {
		t.Fatal(err)
	}
	if session != 1_100_000 || hourly != 1_100_000 {
		t.Fatalf("cost after reset: session=%d hourly=%d, want 1100000", session, hourly)
	}
}

func TestUsageWithoutReportedCostStaysUnknown(t *testing.T) {
	store := testSessionStore(t, t.TempDir())
	id := "session-codex"
	if err := upsertSession(t.Context(), store.DB, SessionInfo{ID: id, Transport: "acp", Harness: "codex"}); err != nil {
		t.Fatal(err)
	}
	changes := acp.LogChanges{LastSeq: 1, Entries: []acp.LogEntry{{Seq: 1, TS: 1, Kind: "turn_end", Payload: json.RawMessage(`{"usage":{"outputTokens":5}}`)}}}
	if err := persistConversationChanges(t.Context(), store.DB, id, changes); err != nil {
		t.Fatal(err)
	}
	var cost int64
	if err := store.Reader.QueryRow("SELECT cost_micros FROM sessions WHERE id=?", id).Scan(&cost); err != nil || cost != -1 {
		t.Fatalf("an agent that reports no cost must not read $0: cost=%d err=%v", cost, err)
	}
}

func TestFinishedTurnsRefreshTheirAgentsLimitsOncePerGate(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	store := g.store
	if err := upsertSession(t.Context(), store.DB, SessionInfo{ID: "chat", Transport: "acp", Harness: "claude"}); err != nil {
		t.Fatal(err)
	}
	// Real time: last_event_at is stamped with the clock, and the report selects sessions by it.
	now := time.Now().Truncate(time.Hour)
	var reads []string
	turns := &turnLimits{store: store, now: func() time.Time { return now }, read: func(_ context.Context, name string) (harness.LimitSnapshot, bool) {
		reads = append(reads, name)
		return harness.LimitSnapshot{Harness: name, State: "available", Windows: []harness.LimitWindow{{ID: "five_hour", RemainingPercent: new(40.0)}}}, true
	}}
	turn := func(seq int64) {
		changes := acp.LogChanges{LastSeq: seq, Entries: []acp.LogEntry{{Seq: seq, TS: now.UnixMilli(), Kind: "turn_end", Payload: json.RawMessage(`{}`)}}}
		if err := persistConversationChanges(t.Context(), store.DB, "chat", changes); err != nil {
			t.Fatal(err)
		}
	}
	check := func() {
		if err := turns.check(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	turn(1)
	check() // the first check only records where turns stand
	turn(2)
	check()
	turn(3)
	now = now.Add(limitsTurnGate - time.Second)
	check()
	if len(reads) != 1 || reads[0] != "claude" {
		t.Fatalf("one refresh inside the gate, got %v", reads)
	}
	now = now.Add(time.Second)
	check()
	if len(reads) != 2 {
		t.Fatalf("a turn inside the gate must refresh once it passes, got %v", reads)
	}
	report, err := readUsage(t.Context(), store.Reader, now.Add(-2*time.Hour).Unix(), now.Add(time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Samples) != 1 || *report.Samples[0].UsedPercent != 60 || report.Samples[0].WindowID != "five_hour" {
		t.Fatalf("limit samples: %+v", report.Samples)
	}
	if len(report.Sessions) != 1 || report.Sessions[0].Turns != 3 || len(report.Hourly) != 1 || report.Hourly[0].Turns != 3 {
		t.Fatalf("usage report: %+v", report)
	}
}

func TestUsageRejectsUnboundedRanges(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	for _, query := range []string{"", "from=10&to=5", "from=0&to=9000000"} {
		recorder := httptest.NewRecorder()
		g.serveUsage(recorder, httptest.NewRequest(http.MethodGet, "/api/usage?"+query, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d", query, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	g.serveUsage(recorder, httptest.NewRequest(http.MethodGet, "/api/usage?from=0&to=3600", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "{\"hourly\":[],\"sessions\":[],\"samples\":[]}\n" {
		t.Fatalf("empty usage: %d %s", recorder.Code, recorder.Body)
	}
}

func TestUsageCostAfterWorkerRestartCountsTheNewProcessInFull(t *testing.T) {
	store := testSessionStore(t, t.TempDir())
	id := "session-restart"
	if err := upsertSession(t.Context(), store.DB, SessionInfo{ID: id, Transport: "acp", Harness: "claude"}); err != nil {
		t.Fatal(err)
	}
	cost := func(seq int64, amount string) acp.LogChanges {
		return acp.LogChanges{LastSeq: seq, Entries: []acp.LogEntry{{Seq: seq, TS: 3_600_000, Kind: "usage_update", Payload: json.RawMessage(`{"cost":{"amount":` + amount + `,"currency":"USD"}}`)}}}
	}
	if err := persistConversationChanges(t.Context(), store.DB, id, cost(1, "0.09")); err != nil {
		t.Fatal(err)
	}
	// The worker restarted after seq 1; the new agent's first total (0.40) is not a delta from 0.09.
	if err := persistConversationChangesSince(t.Context(), store.DB, id, cost(2, "0.4"), 1); err != nil {
		t.Fatal(err)
	}
	var session int64
	if err := store.Reader.QueryRow("SELECT cost_micros FROM sessions WHERE id=?", id).Scan(&session); err != nil || session != 490_000 {
		t.Fatalf("cost after restart: %d %v, want 490000", session, err)
	}
}

func TestLimitSamplesKeepTheHoursLastReadingForAWeekPerMachine(t *testing.T) {
	store := testSessionStore(t, t.TempDir())
	write := func(node string, at time.Time, used float64) {
		t.Helper()
		tx, err := store.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		remaining := 100 - used
		// RemainingPercent alone is converted to used.
		snapshot := harness.LimitSnapshot{Harness: "codex", Windows: []harness.LimitWindow{{ID: "codex/primary", RemainingPercent: &remaining}}}
		if err := writeLimitSamples(t.Context(), tx, node, at, []harness.LimitSnapshot{snapshot}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	samples := func(node string) map[int64]float64 {
		rows, err := store.Reader.Query("SELECT hour,used_percent FROM limit_samples WHERE node_id=?", node)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[int64]float64{}
		for rows.Next() {
			var hour int64
			var used float64
			if err := rows.Scan(&hour, &used); err != nil {
				t.Fatal(err)
			}
			out[hour] = used
		}
		return out
	}
	start := time.Date(2026, 11, 1, 10, 5, 0, 0, time.UTC)
	write("peer", start, 10)
	write("local", start, 20)
	write("local", start.Add(40*time.Minute), 25) // same hour: the last reading wins
	if got := samples("local"); len(got) != 1 || got[start.Truncate(time.Hour).Unix()] != 25 {
		t.Fatalf("hour's last reading: %v", got)
	}
	later := start.Add(8 * 24 * time.Hour)
	write("local", later, 50)
	if got := samples("local"); len(got) != 1 || got[later.Truncate(time.Hour).Unix()] != 50 {
		t.Fatalf("week-old samples kept: %v", got)
	}
	if got := samples("peer"); len(got) != 1 {
		t.Fatalf("another machine's samples were deleted: %v", got)
	}
}

// Work in a worktree counts under its main folder's project: worktree_of names it, and a row
// from an older peer without it falls back to the definition root of a worktree path.
func TestUsageCountsWorktreesUnderTheirProject(t *testing.T) {
	store := testSessionStore(t, t.TempDir())
	main := "/src/app"
	for _, info := range []SessionInfo{
		{ID: "session-main", Directory: main, FolderID: "f-main"},
		{ID: "session-new", Directory: "/home/u/.local/share/agentpack/worktrees/0123456789abcdef/fix", FolderID: "f-new", DefinitionRoot: "/src", WorktreeOf: main},
		{ID: "session-old", Directory: "/home/u/.local/share/agentpack/worktrees/0123456789abcdef/old/sub", FolderID: "f-old", DefinitionRoot: main},
		{ID: "session-orphan", Directory: "/w/worktrees/fedcba9876543210/x", FolderID: "f-orphan", WorktreeOf: "/src/gone"},
		{ID: "session-other", Directory: "/src/other", FolderID: "f-other", DefinitionRoot: main},
	} {
		info.Harness, info.Transport = "claude", "acp"
		if err := upsertSession(t.Context(), store.DB, info); err != nil {
			t.Fatal(err)
		}
	}
	local, _ := store.NodeID(t.Context())
	report := usageReport{}
	for _, id := range []string{"f-main", "f-new", "f-old", "f-orphan", "f-other"} {
		report.Hourly = append(report.Hourly, usageHour{NodeID: local, FolderID: id})
		report.Sessions = append(report.Sessions, usageSession{NodeID: local, FolderID: id, Directory: "d-" + id})
	}
	tx, err := store.Reader.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := attributeWorktrees(t.Context(), tx, &report); err != nil {
		t.Fatal(err)
	}
	want := []struct{ folder, directory string }{{"f-main", "d-f-main"}, {"f-main", main}, {"f-main", main}, {"dir:/src/gone", "/src/gone"}, {"f-other", "d-f-other"}}
	for i, w := range want {
		if report.Hourly[i].FolderID != w.folder || report.Sessions[i].FolderID != w.folder || report.Sessions[i].Directory != w.directory {
			t.Fatalf("row %d: hour %q session %+v, want %+v", i, report.Hourly[i].FolderID, report.Sessions[i], w)
		}
	}
}
