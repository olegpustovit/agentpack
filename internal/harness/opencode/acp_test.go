package opencode

import (
	"encoding/json"
	"testing"
)

func TestAdaptAgentLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantTools int
		wantGroup string
		metaLeft  bool
	}{
		{
			name:      "child session grouped tool",
			line:      `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","_meta":{"opencode/child-session-updates":[{"groupId":"child-1","title":"Search","kind":"search","status":"running"}]}}}`,
			wantTools: 1,
			wantGroup: "child-1",
		},
		{
			name:      "meta stripped",
			line:      `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","_meta":{"opencode/child-session-updates":[{"groupedId":"g2","title":"Read"}]}}}`,
			wantTools: 1,
			wantGroup: "g2",
		},
		{
			name:     "passthrough",
			line:     `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{}}}`,
			metaLeft: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adaptAgentLine([]byte(tt.line))
			if tt.wantTools == 0 {
				if len(got.Log) != 0 || got.Forward != nil {
					t.Fatalf("log=%v forward=%q", got.Log, got.Forward)
				}
				return
			}
			if len(got.Log) != tt.wantTools {
				t.Fatalf("log=%v", got.Log)
			}
			var payload struct {
				ToolCallID string `json:"toolCallId"`
			}
			if err := json.Unmarshal(got.Log[0].Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.ToolCallID != tt.wantGroup {
				t.Fatalf("toolCallId=%q", payload.ToolCallID)
			}
			var forwarded struct {
				Params struct {
					Meta map[string]any `json:"_meta"`
				} `json:"params"`
			}
			if err := json.Unmarshal(got.Forward, &forwarded); err != nil {
				t.Fatal(err)
			}
			if forwarded.Params.Meta != nil {
				if _, ok := forwarded.Params.Meta[childSessionUpdatesMeta]; ok {
					t.Fatalf("_meta=%v", forwarded.Params.Meta)
				}
			}
		})
	}
}
