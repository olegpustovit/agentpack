package hub

import (
	"encoding/json"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestPendingPermissionFromFixtureShape(t *testing.T) {
	state := acp.ReduceLog([]acp.LogEntry{{
		Kind:    "permission_request",
		Payload: json.RawMessage(`{"requestId":"p1","tool":"Bash","options":[{"id":"allow","label":"Allow once"}]}`),
	}})
	perm := pendingPermissionFrom(state)
	if perm.requestID != "p1" || len(perm.optionIDs) != 1 || perm.optionIDs[0] != "allow" {
		t.Fatalf("got %#v", perm)
	}
}

func TestPendingPermissionSelectsOutstandingRequest(t *testing.T) {
	state := acp.ReduceLog([]acp.LogEntry{
		{Kind: "permission_request", Payload: json.RawMessage(`{"requestId":"p1","options":[{"id":"allow"}]}`)},
		{Kind: "permission_request", Payload: json.RawMessage(`{"requestId":"p2","options":[{"id":"reject"}]}`)},
		{Kind: "permission_result", Payload: json.RawMessage(`{"requestId":"p1","optionId":"allow"}`)},
	})
	got := pendingPermissionFrom(state)
	if got.requestID != "p2" || len(got.optionIDs) != 1 || got.optionIDs[0] != "reject" {
		t.Fatalf("got %#v", got)
	}
}

func TestPendingPermissionSkipsInactive(t *testing.T) {
	state := acp.ThreadState{
		NeedsYou: true,
		Items: []acp.ThreadItem{
			{Kind: "permission", Data: json.RawMessage(`{"requestId":"old","inactive":true}`)},
			{Kind: "permission", Data: json.RawMessage(`{"requestId":"new"}`)},
		},
	}
	if got := pendingPermissionFrom(state); got.requestID != "new" {
		t.Fatalf("got %#v", got)
	}
}
