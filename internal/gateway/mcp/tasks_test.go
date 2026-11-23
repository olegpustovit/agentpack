package mcp

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
)

func TestPromptSenderSurvivesTheQueueAndNamesTheChat(t *testing.T) {
	from := &acp.PromptFrom{Session: "session-a", Task: "t_0123456789abcdef", Kind: "ask"}
	input, err := network.PromptInputFromLog(network.QueuedPromptPayload(network.QueuedPrompt{ID: "q", Input: acp.PromptInput{Text: "hi", From: from}}))
	if err != nil || input.From == nil || *input.From != *from {
		t.Fatalf("%+v %v", input.From, err)
	}
	message := agentMessage{Task: from.Task, Kind: "ask", FromSession: "session-a", FromAgent: "claude", Text: "Review   auth.go"}
	if title := network.TitleFromPrompt(envelope(message)); title != "Review auth.go" {
		t.Fatal(title)
	}
	// A person cannot set a sender: the worker reads it only from the gateway's header.
	request := httptest.NewRequest("POST", "/conversation/prompt", strings.NewReader(`{"text":"hi","from":{"session":"x","task":"t"}}`))
	if prompt, _, ok := network.ReadPromptRequest(httptest.NewRecorder(), request); ok && prompt.From != nil {
		t.Fatalf("sender from the body: %+v", prompt.From)
	}
}
