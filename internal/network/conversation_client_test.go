//go:build linux || darwin

package network

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestConversationPrompt(t *testing.T) {
	var captured []byte
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversation/prompt" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	home := t.TempDir()
	id := "session-prompt"
	dir := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("s", 64)
	record := WorkerRecord{ID: id, Address: strings.TrimPrefix(server.URL, "http://"), Secret: secret}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ConversationPrompt(context.Background(), home, id, "hello hub"); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(captured, &payload); err != nil || payload.Text != "hello hub" {
		t.Fatalf("payload = %q err = %v", captured, err)
	}
}

func TestConversationPromptImagesUsesWorkerRoute(t *testing.T) {
	received := make(chan acp.PromptInput, 1)
	broker := acp.NewBroker(func(_ context.Context, input acp.PromptInput) error {
		received <- input
		return nil
	}, nil, nil, nil)
	worker, err := WorkerHandler(nil, NewConversationSession(nil, broker), strings.Repeat("s", 64), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(worker)
	defer server.Close()

	home := t.TempDir()
	id := "session-prompt-images"
	dir := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(WorkerRecord{
		ID: id, Address: strings.TrimPrefix(server.URL, "http://"), Secret: strings.Repeat("s", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worker.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}

	images := []acp.PromptImage{
		{Sha: strings.Repeat("a", 64), Name: "../photo.png", Width: 123, Height: 456},
	}
	if err := ConversationPromptImages(context.Background(), home, id, "  inspect these  ", images); err != nil {
		t.Fatal(err)
	}
	got := <-received
	if got.Text != "inspect these" || len(got.Images) != 1 ||
		got.Images[0].Sha != images[0].Sha || got.Images[0].Name != "photo.png" ||
		got.Images[0].Width != 123 || got.Images[0].Height != 456 {
		t.Fatalf("worker prompt = %+v", got)
	}

	type wireImage struct {
		Sha    string `json:"sha"`
		Name   string `json:"name"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	tooMany := make([]wireImage, maxPromptImages+1)
	for i := range tooMany {
		tooMany[i] = wireImage{Sha: strings.Repeat("a", 64), Name: "photo.png"}
	}
	tooManyBody, err := json.Marshal(struct {
		Text   string      `json:"text"`
		Images []wireImage `json:"images"`
	}{Text: "inspect", Images: tooMany})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{
		[]byte(`{"text":"inspect","images":[{"sha":"bad","name":"photo.png"}]}`),
		[]byte(`{"text":"inspect","images":null}`),
		tooManyBody,
	} {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/conversation/prompt/images", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 64))
		request.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid prompt status = %d", response.StatusCode)
		}
	}
	if len(received) != 0 {
		t.Fatal("invalid image prompt reached ACP broker")
	}
}
