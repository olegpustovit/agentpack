package acp

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
}

type eventLogPersistence struct {
	sink             func(LogChanges) error
	stop, done       chan struct{}
	pending          map[int64]LogEntry
	removed          map[int64]struct{}
	bytes            int
	flushedSeq       int64
	flushedTruncated bool
	err              error
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
	log.compactToolInput(&entry)
	size := entrySize(entry)
	if size < 0 || size > log.limit {
		log.truncated = true
		log.signal()
		log.mu.Unlock()
		return entry
	}
	persistence := log.persistence
	if persistence != nil && persistence.err != nil {
		log.truncated = true
		log.signal()
		log.mu.Unlock()
		return entry
	}
	log.entries = append(log.entries, entry)
	log.bytes += size
	if persistence != nil {
		persistence.put(entry)
	}
	for log.bytes > log.limit && len(log.entries) > 0 {
		log.coalesceDropOldest()
	}
	if persistence != nil && persistence.bytes >= log.limit {
		// Apply backpressure before a pending batch can grow beyond the memory budget.
		log.flushChanges(persistence)
	}
	log.signal()
	onAppend := log.onAppend
	log.mu.Unlock()

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

func (log *EventLog) compactToolInput(entry *LogEntry) {
	if entry.Kind != "tool_call" && entry.Kind != "tool_call_update" {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(entry.Payload, &fields) != nil {
		return
	}
	id := fields["toolCallId"]
	if len(id) <= 2 || id[0] != '"' || len(id)+len(fields[SubagentKey]) > 256 {
		return
	}
	key := toolInputKey{string(id), string(fields[SubagentKey])}
	raw := fields["rawInput"]
	if len(raw) == 0 {
		if entry.Kind == "tool_call" {
			delete(log.toolInputs, key)
		}
		return
	}
	digest := sha256.Sum256(raw)
	if entry.Kind == "tool_call_update" {
		previous, ok := log.toolInputs[key]
		if !ok {
			return
		}
		if previous.digest != digest {
			delete(log.toolInputs, key)
			return
		}
		if _, retained := slices.BinarySearchFunc(log.entries, previous.seq, func(row LogEntry, seq int64) int {
			return cmp.Compare(row.Seq, seq)
		}); retained {
			delete(fields, "rawInput")
			if body, err := json.Marshal(fields); err == nil {
				entry.Payload = body
			}
		}
		return
	}
	// shortcut: reset the small index at 128 tools. Use eviction order if many calls overlap.
	if log.toolInputs == nil || len(log.toolInputs) == 128 {
		log.toolInputs = make(map[toolInputKey]toolInputStamp)
	}
	log.toolInputs[key] = toolInputStamp{digest, entry.Seq}
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

// EnableSink calls sink serially under the log lock; sink must not call log methods.
// Failed writes remain pending for retry, and Close reports the first failure.
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
		pending: map[int64]LogEntry{}, removed: map[int64]struct{}{},
	}
	log.persistence = persistence
	for _, entry := range log.entries {
		persistence.put(entry)
	}
	log.flushChanges(persistence)
	if persistence.err != nil {
		log.persistence = nil
		return persistence.err
	}
	go log.persistLoop(persistence)
	return nil
}

func (log *EventLog) Close() error {
	log.notifyMu.Lock()
	defer log.notifyMu.Unlock()
	log.mu.Lock()
	persistence := log.persistence
	log.mu.Unlock()
	if persistence == nil {
		return nil
	}
	close(persistence.stop)
	<-persistence.done
	log.mu.Lock()
	defer log.mu.Unlock()
	log.persistence = nil
	log.closed = true
	return persistence.err
}

// Flush makes a turn boundary durable before its acknowledgement or idle state is visible.
func (log *EventLog) Flush() error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.closed {
		return errors.New("event log closed")
	}
	if log.persistence == nil {
		return nil
	}
	log.flushChanges(log.persistence)
	return log.persistence.err
}

// persistFlushDelay batches a burst of appends into one write transaction.
const persistFlushDelay = 250 * time.Millisecond

// persistLoop sleeps until an append, then flushes after persistFlushDelay; an idle log never wakes it.
func (log *EventLog) persistLoop(persistence *eventLogPersistence) {
	defer close(persistence.done)
	flush := func() {
		log.mu.Lock()
		log.flushChanges(persistence)
		log.mu.Unlock()
	}
	delay := time.NewTimer(persistFlushDelay)
	delay.Stop()
	for {
		// Taken before flushing, so an append that races the flush still wakes the next wait.
		changed := log.Changed()
		flush()
		select {
		case <-changed:
		case <-persistence.stop:
			flush()
			return
		}
		delay.Reset(persistFlushDelay)
		select {
		case <-delay.C:
		case <-persistence.stop:
			flush()
			return
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

func (p *eventLogPersistence) absorb(seq int64) {
	if old, ok := p.pending[seq]; ok {
		delete(p.pending, seq)
		p.bytes -= entrySize(old)
	}
	if seq <= p.flushedSeq {
		if _, ok := p.removed[seq]; !ok {
			p.removed[seq] = struct{}{}
			p.bytes += 8
		}
	}
}

// flushChanges requires log.mu. Keeping the lock applies backpressure during slow writes.
func (log *EventLog) flushChanges(p *eventLogPersistence) {
	if len(p.pending) == 0 && len(p.removed) == 0 && p.flushedSeq == log.seq && p.flushedTruncated == log.truncated {
		return
	}
	changes := LogChanges{LastSeq: log.seq, Truncated: log.truncated}
	for _, entry := range p.pending {
		changes.Entries = append(changes.Entries, entry)
	}
	slices.SortFunc(changes.Entries, func(a, b LogEntry) int { return cmp.Compare(a.Seq, b.Seq) })
	for seq := range p.removed {
		changes.Removed = append(changes.Removed, seq)
	}
	slices.Sort(changes.Removed)
	if err := p.sink(changes); err != nil {
		if p.err == nil {
			p.err = err
		}
		return
	}
	clear(p.pending)
	clear(p.removed)
	p.bytes, p.flushedSeq = 0, log.seq
	p.flushedTruncated = log.truncated
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

func (log *EventLog) ReadSince(since int64) (out []LogEntry, truncated bool) {
	log.mu.RLock()
	defer log.mu.RUnlock()
	start, _ := slices.BinarySearchFunc(log.entries, since+1, func(entry LogEntry, seq int64) int { return cmp.Compare(entry.Seq, seq) })
	return slices.Clone(log.entries[start:]), log.truncated
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
				if log.persistence != nil {
					log.persistence.put(candidate)
					log.persistence.absorb(second.Seq)
				}
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
func entrySize(entry LogEntry) int {
	line, err := json.Marshal(entry)
	if err != nil {
		return -1
	}
	return len(line) + 1
}

// mergeTextChunks joins two text chunks of the same speaker; chunks of different subagents never merge.
func mergeTextChunks(a, b json.RawMessage) (json.RawMessage, error) {
	type chunk struct {
		Subagent string `json:"_subagent,omitempty"`
		Text     struct {
			Text string `json:"text"`
		} `json:"text"`
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
	left.Text.Text += right.Text.Text
	return json.Marshal(left)
}
