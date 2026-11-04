package network

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/olegpustovit/agentpack/internal/acp"
)

const maxPromptImages = 32

func promptLogPayload(input acp.PromptInput) json.RawMessage {
	body := map[string]any{}
	if input.Text != "" {
		body["text"] = map[string]string{"text": input.Text}
	}
	if input.Image != nil {
		img := map[string]any{"blob": "sha256:" + input.Image.Sha}
		if input.Image.Width > 0 {
			img["width"] = input.Image.Width
		}
		if input.Image.Height > 0 {
			img["height"] = input.Image.Height
		}
		body["image"] = img
	}
	if len(input.Images) > 0 {
		images := make([]map[string]any, len(input.Images))
		for i, image := range input.Images {
			item := map[string]any{
				"blob": "sha256:" + image.Sha,
				"name": image.Name,
				"kind": image.Kind,
			}
			if image.Width > 0 {
				item["width"] = image.Width
			}
			if image.Height > 0 {
				item["height"] = image.Height
			}
			images[i] = item
		}
		body["images"] = images
	}
	if input.ResourceLink != nil {
		body["resource_link"] = map[string]string{
			"blob": "sha256:" + input.ResourceLink.Sha,
			"name": input.ResourceLink.Name,
		}
	}
	return mustRawJSON(body)
}

func sanitizePromptImageName(name string) string {
	name = strings.TrimSpace(acp.StripControls(path.Base(strings.ReplaceAll(name, "\\", "/"))))
	if name == "" || name == "." || name == ".." || name == "/" {
		return "image"
	}
	return name
}

func promptContentBlocks(sessionDir string, input acp.PromptInput, imageCapable bool) ([]sdkacp.ContentBlock, acp.PromptInput, error) {
	var blocks []sdkacp.ContentBlock
	if input.Image != nil && input.ResourceLink != nil ||
		len(input.Images) > 0 && (input.Image != nil || input.ResourceLink != nil) {
		return nil, input, errors.New("prompt cannot include both an image and a resource link")
	}
	if input.Text != "" {
		blocks = append(blocks, sdkacp.TextBlock(input.Text))
	}
	if len(input.Images) > 0 {
		if len(input.Images) > maxPromptImages {
			return nil, input, errors.New("too many prompt images")
		}
		if sessionDir == "" {
			return nil, input, errors.New("blob store unavailable")
		}
		normalized := make([]acp.PromptImage, 0, len(input.Images))
		var totalBytes int64
		for _, image := range input.Images {
			sha, err := validateBlobSHA(image.Sha)
			if err != nil {
				return nil, input, err
			}
			data, err := os.ReadFile(blobPath(sessionDir, sha))
			if err != nil {
				return nil, input, err
			}
			totalBytes += int64(len(data))
			if totalBytes > maxSessionBlobBytes {
				return nil, input, errors.New("prompt image data exceeds session storage limit")
			}
			mime := sniffBlobMIME(data)
			if mime == "" {
				return nil, input, errors.New("unsupported blob type")
			}
			image.Sha, image.Mime, image.Name = sha, mime, sanitizePromptImageName(image.Name)
			if imageCapable {
				image.Kind = "image"
				normalized = append(normalized, image)
				blocks = append(blocks, sdkacp.ContentBlock{
					Image: &sdkacp.ContentBlockImage{
						Type:     "image",
						MimeType: mime,
						Data:     base64.StdEncoding.EncodeToString(data),
					},
				})
				continue
			}
			block, err := resourceLinkContentBlock(blobPath(sessionDir, sha), image.Name, mime, len(data))
			if err != nil {
				return nil, input, err
			}
			image.Kind = "resource_link"
			normalized = append(normalized, image)
			blocks = append(blocks, block)
		}
		input.Images = normalized
		return blocks, input, nil
	}
	if input.ResourceLink != nil {
		if sessionDir == "" {
			return nil, input, errors.New("blob store unavailable")
		}
		sha, err := validateBlobSHA(input.ResourceLink.Sha)
		if err != nil {
			return nil, input, err
		}
		path := blobPath(sessionDir, sha)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, input, err
		}
		mime := sniffBlobMIME(data)
		if mime == "" {
			return nil, input, errors.New("unsupported blob type")
		}
		name := strings.TrimSpace(acp.StripControls(filepath.Base(input.ResourceLink.Name)))
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = "image"
		}
		block, err := resourceLinkContentBlock(path, name, mime, len(data))
		if err != nil {
			return nil, input, err
		}
		normalized := *input.ResourceLink
		normalized.Sha, normalized.Name = sha, name
		input.ResourceLink = &normalized
		blocks = append(blocks, block)
		return blocks, input, nil
	}
	if input.Image == nil {
		if len(blocks) == 0 {
			return nil, input, errors.New("empty prompt")
		}
		return blocks, input, nil
	}
	if sessionDir == "" {
		return nil, input, errors.New("blob store unavailable")
	}
	sha, err := validateBlobSHA(input.Image.Sha)
	if err != nil {
		return nil, input, err
	}
	data, err := os.ReadFile(blobPath(sessionDir, sha))
	if err != nil {
		return nil, input, err
	}
	mime := sniffBlobMIME(data)
	if mime == "" {
		return nil, input, errors.New("unsupported blob type")
	}
	image := *input.Image
	image.Sha, image.Mime = sha, mime
	input.Image = &image
	blocks = append(blocks, sdkacp.ContentBlock{
		Image: &sdkacp.ContentBlockImage{
			Type:     "image",
			MimeType: mime,
			Data:     base64.StdEncoding.EncodeToString(data),
		},
	})
	return blocks, input, nil
}

func resourceLinkContentBlock(filePath, name, mime string, size int) (sdkacp.ContentBlock, error) {
	absolute, err := filepath.Abs(filePath)
	if err != nil {
		return sdkacp.ContentBlock{}, err
	}
	uriPath := filepath.ToSlash(absolute)
	if runtime.GOOS == "windows" {
		uriPath = "/" + uriPath
	}
	uri := (&url.URL{Scheme: "file", Path: uriPath}).String()
	block := sdkacp.ResourceLinkBlock(name, uri)
	block.ResourceLink.MimeType = &mime
	block.ResourceLink.Size = &size
	return block, nil
}
