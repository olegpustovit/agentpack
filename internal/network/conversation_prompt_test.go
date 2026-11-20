package network

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestPromptLogPayloadIncludesImage(t *testing.T) {
	raw := promptLogPayload(acp.PromptInput{
		Text:  "hi",
		Image: &acp.PromptImage{Sha: "abc", Mime: "image/png", Width: 10, Height: 20},
	})
	body := string(raw)
	for _, part := range []string{`"text"`, `"image"`, `sha256:abc`} {
		if !strings.Contains(body, part) {
			t.Fatalf("missing %q in %s", part, body)
		}
	}
}
func TestPromptImageMIMEUsesStoredBytes(t *testing.T) {
	sessionDir := t.TempDir()
	data := []byte("\x89PNG\r\n\x1a\nvalid-prefix")
	sha, err := storeSessionBlob(sessionDir, data)
	if err != nil {
		t.Fatal(err)
	}
	blocks, normalized, err := promptContentBlocks(sessionDir, acp.PromptInput{
		Image: &acp.PromptImage{Sha: sha, Mime: "image/svg+xml"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Image == nil || blocks[0].Image.MimeType != "image/png" {
		t.Fatalf("blocks = %+v", blocks)
	}
	if normalized.Image == nil || normalized.Image.Mime != "image/png" {
		t.Fatalf("normalized prompt = %+v", normalized.Image)
	}
	if strings.Contains(string(promptLogPayload(normalized)), "mime") {
		t.Fatal("client MIME must not be written to the conversation log")
	}
}

func TestPromptResourceLinkUsesStoredBlobURIWithoutLoggingLocalPath(t *testing.T) {
	sessionDir := t.TempDir()
	data := []byte("\x89PNG\r\n\x1a\nresource-link-test")
	sha, err := storeSessionBlob(sessionDir, data)
	if err != nil {
		t.Fatal(err)
	}
	input := acp.PromptInput{ResourceLink: &acp.PromptResourceLink{Sha: sha, Name: "../\x1b[31mphoto.png"}}
	blocks, normalized, err := promptContentBlocks(sessionDir, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].ResourceLink == nil {
		t.Fatalf("blocks = %+v", blocks)
	}
	link := blocks[0].ResourceLink
	if link.Name != "photo.png" || link.MimeType == nil || *link.MimeType != "image/png" || !strings.HasPrefix(link.Uri, "file:") || !strings.HasSuffix(link.Uri, sha) {
		t.Fatalf("resource link = %+v", link)
	}
	if normalized.ResourceLink == nil || normalized.ResourceLink.Sha != sha || normalized.ResourceLink.Name != "photo.png" {
		t.Fatalf("normalized link = %+v", normalized.ResourceLink)
	}
	log := string(promptLogPayload(normalized))
	if !strings.Contains(log, "sha256:"+sha) || strings.Contains(log, sessionDir) || strings.Contains(log, "\x1b") {
		t.Fatalf("resource log payload = %q", log)
	}
}

func TestPromptRejectsBothImageBlockKinds(t *testing.T) {
	_, _, err := promptContentBlocks(t.TempDir(), acp.PromptInput{
		Image:        &acp.PromptImage{Sha: strings.Repeat("a", 64)},
		ResourceLink: &acp.PromptResourceLink{Sha: strings.Repeat("b", 64), Name: "x.png"},
	}, true)
	if err == nil {
		t.Fatal("expected mixed image/resource-link rejection")
	}
}

func TestPromptMultipleImagesFollowAgentCapability(t *testing.T) {
	sessionDir := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\nfirst")
	jpeg := []byte("\xff\xd8\xffsecond")
	pngSHA, err := storeSessionBlob(sessionDir, png)
	if err != nil {
		t.Fatal(err)
	}
	jpegSHA, err := storeSessionBlob(sessionDir, jpeg)
	if err != nil {
		t.Fatal(err)
	}
	input := acp.PromptInput{
		Text: "look",
		Images: []acp.PromptImage{
			{Sha: pngSHA, Name: "../first.png", Mime: "image/svg+xml", Width: 10, Height: 20},
			{Sha: jpegSHA, Name: `C:\private\second.jpg`},
		},
	}
	blocks, normalized, err := promptContentBlocks(sessionDir, input, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 || blocks[0].Text == nil ||
		blocks[1].Image == nil || blocks[1].Image.MimeType != "image/png" ||
		blocks[1].Image.Data != base64.StdEncoding.EncodeToString(png) ||
		blocks[2].Image == nil || blocks[2].Image.MimeType != "image/jpeg" {
		t.Fatalf("image-capable blocks = %+v", blocks)
	}
	if normalized.Images[0].Sha != pngSHA || normalized.Images[1].Sha != jpegSHA ||
		normalized.Images[0].Name != "first.png" || normalized.Images[1].Name != "second.jpg" ||
		normalized.Images[0].Mime != "image/png" || normalized.Images[1].Mime != "image/jpeg" {
		t.Fatalf("normalized images = %+v", normalized.Images)
	}

	imageLog := string(promptLogPayload(normalized))
	if !strings.Contains(imageLog, `"kind":"image"`) ||
		!strings.Contains(imageLog, "sha256:"+pngSHA) || !strings.Contains(imageLog, "sha256:"+jpegSHA) ||
		!strings.Contains(imageLog, `"width":10`) || !strings.Contains(imageLog, `"height":20`) {
		t.Fatalf("image prompt log = %q", imageLog)
	}

	blocks, normalized, err = promptContentBlocks(sessionDir, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 || blocks[0].Text == nil ||
		blocks[1].ResourceLink == nil || blocks[1].ResourceLink.Name != "first.png" ||
		blocks[1].ResourceLink.MimeType == nil || *blocks[1].ResourceLink.MimeType != "image/png" ||
		!strings.HasSuffix(blocks[1].ResourceLink.Uri, pngSHA) ||
		blocks[2].ResourceLink == nil || blocks[2].ResourceLink.Name != "second.jpg" ||
		blocks[2].ResourceLink.MimeType == nil || *blocks[2].ResourceLink.MimeType != "image/jpeg" ||
		!strings.HasSuffix(blocks[2].ResourceLink.Uri, jpegSHA) {
		t.Fatalf("resource-link blocks = %+v", blocks)
	}
	log := string(promptLogPayload(normalized))
	if !strings.Contains(log, `"images"`) || !strings.Contains(log, `"kind":"resource_link"`) ||
		!strings.Contains(log, "first.png") || !strings.Contains(log, "second.jpg") ||
		strings.Contains(log, sessionDir) || strings.Contains(log, `C:\private`) {
		t.Fatalf("image prompt log = %q", log)
	}
}

func TestPromptMultipleImagesAreBounded(t *testing.T) {
	images := make([]acp.PromptImage, maxPromptImages+1)
	_, _, err := promptContentBlocks(t.TempDir(), acp.PromptInput{Images: images}, false)
	if err == nil {
		t.Fatal("expected excessive image count rejection")
	}
}

func TestConversationSessionFollowsAgentOptionAndModeUpdates(t *testing.T) {
	log := acp.NewEventLog(0)
	session := NewConversationSession(log, nil)
	session.SetConfigOptions([]byte(`[{"id":"mode","category":"mode","type":"select","currentValue":"default"}]`))
	session.SetModes([]byte(`{"currentModeId":"default"}`))
	log.Append("current_mode_update", 1, []byte(`{"sessionUpdate":"current_mode_update","currentModeId":"bypassPermissions"}`))
	if got := string(session.ConfigJSON()); strings.Count(got, "bypassPermissions") != 2 {
		t.Fatalf("mode update not applied to options and modes: %s", got)
	}
	log.Append("config_option_update", 2, []byte(`{"configOptions":[{"id":"mode","category":"mode","type":"select","currentValue":"plan"}]}`))
	if got := string(session.ConfigJSON()); !strings.Contains(got, `"currentValue":"plan"`) {
		t.Fatalf("option update not applied: %s", got)
	}
	log.Append("config_option_update", 3, []byte(`{"_subagent":"child","configOptions":[{"id":"model","category":"model","type":"select","currentValue":"haiku"}]}`))
	log.Append("current_mode_update", 4, []byte(`{"_subagent":"child","sessionUpdate":"current_mode_update","currentModeId":"default"}`))
	if got := string(session.ConfigJSON()); strings.Contains(got, "haiku") || !strings.Contains(got, `"currentValue":"plan"`) {
		t.Fatalf("subagent update replaced the chat's options: %s", got)
	}
}

func TestConversationSessionReportsTheAgentsTitle(t *testing.T) {
	log := acp.NewEventLog(0)
	session := NewConversationSession(log, nil)
	var titles []string
	session.mu.Lock()
	session.titleChange = func(title string) { titles = append(titles, title) }
	session.mu.Unlock()
	log.Append("session_info_update", 1, []byte(`{"sessionUpdate":"session_info_update","title":"  Fix the reattach offset "}`))
	log.Append("session_info_update", 2, []byte(`{"sessionUpdate":"session_info_update","updatedAt":"2026-11-18T00:00:00Z"}`))
	if len(titles) != 1 || titles[0] != "Fix the reattach offset" {
		t.Fatalf("titles = %q", titles)
	}
}

func TestHandoffContextCarriesTheChatOnlyOnceAnAgentReplied(t *testing.T) {
	entry := func(seq int64, kind, body string) acp.LogEntry {
		return acp.LogEntry{Seq: seq, Kind: kind, Payload: []byte(body)}
	}
	prompt := entry(1, "prompt", `{"text":{"text":"fix the login"}}`)
	if got := handoffContext([]acp.LogEntry{prompt, entry(2, "notice", `{"title":"Could not start claude"}`)}); got != "" {
		t.Fatalf("unanswered chat handed off %q", got)
	}
	got := handoffContext([]acp.LogEntry{
		prompt,
		entry(2, "agent_thought_chunk", `{"text":{"text":"secret thinking"}}`),
		entry(3, "agent_message_chunk", `{"text":{"text":"Looking"}}`),
		entry(4, "agent_message_chunk", `{"text":{"text":" now."}}`),
		entry(5, "tool_call", `{"toolCallId":"t1","title":"Read login.go"}`),
		entry(6, "agent_message_chunk", `{"text":{"text":"from a helper"},"_subagent":"s1"}`),
	})
	for _, want := range []string{"User: fix the login", "Assistant: Looking now.", "Assistant used a tool: Read login.go", "<previous_transcript>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("handoff missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "secret thinking") || strings.Contains(got, "from a helper") {
		t.Fatalf("handoff carries thoughts:\n%s", got)
	}
}
