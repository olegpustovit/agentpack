package acp

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
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
	entries     []LogEntry
}

type eventLogPersistence struct {
	path     string
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	dirty    atomic.Bool
	mu       sync.Mutex
	err      error
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
	log.seq++
	entry := LogEntry{Seq: log.seq, TS: ts, Kind: kind}
	entry.Payload = append(json.RawMessage(nil), payload...)
	size := entrySize(entry)
	if size < 0 || size > log.limit {
		log.truncated = true
		log.mu.Unlock()
		return entry
	}
	log.entries = append(log.entries, entry)
	log.bytes += size
	for log.bytes > log.limit && len(log.entries) > 0 {
		log.coalesceDropOldest()
	}
	onAppend := log.onAppend
	persistence := log.persistence
	log.mu.Unlock()
	if persistence != nil {
		persistence.dirty.Store(true)
	}
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
func (log *EventLog) EnablePersistence(path string) error {
	if path == "" {
		return errors.New("event log persistence path is required")
	}
	log.notifyMu.Lock()
	defer log.notifyMu.Unlock()
	log.mu.Lock()
	if log.persistence != nil {
		log.mu.Unlock()
		return errors.New("event log persistence already enabled")
	}
	persistence := &eventLogPersistence{
		path: path,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	log.persistence = persistence
	entries := append([]LogEntry(nil), log.entries...)
	log.mu.Unlock()
	if err := writePersistedEventLog(path, entries, log.limit); err != nil {
		log.mu.Lock()
		log.persistence = nil
		log.mu.Unlock()
		return err
	}
	go log.persistLoop(persistence)
	return nil
}

func (log *EventLog) Close() error {
	log.notifyMu.Lock()
	log.mu.Lock()
	persistence := log.persistence
	log.persistence = nil
	log.mu.Unlock()
	log.notifyMu.Unlock()
	if persistence == nil {
		return nil
	}
	persistence.stopOnce.Do(func() { close(persistence.stop) })
	<-persistence.done
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return persistence.err
}

func (log *EventLog) persistLoop(persistence *eventLogPersistence) {
	defer close(persistence.done)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if persistence.dirty.Swap(false) {
				log.persistSnapshot(persistence)
			}
		case <-persistence.stop:
			log.persistSnapshot(persistence)
			return
		}
	}
}

func (log *EventLog) persistSnapshot(persistence *eventLogPersistence) {
	if err := writePersistedEventLog(persistence.path, log.Snapshot(), log.limit); err != nil {
		persistence.mu.Lock()
		if persistence.err == nil {
			persistence.err = err
		}
		persistence.mu.Unlock()
	}
}

func writePersistedEventLog(path string, entries []LogEntry, limit int) error {
	lines := make([][]byte, len(entries))
	total, start := 0, 0
	for i, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		line = append(line, '\n')
		if len(line) > limit {
			continue
		}
		lines[i] = line
		total += len(line)
		for total > limit {
			total -= len(lines[start])
			start++
		}
	}
	data := make([]byte, 0, total)
	for i := start; i < len(lines); i++ {
		data = append(data, lines[i]...)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
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
