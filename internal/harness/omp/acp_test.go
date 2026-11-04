package omp

import (
	"encoding/json"
	"testing"
)

func TestRewriteClientLine(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		method string
		prompt string
		keep   bool
	}{
		{
			name:   "steering method",
			line:   `{"jsonrpc":"2.0","id":1,"method":"_session/steering","params":{"sessionId":"s1","text":"while running"}}`,
			method: "session/prompt",
			prompt: "while running",
		},
		{
			name:   "steering meta on prompt",
			line:   `{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"s1","prompt":[],"_meta":{"steering":{"text":"follow up"}}}}`,
			method: "session/prompt",
			prompt: "follow up",
		},
		{
			name: "unchanged",
			line: `{"jsonrpc":"2.0","method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"hi"}]}}`,
			keep: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := []byte(tt.line)
			out := rewriteClientLine(in)
			if tt.keep {
				if string(out) != string(in) {
					t.Fatalf("got %s", out)
				}
				return
			}
			var envelope struct {
				Method string `json:"method"`
				Params struct {
					Prompt []struct {
						Text string `json:"text"`
					} `json:"prompt"`
					Meta map[string]any `json:"_meta"`
				} `json:"params"`
			}
			if err := json.Unmarshal(out, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Method != tt.method {
				t.Fatalf("method=%q", envelope.Method)
			}
			if len(envelope.Params.Prompt) == 0 || envelope.Params.Prompt[0].Text != tt.prompt {
				t.Fatalf("prompt=%v", envelope.Params.Prompt)
			}
			if envelope.Params.Meta != nil {
				if _, ok := envelope.Params.Meta["steering"]; ok {
					t.Fatalf("_meta=%v", envelope.Params.Meta)
				}
			}
		})
	}
}
