package network

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestShowAddsACardWithItsImageToTheCallingChat(t *testing.T) {
	h := newOAuthHarness(t)
	token := chatFixture(t, h, "session-a", "")
	directory := filepath.Join(h.gateway.options.Home, "network", "sessions", "session-a")
	conv := NewConversationSession(acp.NewEventLog(0), nil)
	worker := &WorkerHTTP{sessionDir: directory, conversation: conv}
	routes := http.NewServeMux()
	worker.registerConversation(routes)
	mux := httptest.NewServer(routes)
	t.Cleanup(mux.Close)
	data, _ := json.Marshal(WorkerRecord{ID: "session-a", Address: strings.TrimPrefix(mux.URL, "http://"), Secret: strings.Repeat("s", 64)})
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	png := []byte("\x89PNG\r\n\x1a\nscreenshot")
	out, text, failed := structured[struct{ Shown bool }](t, h, token, "show", map[string]any{
		"title":    "Nightly e2e",
		"markdown": "**214** tests",
		"table":    map[string]any{"columns": []string{"Project", "Failed"}, "rows": [][]string{{"chromium", "2"}}},
		"image":    map[string]any{"data": base64.StdEncoding.EncodeToString(png), "mime_type": "image/png"},
	})
	if failed || !out.Shown {
		t.Fatal("show:", text)
	}
	entries := conv.log.Snapshot()
	if len(entries) != 1 || entries[0].Kind != "rich_block" {
		t.Fatalf("log = %+v", entries)
	}
	var block richBlock
	if err := json.Unmarshal(entries[0].Payload, &block); err != nil {
		t.Fatal(err)
	}
	stored, err := readSessionBlob(directory, strings.TrimPrefix(block.Image.Blob, "sha256:"))
	if err != nil || string(stored) != string(png) || block.Image.Mime != "image/png" || block.Title != "Nightly e2e" || block.Table.Rows[0][1] != "2" {
		t.Fatalf("card %+v, blob %q %v", block, stored, err)
	}

	// A file the agent names is read on this machine; anything but an image is refused.
	notes := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(notes, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, text, failed := structured[struct{ Shown bool }](t, h, token, "show", map[string]any{"title": "x", "image_path": notes}); !failed || !strings.Contains(text, "only PNG") {
		t.Fatal("non-image shown:", text)
	}
	for _, bad := range []map[string]any{{"title": ""}, {"title": "empty"}} {
		if _, _, failed := structured[struct{ Shown bool }](t, h, token, "show", bad); !failed {
			t.Fatalf("accepted %v", bad)
		}
	}
	if entries := conv.log.Snapshot(); len(entries) != 1 {
		t.Fatalf("refused cards were logged: %d", len(entries))
	}
}
