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
