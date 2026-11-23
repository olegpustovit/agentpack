package acp

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func chunk(text string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"text": map[string]string{"text": text}})
	return raw
}

func TestEventLogFlushIsSynchronousAndReportsFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		log := NewEventLog(1024)
		want := errors.New("write failed")
		var stored int64
		if err := log.EnableSink(func(changes LogChanges) error {
			if fail {
				return want
			}
			stored = changes.LastSeq
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		log.Append("prompt", 1, chunk("go"))
		err := log.Flush()
		if fail {
			if !errors.Is(err, want) {
				t.Fatalf("flush hid failure: %v", err)
			}
		} else if err != nil || stored != 1 {
			t.Fatalf("flush returned before persistence: %d %v", stored, err)
		}
		log.Close()
	}
}

func TestEventLogCoalescesTinyChunks(t *testing.T) {
	log := NewEventLog(16 << 10)
	const n = 10_000
	for i := range n {
		log.Append("agent_message_chunk", int64(i), chunk("x"))
	}
	if len(log.Snapshot()) >= n {
		t.Fatalf("expected coalescing to shrink %d entries, got %d", n, len(log.Snapshot()))
	}
	if log.bytes > log.limit {
		t.Fatalf("log bytes %d exceeded limit %d", log.bytes, log.limit)
	}
	all, truncated := log.ReadSince(0)
	if !truncated {
		t.Fatal("expected truncated flag after bound pressure")
	}
	var text string
	for _, entry := range all {
		if entry.Kind != "agent_message_chunk" {
			continue
		}
		var body struct {
			Text struct {
				Text string `json:"text"`
			} `json:"text"`
		}
		if err := json.Unmarshal(entry.Payload, &body); err != nil {
			t.Fatal(err)
		}
		text += body.Text.Text
	}
	if len(text) != n {
		t.Fatalf("lost bytes after coalesce: got %d want %d", len(text), n)
	}
}

func TestEventLogByteBound(t *testing.T) {
	log := NewEventLog(4096)
	big, _ := json.Marshal(map[string]string{"body": string(make([]byte, 2048))})
	for i := range 20 {
		log.Append("tool_call_update", int64(i), big)
	}
	if log.bytes > log.limit {
		t.Fatalf("log bytes %d exceeded limit %d", log.bytes, log.limit)
	}
}
func TestEventLogDropsSingleOversizedEntry(t *testing.T) {
	log := NewEventLog(64)
	payload, err := json.Marshal(map[string]string{"output": strings.Repeat("x", 256)})
	if err != nil {
		t.Fatal(err)
	}
	log.Append("tool_call_update", 1, payload)
	if got := len(log.Snapshot()); got != 0 {
		t.Fatalf("oversized event retained %d entries", got)
	}
	if log.bytes != 0 || !log.Truncated() {
		t.Fatalf("bytes=%d truncated=%v, want 0/true", log.bytes, log.Truncated())
	}
}
func applyLogChanges(rows map[int64]LogEntry, changes LogChanges) {
	for _, seq := range changes.Removed {
		delete(rows, seq)
	}
	for _, entry := range changes.Entries {
		rows[entry.Seq] = entry
	}
}

func TestEventLogSinkRetainsEvictedHistory(t *testing.T) {
	const limit = 512
	log := NewEventLog(limit)
	stored := map[int64]LogEntry{}
	var lastSeq int64
	if err := log.EnableSink(func(changes LogChanges) error {
		applyLogChanges(stored, changes)
		lastSeq = changes.LastSeq
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		log.Append("tool_call_update", int64(i), chunk(strings.Repeat("x", 64)))
	}
	if !log.Truncated() || log.bytes > limit {
		t.Fatal("memory bound was not retained")
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 50 || lastSeq != 50 {
		t.Fatalf("durable events=%d last_seq=%d", len(stored), lastSeq)
	}
}

func TestEventLogSinkReplacesAlreadyPersistedMergedChunks(t *testing.T) {
	log := NewEventLog(512)
	log.Append("agent_message_chunk", 1, chunk("x"))
	log.Append("agent_message_chunk", 2, chunk("x"))
	stored := map[int64]LogEntry{}
	if err := log.EnableSink(func(changes LogChanges) error {
		applyLogChanges(stored, changes)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := range 4000 {
		log.Append("agent_message_chunk", int64(i+3), chunk("x"))
		log.mu.Lock()
		pendingBytes := log.persistence.bytes
		log.mu.Unlock()
		if pendingBytes > 4*log.limit {
			t.Fatal("pending writes exceeded byte bound")
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if _, exists := stored[2]; exists {
		t.Fatal("absorbed row was retained")
	}
	total := 0
	for _, entry := range stored {
		var payload struct {
			Text struct {
				Text string `json:"text"`
			} `json:"text"`
		}
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		total += len(payload.Text.Text)
	}
	if total != 4002 {
		t.Fatalf("merged text was lost or duplicated: %d bytes", total)
	}
}

func TestEventLogKeepsEventsThroughAFailedWrite(t *testing.T) {
	log := NewEventLog(1 << 10)
	failure := errors.New("sink failed")
	failing := true
	stored := map[int64]LogEntry{}
	if err := log.EnableSink(func(changes LogChanges) error {
		if failing {
			return failure
		}
		applyLogChanges(stored, changes)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var seen []string
	log.SetOnAppend(func(entry LogEntry) { seen = append(seen, entry.Kind) })
	log.Append("prompt", 1, chunk("go"))
	if !errors.Is(log.Flush(), failure) {
		t.Fatal("flush hid the failure")
	}
	// Later events still reach memory and subscribers while the store fails.
	log.Append("turn_end", 2, chunk("done"))
	if got := strings.Join(seen, ","); !strings.HasPrefix(got, "prompt") || !strings.Contains(got, "turn_end") {
		t.Fatalf("subscribers saw %q", got)
	}
	if entries, _ := log.ReadSince(0); len(entries) < 2 {
		t.Fatalf("memory kept %d entries", len(entries))
	}
	failing = false
	if err := log.Flush(); err != nil {
		t.Fatalf("a successful retry clears the failure: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := stored[1]; !ok {
		t.Fatal("the failed batch was lost")
	}
	if _, ok := stored[2]; !ok {
		t.Fatal("an event appended during the failure was lost")
	}
}

func TestEventLogSinkDoesNotBlockReaders(t *testing.T) {
	log := NewEventLog(1 << 10)
	release := make(chan struct{})
	writing := make(chan struct{}, 1)
	if err := log.EnableSink(func(changes LogChanges) error {
		if len(changes.Entries) > 0 {
			writing <- struct{}{}
			<-release
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	log.Append("prompt", 1, chunk("go"))
	flushed := make(chan error, 1)
	go func() { flushed <- log.Flush() }()
	<-writing
	if entries, _ := log.ReadSince(0); len(entries) != 1 {
		t.Fatalf("read during a write = %d entries", len(entries))
	}
	log.Append("turn_end", 2, chunk("done"))
	close(release)
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEventLogRestoreKeepsSequenceAndCopiesPayload(t *testing.T) {
	log := NewEventLog(512)
	entries := []LogEntry{{Seq: 3, Kind: "agent_message_chunk", Payload: chunk("old")}}
	if err := log.Restore(entries, 9, true); err != nil {
		t.Fatal(err)
	}
	entries[0].Payload[0] = 'x'
	if got := log.Append("turn_end", 2, json.RawMessage(`{}`)); got.Seq != 10 {
		t.Fatalf("resumed seq=%d", got.Seq)
	}
	if !log.Truncated() || !json.Valid(log.Snapshot()[0].Payload) {
		t.Fatal("restored state was not retained independently")
	}
	for _, restored := range [][]LogEntry{
		{{Seq: 2}, {Seq: 1}}, {{Seq: 11}}, {{Seq: 0}}, {{Seq: 1, Payload: chunk(strings.Repeat("x", 512))}},
	} {
		empty := NewEventLog(512)
		if err := empty.Restore(restored, 10, false); err == nil {
			t.Fatalf("invalid restore accepted: %+v", restored)
		}
		if len(empty.Snapshot()) != 0 {
			t.Fatal("failed restore partially changed the log")
		}
	}
}

func TestEventLogNativeReplayDoesNotDuplicateRestoredHistory(t *testing.T) {
	log := NewEventLog(512)
	if err := log.Restore([]LogEntry{{Seq: 1, Kind: "agent_message_chunk", Payload: chunk("old")}}, 1, false); err != nil {
		t.Fatal(err)
	}
	log.SetReplaying(true)
	log.Append("agent_message_chunk", 2, chunk("old"))
	log.SetReplaying(false)
	if entry := log.Append("agent_message_chunk", 3, chunk("new")); entry.Seq != 2 || len(log.Snapshot()) != 2 {
		t.Fatalf("replay duplicated history: %+v", log.Snapshot())
	}
	log.SetReplaying(true)
	log.Append("agent_exit", 4, json.RawMessage(`{}`))
	if got := log.Snapshot(); len(got) != 3 || got[2].Kind != "agent_exit" {
		t.Fatal("native exit disappeared during load")
	}
}

// Reading a stream's new tail is a binary search plus a copy of the new entries, not a scan of the log.
func BenchmarkEventLogReadSinceTail(b *testing.B) {
	log := NewEventLog(64 << 20)
	payload := json.RawMessage(`{"content":{"type":"text","text":"token "}}`)
	for range 20000 {
		log.Append("agent_message_chunk", 1, payload)
	}
	since := log.seq - 1
	b.ReportAllocs()
	for b.Loop() {
		if entries, _ := log.ReadSince(since); len(entries) != 1 {
			b.Fatal(len(entries))
		}
	}
}

// A streamed token wakes every waiting reader once, with no polling.
// The log is full here, so every append also evicts: the steady state of a long chat.
func BenchmarkEventLogAppendWakesReaders(b *testing.B) {
	log := NewEventLog(1 << 20)
	payload := json.RawMessage(`{"content":{"type":"text","text":"token "}}`)
	b.ReportAllocs()
	for b.Loop() {
		changed := log.Changed()
		log.Append("agent_message_chunk", 1, payload)
		<-changed
	}
}

func TestReadSinceAllocatesOnlyTheTail(t *testing.T) {
	log := NewEventLog(64 << 20)
	for range 5000 {
		log.Append("agent_message_chunk", 1, json.RawMessage(`{}`))
	}
	since := log.seq - 3
	if allocs := testing.AllocsPerRun(100, func() { log.ReadSince(since) }); allocs > 1 {
		t.Fatalf("ReadSince allocated %v times for a three-entry tail", allocs)
	}
}

func TestCoalescedHeadStaysBounded(t *testing.T) {
	log := NewEventLog(256 << 10)
	payload := json.RawMessage(`{"content":{"type":"text","text":"` + strings.Repeat("x", 200) + `"}}`)
	for range 20000 {
		log.Append("agent_message_chunk", 1, payload)
	}
	if head := len(log.Snapshot()[0].Payload); head > maxCoalescedPayload+len(payload) {
		t.Fatalf("head entry grew to %d bytes", head)
	}
}

func TestEventLogOmitsRepeatedToolInputWhileItsSourceIsRetained(t *testing.T) {
	log := NewEventLog(1024)
	call := json.RawMessage(`{"toolCallId":"tool","rawInput":{"content":"first"}}`)
	update := json.RawMessage(`{"toolCallId":"tool","rawInput":{"content":"first"},"status":"completed"}`)
	log.Append("tool_call", 1, call)
	entry := log.Append("tool_call_update", 2, update)
	if entry.Seq != 2 || strings.Contains(string(entry.Payload), "rawInput") {
		t.Fatalf("compacted update = %+v", entry)
	}
	thread := ReduceLog(log.Snapshot())
	if got := string(thread.Items[0].Data); !strings.Contains(got, `"content":"first"`) || !strings.Contains(got, `"status":"completed"`) {
		t.Fatalf("reduced update = %s", got)
	}
	changed := json.RawMessage(`{"toolCallId":"tool","rawInput":{"content":"changed"}}`)
	log.Append("tool_call_update", 3, changed)
	entry = log.Append("tool_call_update", 4, update)
	if !strings.Contains(string(entry.Payload), "rawInput") {
		t.Fatalf("update lost its input after a different input: %s", entry.Payload)
	}
	log.Append("tool_call", 5, call)
	log.Append("agent_message_chunk", 6, chunk(strings.Repeat("x", 850)))
	log.Append("agent_message_chunk", 7, chunk(strings.Repeat("x", 700)))
	entry = log.Append("tool_call_update", 8, update)
	if !strings.Contains(string(entry.Payload), "rawInput") {
		t.Fatalf("update lost its input after dropping the source: %s", entry.Payload)
	}
}

func TestEventLogStoresMatchingDiffTextOnce(t *testing.T) {
	log := NewEventLog(0)
	log.Append("tool_call", 1, []byte(`{"toolCallId":"write","rawInput":{"content":"new file content used by the tool"}}`))
	update := log.Append("tool_call_update", 2, []byte(`{"toolCallId":"write","rawInput":{"content":"new file content used by the tool"},"content":[{"type":"diff","path":"first","newText":"new file content used by the tool"},{"type":"diff","path":"second","newText":"different"}]}`))
	body := string(update.Payload)
	if update.Seq != 2 || strings.Contains(body, `"new file content used by the tool"`) || !strings.Contains(body, `"newTextFromInput":true`) ||
		!strings.Contains(body, `"newText":"different"`) {
		t.Fatalf("tool update = %+v", update)
	}
	reduced := string(ReduceLog(log.Snapshot()).Items[0].Data)
	if !strings.Contains(reduced, `"newText":"new file content used by the tool"`) || !strings.Contains(reduced, `"newText":"different"`) {
		t.Fatalf("reduced tool = %s", reduced)
	}
}

func TestEventLogClosesWhileTheStoreKeepsFailing(t *testing.T) {
	delay, retry := persistFlushDelay, persistRetry
	persistFlushDelay, persistRetry = time.Millisecond, time.Millisecond
	t.Cleanup(func() { persistFlushDelay, persistRetry = delay, retry })
	log := NewEventLog(512)
	failure := errors.New("sink failed")
	if err := log.EnableSink(func(LogChanges) error { return failure }); err != nil {
		t.Fatal(err)
	}
	log.Append("prompt", 1, chunk("go"))
	// The writer fails, appends its notice and retries; Close must not wait on it forever.
	deadline := time.After(2 * time.Second)
	for !slices.ContainsFunc(log.Snapshot(), func(entry LogEntry) bool { return entry.Kind == "notice" }) {
		select {
		case <-deadline:
			t.Fatal("no notice about the failing store")
		case <-time.After(10 * time.Millisecond):
		}
	}
	for i := range 50 {
		log.Append("agent_message_chunk", int64(i), chunk(strings.Repeat("x", 40)))
	}
	log.mu.RLock()
	pending := log.persistence.bytes
	log.mu.RUnlock()
	if pending > 2*log.limit {
		t.Fatalf("pending grew to %d bytes while the store was down", pending)
	}
	if !errors.Is(log.Close(), failure) {
		t.Fatal("close hid the failure")
	}
}
