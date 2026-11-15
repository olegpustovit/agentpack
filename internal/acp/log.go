package acp

import (
	"cmp"
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
	entries     []LogEntry
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
	if log.replaying && kind != "agent_exit" {
		log.mu.Unlock()
		return LogEntry{}
	}
	log.seq++
	entry := LogEntry{Seq: log.seq, TS: ts, Kind: kind}
	entry.Payload = append(json.RawMessage(nil), payload...)
	size := entrySize(entry)
	if size < 0 || size > log.limit {
		log.truncated = true
		log.mu.Unlock()
		return entry
	}
	persistence := log.persistence
	if persistence != nil && persistence.err != nil {
		log.truncated = true
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
	onAppend := log.onAppend
	log.mu.Unlock()

	if onAppend != nil {
		onAppend(entry)
	}
	return entry
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
	return persistence.err
}

// Flush makes a turn boundary durable before its acknowledgement or idle state is visible.
func (log *EventLog) Flush() error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.persistence == nil {
		return nil
	}
	log.flushChanges(log.persistence)
	return log.persistence.err
}

func (log *EventLog) persistLoop(persistence *eventLogPersistence) {
	defer close(persistence.done)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			log.mu.Lock()
			log.flushChanges(persistence)
			log.mu.Unlock()
		case <-persistence.stop:
			log.mu.Lock()
			log.flushChanges(persistence)
			log.mu.Unlock()
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
	return nil
}

func (log *EventLog) ReadSince(since int64) (out []LogEntry, truncated bool) {
	log.mu.RLock()
	defer log.mu.RUnlock()
	for _, entry := range log.entries {
		if entry.Seq > since {
			out = append(out, entry)
		}
	}
	return out, log.truncated
}

func (log *EventLog) Snapshot() []LogEntry {
	log.mu.RLock()
	defer log.mu.RUnlock()
	out := make([]LogEntry, len(log.entries))
	copy(out, log.entries)
	return out
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
	log.mu.Unlock()
}

// SetReplaying suppresses the native load response when durable history is already restored.
// Process exits remain live events even while the adapter is loading its session.
func (log *EventLog) SetReplaying(replaying bool) {
	log.mu.Lock()
	log.replaying = replaying
	log.mu.Unlock()
}

func (log *EventLog) coalesceDropOldest() {
	if len(log.entries) == 0 {
		return
	}
	if len(log.entries) > 1 {
		first, second := log.entries[0], log.entries[1]
		if first.Kind == second.Kind && first.Kind == "agent_message_chunk" {
			merged, err := mergeTextChunks(first.Payload, second.Payload)
			candidate := second
			candidate.Payload = merged
			candidate.Seq = first.Seq
			candidateSize := entrySize(candidate)
			if err == nil && candidateSize >= 0 && candidateSize <= log.limit {
				log.bytes -= entrySize(first) + entrySize(second)
				log.entries = append([]LogEntry{candidate}, log.entries[2:]...)
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

func mergeTextChunks(a, b json.RawMessage) (json.RawMessage, error) {
	var left, right struct {
		Text struct {
			Text string `json:"text"`
		} `json:"text"`
	}
	if err := json.Unmarshal(a, &left); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &right); err != nil {
		return nil, err
	}
	merged := struct {
		Text struct {
			Text string `json:"text"`
		} `json:"text"`
	}{}
	merged.Text.Text = left.Text.Text + right.Text.Text
	return json.Marshal(merged)
}
