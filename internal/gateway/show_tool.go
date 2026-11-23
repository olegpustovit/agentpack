package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
)

func (g *Gateway) serveShow(w http.ResponseWriter, r *http.Request) {
	record, err := g.workerRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "session unavailable", http.StatusNotFound)
		return
	}
	if !session.AuthorizeWrite(w, r, record.Session) {
		return
	}
	network.ProxyWorkerConversation(w, r, record, "/show")
}

type mcpShowInput struct {
	Title     string             `json:"title" jsonschema:"Card heading."`
	Markdown  string             `json:"markdown,omitempty" jsonschema:"Card body as Markdown."`
	Table     *network.RichTable `json:"table,omitempty"`
	ImagePath string             `json:"image_path,omitempty" jsonschema:"PNG, JPEG, GIF or WebP file on this machine, absolute or relative to the chat's folder."`
	Image     *struct {
		Data     string `json:"data" jsonschema:"Base64 image bytes."`
		MimeType string `json:"mime_type,omitempty"`
	} `json:"image,omitempty" jsonschema:"Inline image, when there is no file."`
}

func (m *GatewayMCP) showTool(server *mcp.Server) {
	addTool(server, "show", "Show a card in this chat for the person: a title with Markdown, a table and/or an image (a screenshot, a chart). Use it for results worth seeing at a glance; it does not reach the agent or other chats.", mutating, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpShowInput) (*mcp.CallToolResult, struct {
		Shown bool `json:"shown"`
	}, error) {
		var out struct {
			Shown bool `json:"shown"`
		}
		self, _, err := m.caller(ctx)
		if errors.Is(err, errNotAChat) {
			return nil, out, errors.New("show is for a chat's agent: it adds a card to that chat")
		}
		if err != nil {
			return nil, out, err
		}
		block := network.RichBlock{Title: in.Title, Markdown: in.Markdown, Table: in.Table}
		if in.ImagePath != "" || in.Image != nil {
			data, err := showImageBytes(self.Directory, in)
			if err != nil {
				return nil, out, err
			}
			stored, err := callAPI[struct {
				SHA string `json:"sha"`
			}](ctx, m, "", http.MethodPost, "/api/sessions/"+url.PathEscape(self.ID)+"/conversation/blobs", data)
			if err != nil {
				return nil, out, fmt.Errorf("image: %w", err)
			}
			block.Image = &network.RichImage{Blob: "sha256:" + stored.SHA, Mime: network.SniffBlobMIME(data)}
		}
		if err := block.Valid(); err != nil {
			return nil, out, err
		}
		if _, err := m.do(ctx, "", http.MethodPost, "/api/sessions/"+url.PathEscape(self.ID)+"/conversation/show", block); err != nil {
			return nil, out, err
		}
		out.Shown = true
		return nil, out, nil
	})
}

// showImageBytes reads the image the agent named; only images the blob store accepts are shown.
func showImageBytes(folder string, in mcpShowInput) ([]byte, error) {
	var data []byte
	if in.Image != nil {
		if base64.StdEncoding.DecodedLen(len(in.Image.Data)) > network.MaxBlobBytes {
			return nil, errors.New("an image holds at most 20 MiB")
		}
		decoded, err := base64.StdEncoding.DecodeString(in.Image.Data)
		if err != nil {
			return nil, errors.New("image data is not base64")
		}
		data = decoded
	} else {
		name := in.ImagePath
		if !filepath.IsAbs(name) {
			name = filepath.Join(folder, name)
		}
		file, err := os.Open(name)
		if err != nil {
			return nil, fmt.Errorf("image_path: %w", err)
		}
		defer file.Close()
		if data, err = io.ReadAll(io.LimitReader(file, network.MaxBlobBytes+1)); err != nil {
			return nil, fmt.Errorf("image_path: %w", err)
		}
	}
	if len(data) == 0 || len(data) > network.MaxBlobBytes {
		return nil, errors.New("an image holds 1 byte to 20 MiB")
	}
	if network.SniffBlobMIME(data) == "" {
		return nil, errors.New("only PNG, JPEG, GIF and WebP images can be shown")
	}
	return data, nil
}
