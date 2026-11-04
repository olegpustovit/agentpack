package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func chunk(text string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"text": map[string]string{"text": text}})
	return raw
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
func TestEventLogPersistenceStaysPrivateAndBoundedAfterRollover(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "worker")
	if err := os.Mkdir(directory, 0o700); err != nil { // t.TempDir honours the umask
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports no Unix permission bits; the directory's ACL is what protects it there.
	if runtime.GOOS != "windows" && dirInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("worker directory permissions = %o", dirInfo.Mode().Perm())
	}
	path := filepath.Join(directory, "conversation.ndjson")
	const limit = 512
	log := NewEventLog(limit)
	if err := log.EnablePersistence(path); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		log.Append("tool_call_update", int64(i), chunk(strings.Repeat("x", 64)))
	}
	if !log.Truncated() {
		t.Fatal("expected bounded log rollover")
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > limit || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("persisted log size=%d mode=%o", info.Size(), info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []LogEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 || entries[len(entries)-1].Seq != 50 {
		t.Fatalf("persisted rollover entries = %+v", entries)
	}
}
