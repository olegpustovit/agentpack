package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Sums every entry of a recorded session, as the store would for a fresh log.
func fixtureUsage(t *testing.T, name string) Usage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "acp", name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	total := Usage{CostMicros: -1, CostTotalMicros: -1}
	for line := range splitLines(data) {
		if line == "" {
			continue
		}
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		usage, ok := ParseUsage(entry)
		if !ok {
			continue
		}
		total.Turns += usage.Turns
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens
		total.CacheReadTokens += usage.CacheReadTokens
		total.CacheWriteTokens += usage.CacheWriteTokens
		total.CostMicros = max(total.CostMicros, usage.CostMicros)
		total.CostTotalMicros = max(total.CostTotalMicros, usage.CostTotalMicros)
		if usage.Model != "" {
			total.Model = usage.Model
		}
	}
	return total
}

func TestParseUsageFixtures(t *testing.T) {
	for name, want := range map[string]Usage{
		"claude": {Turns: 1, InputTokens: 2, OutputTokens: 4, CacheReadTokens: 12932, CacheWriteTokens: 11063, CostMicros: -1, CostTotalMicros: 91304, Model: "claude-opus-5-5"},
		"codex":  {Turns: 1, InputTokens: 2842, OutputTokens: 5, CacheReadTokens: 12288, CostMicros: -1, CostTotalMicros: -1, Model: "gpt-6.1-sol"},
		"omp":    {Turns: 1, InputTokens: 3, OutputTokens: 5, CacheWriteTokens: 7769, CostMicros: -1, CostTotalMicros: 974},
		"agy":    {Turns: 1, InputTokens: 7719, OutputTokens: 13, CostMicros: -1, CostTotalMicros: -1},
		"grok":   {Turns: 1, InputTokens: 13868, OutputTokens: 30, CacheReadTokens: 2176, CostMicros: 29004, CostTotalMicros: -1, Model: "grok-4.7"},
		"cursor": {Turns: 1, CostMicros: -1, CostTotalMicros: -1},
		// A subagent's events never count toward the parent.
		"subagents": {Turns: 1, CostMicros: -1, CostTotalMicros: -1},
	} {
		if got := fixtureUsage(t, name); got != want {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
		}
	}
}

func TestParseUsageIgnoresOtherEntries(t *testing.T) {
	for _, entry := range []LogEntry{
		{Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"hi"}}`)},
		{Kind: "usage_update", Payload: json.RawMessage(`{"used":10,"size":100}`)},
		{Kind: "usage_update", Payload: json.RawMessage(`{"_subagent":"child","cost":{"amount":1,"currency":"USD"}}`)},
		{Kind: "usage_update", Payload: json.RawMessage(`{"cost":{"amount":1,"currency":"EUR"}}`)},
	} {
		if usage, ok := ParseUsage(entry); ok {
			t.Errorf("%s %s counted: %+v", entry.Kind, entry.Payload, usage)
		}
	}
}
