package cursor

import (
	"encoding/json"
	"testing"
)

func TestAdaptAgentLine(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		wantLog     string
		wantDropped bool
	}{
		{
			name:        "ask_question",
			line:        `{"jsonrpc":"2.0","id":"question-1","method":"cursor/ask_question","params":{"questions":[{"id":"q1"}]}}`,
			wantLog:     "ask_question",
			wantDropped: false,
		},
		{
			name:        "create_plan",
			line:        `{"jsonrpc":"2.0","id":7,"method":"cursor/create_plan","params":{"title":"Plan"}}`,
			wantLog:     "create_plan",
			wantDropped: false,
		},
		{
			name:        "update_todos",
			line:        `{"jsonrpc":"2.0","method":"cursor/update_todos","params":{"todos":[{"id":"1","status":"done"}]}}`,
			wantLog:     "update_todos",
			wantDropped: true,
		},
		{
			name:        "task",
			line:        `{"jsonrpc":"2.0","method":"cursor/task","params":{"description":"scout"}}`,
			wantLog:     "task",
			wantDropped: true,
		},
		{
			name:        "generate_image",
			line:        `{"jsonrpc":"2.0","method":"cursor/generate_image","params":{"prompt":"icon"}}`,
			wantLog:     "generate_image",
			wantDropped: true,
		},
		{
			name: "passthrough",
			line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adaptAgentLine([]byte(tt.line))
			if tt.wantLog == "" {
				if len(got.Log) != 0 {
					t.Fatalf("log=%v", got.Log)
				}
			} else if len(got.Log) != 1 || got.Log[0].Kind != tt.wantLog {
				t.Fatalf("log=%v", got.Log)
			}
			dropped := got.Forward != nil && len(got.Forward) == 0
			if dropped != tt.wantDropped {
				t.Fatalf("forward=%q wantDropped=%v", got.Forward, tt.wantDropped)
			}
		})
	}
}

func TestAdaptAgentLineRewritesBlockingRequests(t *testing.T) {
	for _, test := range []struct {
		method string
		id     string
	}{
		{method: "cursor/ask_question", id: `"question-1"`},
		{method: "cursor/create_plan", id: `7`},
	} {
		t.Run(test.method, func(t *testing.T) {
			line := []byte(`{"jsonrpc":"2.0","id":` + test.id + `,"method":"` + test.method + `","params":{"plan":"Review"}}`)
			got := adaptAgentLine(line)
			if len(got.Forward) == 0 {
				t.Fatal("blocking request was dropped")
			}
			var forwarded struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Method  string          `json:"method"`
				Params  json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(got.Forward, &forwarded); err != nil {
				t.Fatal(err)
			}
			wantMethod := "_agentpack/" + test.method
			if forwarded.JSONRPC != "2.0" || string(forwarded.ID) != test.id || forwarded.Method != wantMethod {
				t.Fatalf("forwarded = %+v", forwarded)
			}
			if !json.Valid(forwarded.Params) {
				t.Fatalf("invalid forwarded params: %s", forwarded.Params)
			}
		})
	}
}

func TestAdaptAgentLinePayload(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","id":"question-1","method":"cursor/ask_question","params":{"questions":[{"id":"q1","prompt":"Pick?"}]}}`)
	got := adaptAgentLine(line)
	if len(got.Log) != 1 {
		t.Fatal(got.Log)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Log[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	questions, ok := payload["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("payload=%v", payload)
	}
}
