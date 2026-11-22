package acp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkacp "github.com/coder/acp-go-sdk"
)

func TestImagesMoveToTheBlobStoreAndNeverReachTheLog(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nimage"))
	var stored [][]byte
	store := func(data []byte) (string, error) {
		stored = append(stored, data)
		return strings.Repeat("a", 64), nil
	}
	chunk := sdkacp.SessionUpdate{AgentMessageChunk: &sdkacp.SessionUpdateAgentMessageChunk{Content: sdkacp.ImageBlock(png, "image/png")}}
	kind, payload := sessionUpdateLogEntry(chunk, store)
	if kind != "agent_message_chunk" || string(payload) != `{"image":{"blob":"sha256:`+strings.Repeat("a", 64)+`","mime":"image/png"}}` {
		t.Fatalf("chunk = %s %s", kind, payload)
	}

	tool := json.RawMessage(`{"toolCallId":"s1","status":"completed","content":[` +
		`{"type":"content","content":{"type":"image","data":"` + png + `","mimeType":"image/png"}},` +
		`{"type":"content","content":{"type":"text","text":"ok"}},` +
		`{"type":"content","content":{"type":"resource","resource":{"uri":"file:///r/notes.md","text":"# notes"}}},` +
		`{"type":"content","content":{"type":"resource_link","uri":"https://example.com/report.html","name":"report"}},` +
		`{"type":"diff","path":"a.go","newText":"x"},{"type":"terminal","terminalId":"t1"}]}`)
	got := string(toolContentPayload(tool, store))
	for _, want := range []string{
		`{"type":"image","blob":"sha256:` + strings.Repeat("a", 64) + `","mimeType":"image/png"}`,
		`{"type":"text","text":"ok"}`,
		`{"type":"text","text":"# notes"}`,
		`{"type":"resource_link","uri":"https://example.com/report.html","name":"report"}`,
		`{"newText":"x","path":"a.go","type":"diff"}`,
		`{"terminalId":"t1","type":"terminal"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("tool payload lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, png) || len(stored) != 2 {
		t.Fatalf("inline data kept or not stored: %d stored, %s", len(stored), got)
	}
}

func TestImagesThatCannotBeStoredBecomeText(t *testing.T) {
	block := json.RawMessage(`{"type":"image","data":"aGk=","mimeType":"image/svg+xml"}`)
	failing := func([]byte) (string, error) { return "", errors.New("unsupported image type") }
	for _, tc := range []struct {
		store ImageStore
		raw   json.RawMessage
		want  string
	}{
		{nil, block, "[image not shown]"},
		{failing, block, "[image not shown: unsupported image type]"},
		{failing, json.RawMessage(`{"type":"image","data":"%%%","mimeType":"image/png"}`), "[image not shown: invalid data]"},
	} {
		if got := string(chunkPayload(tc.raw, tc.store)); got != `{"text":{"text":"`+tc.want+`"}}` {
			t.Errorf("chunk = %s, want %s", got, tc.want)
		}
	}
}

func TestMergedChunksKeepEveryAttachment(t *testing.T) {
	merged, err := mergeTextChunks(json.RawMessage(`{"text":{"text":"see "},"image":{"blob":"sha256:a"}}`), json.RawMessage(`{"image":{"blob":"sha256:b"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(merged); got != `{"text":{"text":"see "},"images":[{"blob":"sha256:a"},{"blob":"sha256:b"}]}` {
		t.Fatalf("merged = %s", got)
	}
	plain, _ := mergeTextChunks(json.RawMessage(`{"text":{"text":"a"}}`), json.RawMessage(`{"text":{"text":"b"}}`))
	if string(plain) != `{"text":{"text":"ab"}}` {
		t.Fatalf("plain = %s", plain)
	}
}

// rich.jsonl is shared with the UI reducer test: both must fold it into the same items.
func TestReduceRichFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "acp", "rich.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []LogEntry
	for line := range splitLines(data) {
		if line == "" {
			continue
		}
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	var kinds []string
	state := ReduceLog(entries)
	for _, item := range state.Items {
		kinds = append(kinds, item.Kind)
	}
	if strings.Join(kinds, ",") != "agent_message_chunk,tool,rich_block" {
		t.Fatalf("items = %v", kinds)
	}
	message := string(state.Items[0].Data)
	if !strings.Contains(message, `"text":"Here is the failing page:"`) || !strings.Contains(message, `"images":[{"blob":"sha256:5e5e`) {
		t.Fatalf("message = %s", message)
	}
	if card := string(state.Items[2].Data); !strings.Contains(card, `"title":"Nightly e2e"`) {
		t.Fatalf("card = %s", card)
	}
	if again := string(toolContentPayload(entries[3].Payload, nil)); again != string(entries[3].Payload) {
		t.Fatalf("a stored image was rewritten: %s", again)
	}
}

// codex-acp forwards an MCP tool's result raw in rawOutput (codex-cli 0.169), images included.
// They go to the blob store and show as content blocks, unless the adapter already sent one.
func TestRawMCPResultImagesShowAsContent(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nraw"))
	var stored int
	store := func([]byte) (string, error) { stored++; return strings.Repeat("b", 64), nil }
	update := json.RawMessage(`{"toolCallId":"exec-1","status":"completed","rawOutput":{"error":null,"result":{"_meta":null,"content":[` +
		`{"data":"` + png + `","mimeType":"image/png","type":"image"},{"text":"Screenshot captured.","type":"text"}]}}}`)
	got := string(toolContentPayload(update, store))
	blob := `"blob":"sha256:` + strings.Repeat("b", 64) + `"`
	if strings.Contains(got, png) || stored != 1 {
		t.Fatalf("inline data kept or not stored once (%d): %s", stored, got)
	}
	var fields struct {
		Content   []map[string]json.RawMessage `json:"content"`
		RawOutput json.RawMessage              `json:"rawOutput"`
	}
	if err := json.Unmarshal([]byte(got), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields.Content) != 1 || !strings.Contains(string(fields.Content[0]["content"]), blob) || string(fields.Content[0]["type"]) != `"content"` {
		t.Fatalf("image not shown as content: %s", got)
	}
	if !strings.Contains(string(fields.RawOutput), blob) || !strings.Contains(string(fields.RawOutput), "Screenshot captured.") {
		t.Fatalf("rawOutput lost the reference or the text: %s", fields.RawOutput)
	}
	// An adapter that already shows the image as content (claude-agent-acp) gets no duplicate.
	both := json.RawMessage(`{"toolCallId":"t","content":[{"type":"content","content":{"type":"image","blob":"sha256:` + strings.Repeat("c", 64) + `","mimeType":"image/png"}}],` +
		`"rawOutput":{"content":[{"type":"image","data":"` + png + `","mimeType":"image/png"}]}}`)
	if got := string(toolContentPayload(both, store)); strings.Count(got, `"type":"content"`) != 1 || strings.Contains(got, png) {
		t.Fatalf("duplicate or inline image: %s", got)
	}
	// No images: the payload is untouched.
	plain := json.RawMessage(`{"toolCallId":"t","rawOutput":{"result":{"content":[{"type":"text","text":"ok"}]}}}`)
	if got := toolContentPayload(plain, store); string(got) != string(plain) {
		t.Fatalf("payload without images changed: %s", got)
	}
}
