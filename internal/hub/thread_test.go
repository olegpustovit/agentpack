package hub

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestReduceThreadFullFixture(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "acp", "full.jsonl")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var entries []acp.LogEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry acp.LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	state := ReduceThread(entries)
	if len(state.Items) < 8 {
		t.Fatalf("items=%d", len(state.Items))
	}
	if state.ContextLimit != 200_000 {
		t.Fatalf("context limit %d", state.ContextLimit)
	}
	if state.PlanKind != "Publish the branch" {
		t.Fatalf("plan_kind = %q", state.PlanKind)
	}
	foundPerm, foundElicit, foundDivider, foundDiff := false, false, false, false
	for _, item := range state.Items {
		switch item.Kind {
		case "permission":
			foundPerm = true
		case "elicitation":
			foundElicit = true
		case "divider":
			foundDivider = true
		case "tool":
			if hasDiffBlock(item.Data) {
				foundDiff = true
			}
		}
	}
	if !foundPerm || !foundElicit || !foundDivider || !foundDiff {
		t.Fatalf("perm=%v elicit=%v divider=%v diff=%v", foundPerm, foundElicit, foundDivider, foundDiff)
	}
}

func hasDiffBlock(data any) bool {
	raw, err := json.Marshal(data)
	if err != nil {
		return false
	}
	var tool struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &tool); err != nil {
		return false
	}
	for _, block := range tool.Content {
		if block.Type == "diff" {
			return true
		}
	}
	return false
}
