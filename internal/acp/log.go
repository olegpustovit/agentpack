package acp

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"
)

const defaultLogByteLimit = 4 << 20

// EventLog stores bounded session events for replay and coalesced retention.
type EventLog struct {
	mu          sync.RWMutex
	notifyMu    sync.Mutex
	onAppend    func(LogEntry)
	fold        Fold
	persistence *eventLogPersistence
	limit       int
	seq         int64
	bytes       int
	truncated   bool
	replaying   bool
	closed      bool
	entries     []LogEntry
	toolInputs  map[toolInputKey]toolInputStamp
	subagents   map[string]bool // announced child session -> cancellable
	changed     chan struct{}   // closed and replaced whenever readers have something new
}

// LogChanges contains durable updates, including replacements for coalesced chunks.
// Removed contains only absorbed chunks, never ordinary memory evictions.
type LogChanges struct {
	Entries   []LogEntry
	Removed   []int64
	LastSeq   int64
	Truncated bool
	// Checkpoint is the fold's state after LastSeq, so a sink stores events and what they add up to together.
	Checkpoint json.RawMessage
}

// Fold derives state from the log. Apply runs for every retained entry under the log's lock, in
// sequence order, so each batch carries the fold at exactly its last sequence.
type Fold interface {
	Apply(LogEntry)
	Checkpoint() json.RawMessage
}

// eventLogPersistence hands batches to the sink. pending is guarded by the log's mu;
// writeMu makes one writer at a time, and the sink runs outside mu so readers never wait on IO.
type eventLogPersistence struct {
	sink             func(LogChanges) error
	stop, done       chan struct{}
	writeMu          sync.Mutex
	pending          map[int64]LogEntry
	bytes            int
	tail             LogEntry // the newest stored row, which the next text chunk may join
	flushedSeq       int64
	flushedTruncated bool
	err              error // the last write's failure; cleared by the next success
}

func NewEventLog(limitBytes int) *EventLog {
	if limitBytes <= 0 {
		limitBytes = defaultLogByteLimit
	}
	return &EventLog{limit: limitBytes}
}

func (log *EventLog) Append(kind string, ts int64, payload json.RawMessage) LogEntry {
	log.notifyMu.Lock()
	defer log.notifyMu.Unlock()
	log.mu.Lock()
	if log.closed || log.replaying && kind != "agent_exit" && kind != "queued_prompt" {
		log.mu.Unlock()
		return LogEntry{}
	}
	log.seq++
	entry := LogEntry{Seq: log.seq, TS: ts, Kind: kind}
	entry.Payload = append(json.RawMessage(nil), payload...)
	log.compactToolPayload(&entry)
	size := entrySize(entry)
	if size < 0 || size > log.limit {
		log.truncated = true
		log.signal()
		log.mu.Unlock()
		return entry
	}
	// A failing store never drops events: they stay in memory and pending until a write succeeds.
	persistence := log.persistence
	if log.fold != nil {
		log.fold.Apply(entry)
	}
	log.entries = append(log.entries, entry)
	log.bytes += size
	if persistence != nil {
		persistence.put(entry)
	}
	for log.bytes > log.limit && len(log.entries) > 0 {
		log.coalesceDropOldest()
	}
	// Backpressure keeps a pending batch within the memory budget while the store keeps up.
	pressure := persistence != nil && persistence.bytes >= log.limit && persistence.err == nil
	if persistence != nil && persistence.bytes >= log.limit && persistence.err != nil {
		// While the store is down, events memory has evicted are given up, so pending stays bounded.
		persistence.dropBefore(log.entries[0].Seq)
	}
	log.signal()
	onAppend := log.onAppend
	log.mu.Unlock()

	if pressure {
		_ = log.flushChanges(persistence, false)
	}
	if onAppend != nil {
		onAppend(entry)
	}
	return entry
}

type toolInputKey struct{ call, child string }

type toolInputStamp struct {
	digest [32]byte
	seq    int64
}

func (log *EventLog) compactToolPayload(entry *LogEntry) {
	if entry.Kind != "tool_call" && entry.Kind != "tool_call_update" {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(entry.Payload, &fields) != nil {
		return
	}
	diffChanged := omitRepeatedDiffText(fields)
	inputChanged := log.omitRepeatedToolInput(entry, fields)
	if diffChanged || inputChanged {
		if body, err := json.Marshal(fields); err == nil {
			entry.Payload = body
		}
	}
}

func omitRepeatedDiffText(fields map[string]json.RawMessage) bool {
	text, blocks := toolDiffInput(fields)
	if len(text) <= len(`"newTextFromInput":true`)-len(`"newText":`) {
		return false
	}
	changed := false
	for _, block := range blocks {
		if string(block["type"]) == `"diff"` && bytes.Equal(block["newText"], text) {
			delete(block, "newText")
			block["newTextFromInput"] = json.RawMessage(`true`)
			changed = true
		}
	}
	if changed {
		if content, err := json.Marshal(blocks); err == nil {
			fields["content"] = content
			return true
		}
	}
	return false
}

func (log *EventLog) omitRepeatedToolInput(entry *LogEntry, fields map[string]json.RawMessage) bool {
	id := fields["toolCallId"]
	if len(id) <= 2 || id[0] != '"' || len(id)+len(fields[SubagentKey]) > 256 {
		return false
	}
	key := toolInputKey{string(id), string(fields[SubagentKey])}
	raw := fields["rawInput"]
	if len(raw) == 0 {
		if entry.Kind == "tool_call" {
			delete(log.toolInputs, key)
		}
		return false
	}
	digest := sha256.Sum256(raw)
	if entry.Kind == "tool_call_update" {
		previous, ok := log.toolInputs[key]
		if !ok {
			return false
		}
		if previous.digest != digest {
			delete(log.toolInputs, key)
			return false
		}
		if _, retained := slices.BinarySearchFunc(log.entries, previous.seq, func(row LogEntry, seq int64) int {
			return cmp.Compare(row.Seq, seq)
		}); retained {
			delete(fields, "rawInput")
			return true
		}
		return false
	}
	// shortcut: reset the small index at 128 tools. Use eviction order if many calls overlap.
	if log.toolInputs == nil || len(log.toolInputs) == 128 {
		log.toolInputs = make(map[toolInputKey]toolInputStamp)
	}
	log.toolInputs[key] = toolInputStamp{digest, entry.Seq}
	return false
}

// Changed returns a channel that closes on the next append, restore or truncation.
func (log *EventLog) Changed() <-chan struct{} {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.changed == nil {
		log.changed = make(chan struct{})
	}
	return log.changed
}

// signal wakes Changed waiters. The caller holds mu.
func (log *EventLog) signal() {
	if log.changed != nil {
		close(log.changed)
		log.changed = nil
	}
}

func (log *EventLog) SetOnAppend(onAppend func(LogEntry)) []LogEntry {
	log.notifyMu.Lock()
	defer log.notifyMu.Unlock()
	log.mu.Lock()
	log.onAppend = onAppend
	entries := append([]LogEntry(nil), log.entries...)
	log.mu.Unlock()
	return entries
}

// EnableSink calls sink serially from one writer at a time; sink must not call log methods.
// Failed writes remain pending and are retried with backoff; Close reports a failure that remains.
func (log *EventLog) EnableSink(sink func(LogChanges) error) error {
	if sink == nil {
		return errors.New("event log sink is required")
	}
	log.notifyMu.Lock()
	defer log.notifyMu.Unlock()
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.persistence != nil {
		return errors.New("event log persistence already enabled")
	}
	persistence := &eventLogPersistence{
		sink: sink, stop: make(chan struct{}), done: make(chan struct{}),
		pending: map[int64]LogEntry{},
	}
	for _, entry := range log.entries {
		persistence.put(entry)
	}
	// Entries the log holds now may already be stored rows; one a joined row absorbs is deleted.
	persistence.flushedSeq = log.seq
	log.mu.Unlock()
	err := log.flushChanges(persistence, false)
	log.mu.Lock()
	if err != nil {
		return err
	}
	log.persistence = persistence
	go log.persistLoop(persistence)
	return nil
}

// Close stops later appends, then writes what is pending. It does not hold notifyMu while the
// writer stops, because the writer appends its own notice.
func (log *EventLog) Close() error {
	log.notifyMu.Lock()
	log.mu.Lock()
	persistence := log.persistence
	if persistence != nil && !log.closed {
		log.closed = true
	} else {
		persistence = nil
	}
	log.mu.Unlock()
	log.notifyMu.Unlock()
	if persistence == nil {
		return nil
	}
	close(persistence.stop)
	<-persistence.done
	err := log.flushChanges(persistence, false)
	log.mu.Lock()
	log.persistence = nil
	log.mu.Unlock()
	return err
}

// Flush makes a turn boundary durable before its acknowledgement or idle state is visible.
func (log *EventLog) Flush() error { return log.flush(false) }

// Republish writes the fold's checkpoint even when no event is pending, for a sink whose
// reading of it changed (a worker that finished starting). A closed log already wrote its last.
func (log *EventLog) Republish() error { return log.flush(true) }

func (log *EventLog) flush(force bool) error {
	log.mu.Lock()
	closed, persistence := log.closed, log.persistence
	log.mu.Unlock()
	if closed && force {
		return nil
	}
	if closed {
		return errors.New("event log closed")
	}
	if persistence == nil {
		return nil
	}
	return log.flushChanges(persistence, force)
}

// SetFold installs fold and applies to it the retained entries after seq, the sequence its
// checkpoint was taken at (0 for a new fold). Install it before the sink.
func (log *EventLog) SetFold(fold Fold, seq int64) {
	log.notifyMu.Lock()
	defer log.notifyMu.Unlock()
	log.mu.Lock()
	defer log.mu.Unlock()
	for _, entry := range log.entries {
		if entry.Seq > seq {
			fold.Apply(entry)
		}
	}
	log.fold = fold
}

// ReadFold calls read with the fold while no entry is being applied to it.
func (log *EventLog) ReadFold(read func(Fold)) {
	log.mu.RLock()
	defer log.mu.RUnlock()
	if log.fold != nil {
		read(log.fold)
	}
}

// Fold is the installed fold, or nil.
func (log *EventLog) Fold() Fold {
	log.mu.RLock()
	defer log.mu.RUnlock()
	return log.fold
}

// persistFlushDelay batches a burst of appends into one write transaction.
var persistFlushDelay = 250 * time.Millisecond

// persistRetry is the first wait after a failed write; it doubles up to persistRetryMax.
var persistRetry, persistRetryMax = time.Second, 30 * time.Second

// persistLoop sleeps until an append, then flushes after persistFlushDelay; an idle log never wakes it.
// A failed write is retried with backoff, and each run of failures leaves one notice in the chat.
func (log *EventLog) persistLoop(persistence *eventLogPersistence) {
	defer close(persistence.done)
	delay := time.NewTimer(persistFlushDelay)
	delay.Stop()
	wait := func(d time.Duration) bool {
		delay.Reset(d)
		select {
		case <-delay.C:
			return true
		case <-persistence.stop:
			delay.Stop()
			return false
		}
	}
	retry, failing := persistRetry, false
	for {
		// Taken before flushing, so an append that races the flush still wakes the next wait.
		changed := log.Changed()
		if err := log.flushChanges(persistence, false); err != nil {
			if !failing {
				failing = true
				log.Append("notice", time.Now().UnixMilli(), json.RawMessage(`{"sessionUpdate":"notice","severity":"warning","title":"Chat history is not being saved","description":"agentpack keeps the conversation in memory and retries."}`))
			}
			if !wait(retry) {
				return
			}
			retry = min(retry*2, persistRetryMax)
			continue
		}
		retry, failing = persistRetry, false
		select {
		case <-changed:
		case <-persistence.stop:
			return
		}
		if !wait(persistFlushDelay) {
			return
		}
	}
}

func (p *eventLogPersistence) dropBefore(seq int64) {
	for pending, entry := range p.pending {
		if pending < seq {
			delete(p.pending, pending)
			p.bytes -= entrySize(entry)
		}
	}
}

func (p *eventLogPersistence) put(entry LogEntry) {
	if old, ok := p.pending[entry.Seq]; ok {
		p.bytes -= entrySize(old)
	}
	p.pending[entry.Seq] = entry
	p.bytes += entrySize(entry)
}

// flushChanges writes what is pending. The caller must not hold log.mu: the batch is taken out
// under it and written outside it, so appends and readers go on during a slow write.
func (log *EventLog) flushChanges(p *eventLogPersistence, force bool) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	log.mu.Lock()
	if !force && len(p.pending) == 0 && p.flushedSeq == log.seq && p.flushedTruncated == log.truncated {
		log.mu.Unlock()
		return nil
	}
	changes := LogChanges{LastSeq: log.seq, Truncated: log.truncated}
	if log.fold != nil {
		changes.Checkpoint = log.fold.Checkpoint()
	}
	batch := slices.SortedFunc(maps.Values(p.pending), func(a, b LogEntry) int { return cmp.Compare(a.Seq, b.Seq) })
	pending, flushedSeq, tail := p.pending, p.flushedSeq, p.tail
	p.pending, p.bytes = map[int64]LogEntry{}, 0
	log.mu.Unlock()

	// A streamed reply is one stored and replicated row per run of chunks, not one per token.
	for _, entry := range batch {
		if joined, ok := joinStoredChunks(tail, entry); ok {
			tail.Payload = joined
			if entry.Seq <= flushedSeq {
				changes.Removed = append(changes.Removed, entry.Seq) // a restored row the run absorbed
			}
			if n := len(changes.Entries); n > 0 && changes.Entries[n-1].Seq == tail.Seq {
				changes.Entries[n-1] = tail
			} else {
				changes.Entries = append(changes.Entries, tail)
			}
			continue
		}
		tail = entry
		changes.Entries = append(changes.Entries, entry)
	}
	err := p.sink(changes)

	log.mu.Lock()
	defer log.mu.Unlock()
	p.err = err
	if err == nil {
		p.flushedSeq, p.flushedTruncated, p.tail = changes.LastSeq, changes.Truncated, tail
		return nil
	}
	// Put the batch back; the next write joins it again.
	for _, entry := range pending {
		p.put(entry)
	}
	return err
}

// maxStoredChunk bounds a joined row, which every later chunk of its run rewrites and replicates.
const maxStoredChunk = 4 << 10

// joinStoredChunks joins next onto the stored row tail when both are plain text chunks of one
// speaker and next follows it, so nothing but text is joined.
func joinStoredChunks(tail, next LogEntry) (json.RawMessage, bool) {
	if tail.Kind != next.Kind || tail.Kind != "agent_message_chunk" && tail.Kind != "agent_thought_chunk" ||
		tail.Seq >= next.Seq || len(tail.Payload)+len(next.Payload) > maxStoredChunk {
		return nil, false
	}
	headA, _, okA := splitTextChunk(tail.Payload)
	headB, _, okB := splitTextChunk(next.Payload)
	if !okA || !okB || !bytes.Equal(headA, headB) {
		return nil, false
	}
	joined, err := mergeTextChunks(tail.Payload, next.Payload)
	return joined, err == nil
}

// splitTextChunk splits {"text":{"text":"…"}}, optionally led by "_subagent", into everything up to
// the text's opening quote and the still-escaped text. Any other shape is not ok.
func splitTextChunk(payload []byte) (head, text []byte, ok bool) {
	const open = `"text":{"text":"`
	i := bytes.Index(payload, []byte(open))
	if i < 0 || !bytes.HasSuffix(payload, []byte(`"}}`)) || len(payload) < i+len(open)+3 {
		return nil, nil, false
	}
	head, text = payload[:i+len(open)], payload[i+len(open):len(payload)-3]
	switch {
	case i == 1 && payload[0] == '{':
	case bytes.HasPrefix(head, []byte(`{"`+SubagentKey+`":"`)) && bytes.HasSuffix(head, []byte(`",`+open)):
		if !plainString(head[len(`{"`+SubagentKey+`":"`) : len(head)-len(`",`+open)]) {
			return nil, nil, false
		}
	default:
		return nil, nil, false
	}
	return head, text, plainString(text)
}

// plainString reports escaped string content with no unescaped quote, so it is one JSON string.
func plainString(content []byte) bool {
	for i := 0; i < len(content); i++ {
		switch content[i] {
		case '\\':
			i++
		case '"':
			return false
		}
	}
	return true
}

// Restore seeds a new log without renumbering events or notifying live subscribers.
func (log *EventLog) Restore(entries []LogEntry, lastSeq int64, truncated bool) error {
	log.notifyMu.Lock()
	defer log.notifyMu.Unlock()
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.seq != 0 || len(log.entries) != 0 || log.persistence != nil {
		return errors.New("event log is not empty")
	}
	if lastSeq < 0 {
		return errors.New("invalid restored event sequence")
	}
	var previous int64
	var total int
	for _, entry := range entries {
		if entry.Seq <= previous || entry.Seq > lastSeq || entrySize(entry) < 0 {
			return errors.New("invalid restored event sequence")
		}
		total += entrySize(entry)
		if total > log.limit {
			return errors.New("restored event log exceeds memory budget")
		}
		previous = entry.Seq
	}
	log.seq, log.truncated = lastSeq, truncated
	for _, entry := range entries {
		entry.Payload = append(json.RawMessage(nil), entry.Payload...)
		log.entries = append(log.entries, entry)
		log.bytes += entrySize(entry)
	}
	log.signal()
	return nil
}

// ReadSince returns the retained entries after since. truncated reports that the log ever left
// entries out; gap that some after since are no longer in memory, so this reader missed them.
func (log *EventLog) ReadSince(since int64) (out []LogEntry, truncated, gap bool) {
	log.mu.RLock()
	defer log.mu.RUnlock()
	start, _ := slices.BinarySearchFunc(log.entries, since+1, func(entry LogEntry, seq int64) int { return cmp.Compare(entry.Seq, seq) })
	first := log.seq + 1
	if len(log.entries) > 0 {
		first = log.entries[0].Seq
	}
	return slices.Clone(log.entries[start:]), log.truncated, since > 0 && since < first-1
}

func (log *EventLog) Snapshot() []LogEntry {
	log.mu.RLock()
	defer log.mu.RUnlock()
	out := make([]LogEntry, len(log.entries))
	copy(out, log.entries)
	return out
}

// Seq is the sequence of the newest event; it moves on every append.
func (log *EventLog) Seq() int64 {
	log.mu.RLock()
	defer log.mu.RUnlock()
	return log.seq
}

func (log *EventLog) Truncated() bool {
	log.mu.RLock()
	defer log.mu.RUnlock()
	return log.truncated
}

// MarkTruncated records that an event could not be retained, such as an oversized wire frame.
func (log *EventLog) MarkTruncated() {
	log.mu.Lock()
	log.truncated = true
	log.signal()
	log.mu.Unlock()
}

// SetReplaying suppresses the native load response when durable history is already restored.
// Process exits remain live events even while the adapter is loading its session.
func (log *EventLog) SetReplaying(replaying bool) {
	log.mu.Lock()
	log.replaying = replaying
	log.mu.Unlock()
}

// maxCoalescedPayload caps how much old reply text is merged into one entry before older text is dropped instead.
const maxCoalescedPayload = 64 << 10

func (log *EventLog) coalesceDropOldest() {
	if len(log.entries) == 0 {
		return
	}
	if len(log.entries) > 1 {
		first, second := log.entries[0], log.entries[1]
		// Bounded, because a merge re-encodes the head: an unbounded head grows to the whole budget and every
		// streamed token would then re-encode megabytes.
		if first.Kind == second.Kind && first.Kind == "agent_message_chunk" && len(first.Payload)+len(second.Payload) <= maxCoalescedPayload {
			merged, err := mergeTextChunks(first.Payload, second.Payload)
			candidate := second
			candidate.Payload = merged
			candidate.Seq = first.Seq
			candidateSize := entrySize(candidate)
			if err == nil && candidateSize >= 0 && candidateSize <= log.limit {
				log.bytes -= entrySize(first) + entrySize(second)
				log.entries[1] = candidate
				log.entries = log.entries[1:]
				log.bytes += candidateSize
				// Memory only: the store keeps the rows it has, and joins chunks as it writes them.
				log.truncated = true
				return
			}
		}
	}
	dropped := log.entries[0]
	log.entries = log.entries[1:]
	log.bytes -= entrySize(dropped)
	log.truncated = true
}

// entrySize is the length of the entry's stored JSON line, counted without encoding it: every
// streamed token is sized several times. An invalid payload is -1 and never retained.
func entrySize(entry LogEntry) int {
	if !json.Valid(entry.Payload) {
		return -1
	}
	const fixed = len(`{"seq":,"ts":,"kind":"","payload":}`) + 1
	return fixed + digits(entry.Seq) + digits(entry.TS) + len(entry.Kind) + len(entry.Payload)
}

func digits(n int64) int {
	count := 1
	if n < 0 {
		count++
	}
	for n /= 10; n != 0; n /= 10 {
		count++
	}
	return count
}

// mergeTextChunks joins two text chunks of the same speaker, keeping every image or link either one
// carries; chunks of different subagents never merge.
func mergeTextChunks(a, b json.RawMessage) (json.RawMessage, error) {
	// Plain text of one speaker, the shape of nearly every streamed token, joins without decoding.
	if headA, textA, ok := splitTextChunk(a); ok {
		if headB, textB, ok := splitTextChunk(b); ok && bytes.Equal(headA, headB) {
			out := make(json.RawMessage, 0, len(headA)+len(textA)+len(textB)+3)
			return append(append(append(append(out, headA...), textA...), textB...), `"}}`...), nil
		}
	}
	type chunk struct {
		Subagent string `json:"_subagent,omitempty"`
		Text     struct {
			Text string `json:"text"`
		} `json:"text"`
		Image        json.RawMessage   `json:"image,omitempty"`
		ResourceLink json.RawMessage   `json:"resource_link,omitempty"`
		Images       []json.RawMessage `json:"images,omitempty"`
	}
	var left, right chunk
	if err := json.Unmarshal(a, &left); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &right); err != nil {
		return nil, err
	}
	if left.Subagent != right.Subagent {
		return nil, errors.New("chunks belong to different subagents")
	}
	attachments := func(c chunk) []json.RawMessage {
		if c.Images != nil {
			return c.Images
		}
		var out []json.RawMessage
		for _, one := range []json.RawMessage{c.Image, c.ResourceLink} {
			if len(one) > 0 && string(one) != "null" {
				out = append(out, one)
			}
		}
		return out
	}
	merged := chunk{Subagent: left.Subagent, Images: append(attachments(left), attachments(right)...)}
	merged.Text.Text = left.Text.Text + right.Text.Text
	if len(merged.Images) == 0 {
		merged.Images = nil
	}
	return json.Marshal(merged)
}
