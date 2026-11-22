package network

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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// richBlock is a card an agent shows in its own chat with the show tool: the payload of a rich_block entry.
type richBlock struct {
	Title    string     `json:"title"`
	Markdown string     `json:"markdown,omitempty"`
	Table    *richTable `json:"table,omitempty"`
	Image    *richImage `json:"image,omitempty"`
}

type richTable struct {
	Columns []string   `json:"columns" jsonschema:"Column headings."`
	Rows    [][]string `json:"rows" jsonschema:"Rows of cells, one per column."`
}

type richImage struct {
	Blob string `json:"blob"`
	Mime string `json:"mime,omitempty"`
}

const (
	richTitleMax    = 200
	richMarkdownMax = 32 << 10
	richColumnsMax  = 12
	richRowsMax     = 100
	richCellMax     = 500
)

func (block richBlock) valid() error {
	title := strings.TrimSpace(block.Title)
	switch {
	case title == "" || utf8.RuneCountInString(title) > richTitleMax:
		return fmt.Errorf("a title of 1 to %d characters is required", richTitleMax)
	case len(block.Markdown) > richMarkdownMax:
		return fmt.Errorf("markdown holds at most %d KiB", richMarkdownMax>>10)
	case block.Markdown == "" && block.Table == nil && block.Image == nil:
		return errors.New("show needs markdown, a table or an image")
	}
	if table := block.Table; table != nil {
		if len(table.Columns) == 0 || len(table.Columns) > richColumnsMax || len(table.Rows) > richRowsMax {
			return fmt.Errorf("a table has 1 to %d columns and at most %d rows", richColumnsMax, richRowsMax)
		}
		for _, row := range append([][]string{table.Columns}, table.Rows...) {
			if len(row) > len(table.Columns) {
				return errors.New("a row has more cells than the table has columns")
			}
			for _, cell := range row {
				if len(cell) > richCellMax {
					return fmt.Errorf("a cell holds at most %d bytes", richCellMax)
				}
			}
		}
	}
	return nil
}

// receiveShow appends a card to this chat. Its image is already in the session's blob store.
func (worker *WorkerHTTP) receiveShow(w http.ResponseWriter, r *http.Request) {
	conv := worker.conversationSession()
	if conv == nil {
		http.Error(w, "conversation unavailable", http.StatusNotFound)
		return
	}
	var block richBlock
	if !readJSON(w, r, &block) {
		return
	}
	if err := block.valid(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	block.Title = strings.TrimSpace(block.Title)
	if block.Image != nil {
		sha, err := validateBlobSHA(strings.TrimPrefix(block.Image.Blob, "sha256:"))
		if err == nil {
			_, err = readSessionBlob(worker.sessionDir, sha)
		}
		if err != nil {
			http.Error(w, "image not found in this chat", http.StatusBadRequest)
			return
		}
		block.Image.Blob = "sha256:" + sha
	}
	entry := conv.log.Append("rich_block", time.Now().UnixMilli(), mustRawJSON(block))
	writeJSON(w, map[string]int64{"seq": entry.Seq})
}

func (g *Gateway) serveShow(w http.ResponseWriter, r *http.Request) {
	record, err := g.workerRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "session unavailable", http.StatusNotFound)
		return
	}
	if !authorizeACPWrite(w, r, record.Session) {
		return
	}
	proxyWorkerConversation(w, r, record, "/show")
}

type mcpShowInput struct {
	Title     string     `json:"title" jsonschema:"Card heading."`
	Markdown  string     `json:"markdown,omitempty" jsonschema:"Card body as Markdown."`
	Table     *richTable `json:"table,omitempty"`
	ImagePath string     `json:"image_path,omitempty" jsonschema:"PNG, JPEG, GIF or WebP file on this machine, absolute or relative to the chat's folder."`
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
		block := richBlock{Title: in.Title, Markdown: in.Markdown, Table: in.Table}
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
			block.Image = &richImage{Blob: "sha256:" + stored.SHA, Mime: sniffBlobMIME(data)}
		}
		if err := block.valid(); err != nil {
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
		if base64.StdEncoding.DecodedLen(len(in.Image.Data)) > maxBlobBytes {
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
		if data, err = io.ReadAll(io.LimitReader(file, maxBlobBytes+1)); err != nil {
			return nil, fmt.Errorf("image_path: %w", err)
		}
	}
	if len(data) == 0 || len(data) > maxBlobBytes {
		return nil, errors.New("an image holds 1 byte to 20 MiB")
	}
	if sniffBlobMIME(data) == "" {
		return nil, errors.New("only PNG, JPEG, GIF and WebP images can be shown")
	}
	return data, nil
}
