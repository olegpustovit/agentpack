package hub

import (
	"encoding/json"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestPendingElicitationFrom(t *testing.T) {
	state := acp.ReduceLog([]acp.LogEntry{{
		Kind:    "elicitation",
		Payload: json.RawMessage(`{"elicitationId":"e1","choices":["Direct","Friendly"],"optionIds":["direct-id","friendly-id"],"allowMultiple":true}`),
	}})
	got := pendingElicitationFrom(state)
	if got.id != "e1" || len(got.choices) != 2 || len(got.optionIDs) != 2 || !got.allowMultiple {
		t.Fatalf("got %+v", got)
	}
}

func TestPreviewPendingPrefersPermission(t *testing.T) {
	state := acp.ReduceLog([]acp.LogEntry{
		{Kind: "elicitation", Payload: json.RawMessage(`{"elicitationId":"e1","choices":["a"]}`)},
		{Kind: "permission_request", Payload: json.RawMessage(`{"requestId":"p1","options":[{"id":"allow"}]}`)},
	})
	perm, elicit := previewPending(state)
	if perm.requestID != "p1" || elicit.id != "" {
		t.Fatalf("perm=%+v elicit=%+v", perm, elicit)
	}
}

func TestPreviewPendingSkipsAnsweredPermission(t *testing.T) {
	state := acp.ReduceLog([]acp.LogEntry{
		{Kind: "permission_request", Payload: json.RawMessage(`{"requestId":"p1","options":[{"id":"allow"}]}`)},
		{Kind: "permission_result", Payload: json.RawMessage(`{"requestId":"p1","optionId":"allow"}`)},
		{Kind: "elicitation", Payload: json.RawMessage(`{"elicitationId":"e1","choices":["Direct"]}`)},
	})
	perm, elicit := previewPending(state)
	if perm.requestID != "" || elicit.id != "e1" {
		t.Fatalf("perm=%+v elicit=%+v", perm, elicit)
	}
}

func TestPendingElicitationSkipsInactive(t *testing.T) {
	state := acp.ThreadState{
		NeedsYou: true,
		Items: []acp.ThreadItem{
			{Kind: "elicitation", Data: json.RawMessage(`{"elicitationId":"old","inactive":true}`)},
			{Kind: "elicitation", Data: json.RawMessage(`{"elicitationId":"new"}`)},
		},
	}
	if got := pendingElicitationFrom(state); got.id != "new" {
		t.Fatalf("got %+v", got)
	}
}

func TestElicitationChoiceUsesOptionIDs(t *testing.T) {
	pending := pendingElicitation{choices: []string{"Local", "Remote"}, optionIDs: []string{"local-id", "remote-id"}}
	if got := elicitationChoice(pending, 1); got != "remote-id" {
		t.Fatalf("elicitationChoice = %q, want option ID", got)
	}
}

func TestToggleElicitationSelection(t *testing.T) {
	selected := toggleElicitationSelection(nil, "local-id")
	selected = toggleElicitationSelection(selected, "remote-id")
	if len(selected) != 2 || selected[0] != "local-id" || selected[1] != "remote-id" {
		t.Fatalf("selected = %v", selected)
	}
	selected = toggleElicitationSelection(selected, "local-id")
	if len(selected) != 1 || selected[0] != "remote-id" {
		t.Fatalf("deselected = %v", selected)
	}
}
