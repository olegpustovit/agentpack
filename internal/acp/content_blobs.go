package acp

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"path"
	"strings"
)

// ImageStore saves image bytes in the session's blob store and returns their sha256 in hex.
type ImageStore func([]byte) (string, error)

// maxInlineImage bounds the base64 text decoded for one block; the blob store caps the bytes at 20 MiB.
const maxInlineImage = 28 << 20

// contentBlock is one ACP ContentBlock as JSON, the form both agent chunks and tool content carry.
type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
	Title    string `json:"title,omitempty"`
	Resource *struct {
		URI      string `json:"uri"`
		MimeType string `json:"mimeType,omitempty"`
		Text     string `json:"text,omitempty"`
		Blob     string `json:"blob,omitempty"`
	} `json:"resource,omitempty"`
}

// shownBlock is what the log keeps of a block: text, an image in the blob store, or a link.
// Inline base64 never reaches the log, so replays stay small and images load from /blobs/{sha}.
type shownBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
}

func showBlock(block contentBlock, store ImageStore) shownBlock {
	switch block.Type {
	case "image":
		return storedImage(block.Data, block.MimeType, store)
	case "audio":
		return shownBlock{Type: "text", Text: "[audio not shown]"}
	case "resource_link":
		return shownBlock{Type: "resource_link", URI: block.URI, Name: cmp.Or(block.Title, block.Name, baseName(block.URI)), MimeType: block.MimeType}
	case "resource":
		if block.Resource == nil {
			return shownBlock{Type: "text"}
		}
		resource := block.Resource
		if resource.Text != "" {
			return shownBlock{Type: "text", Text: resource.Text}
		}
		if resource.Blob != "" && strings.HasPrefix(resource.MimeType, "image/") {
			return storedImage(resource.Blob, resource.MimeType, store)
		}
		return shownBlock{Type: "resource_link", URI: resource.URI, Name: baseName(resource.URI), MimeType: resource.MimeType}
	}
	return shownBlock{Type: "text", Text: block.Text}
}

// storedImage moves base64 image data into the blob store. Anything it cannot store becomes a
// short text in its place, so an image never fails the turn.
func storedImage(data, mime string, store ImageStore) shownBlock {
	if store == nil {
		return shownBlock{Type: "text", Text: "[image not shown]"}
	}
	if len(data) > maxInlineImage {
		return shownBlock{Type: "text", Text: "[image not shown: larger than 20 MiB]"}
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return shownBlock{Type: "text", Text: "[image not shown: invalid data]"}
	}
	sha, err := store(raw)
	if err != nil {
		return shownBlock{Type: "text", Text: "[image not shown: " + err.Error() + "]"}
	}
	return shownBlock{Type: "image", Blob: "sha256:" + sha, MimeType: mime}
}

// chunkPayload is the log form of an agent or user chunk: {"text":{...}}, {"image":{...}} or {"resource_link":{...}},
// the shapes user attachments already use.
func chunkPayload(raw json.RawMessage, store ImageStore) json.RawMessage {
	var block contentBlock
	if json.Unmarshal(raw, &block) != nil {
		return mustJSON(map[string]any{"text": map[string]string{"text": ""}})
	}
	shown := showBlock(block, store)
	switch shown.Type {
	case "image":
		return mustJSON(map[string]any{"image": map[string]string{"blob": shown.Blob, "mime": shown.MimeType}})
	case "resource_link":
		return mustJSON(map[string]any{"resource_link": map[string]string{"uri": shown.URI, "name": shown.Name, "mime": shown.MimeType}})
	}
	return mustJSON(map[string]any{"text": map[string]string{"text": shown.Text}})
}

// toolContentPayload rewrites the content blocks of a tool call or update the same way; diffs,
// terminals and other entries pass through unchanged.
func toolContentPayload(payload json.RawMessage, store ImageStore) json.RawMessage {
	if !bytes.Contains(payload, []byte(`"content"`)) {
		return payload
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return payload
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(fields["content"], &entries) != nil {
		return payload
	}
	changed := false
	for _, entry := range entries {
		if string(entry["type"]) != `"content"` || len(entry["content"]) == 0 {
			continue
		}
		var block contentBlock
		// Text, links and images already in the blob store stay as they are.
		if json.Unmarshal(entry["content"], &block) != nil || block.Type == "text" || block.Type == "resource_link" || block.Type == "image" && block.Data == "" {
			continue
		}
		entry["content"] = mustJSON(showBlock(block, store))
		changed = true
	}
	if !changed {
		return payload
	}
	fields["content"] = mustJSON(entries)
	return mustJSON(fields)
}

func baseName(uri string) string {
	if uri == "" {
		return ""
	}
	return path.Base(uri)
}
