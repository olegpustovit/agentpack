package network

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// RichBlock is a card an agent shows in its own chat with the show tool: the payload of a rich_block entry.
type RichBlock struct {
	Title    string     `json:"title"`
	Markdown string     `json:"markdown,omitempty"`
	Table    *RichTable `json:"table,omitempty"`
	Image    *RichImage `json:"image,omitempty"`
}

type RichTable struct {
	Columns []string   `json:"columns" jsonschema:"Column headings."`
	Rows    [][]string `json:"rows" jsonschema:"Rows of cells, one per column."`
}

type RichImage struct {
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

func (block RichBlock) Valid() error {
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
	var block RichBlock
	if !ReadJSON(w, r, &block) {
		return
	}
	if err := block.Valid(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	block.Title = strings.TrimSpace(block.Title)
	if block.Image != nil {
		sha, err := ValidateBlobSHA(strings.TrimPrefix(block.Image.Blob, "sha256:"))
		if err == nil {
			_, err = ReadSessionBlob(worker.SessionDir, sha)
		}
		if err != nil {
			http.Error(w, "image not found in this chat", http.StatusBadRequest)
			return
		}
		block.Image.Blob = "sha256:" + sha
	}
	entry := conv.Log.Append("rich_block", time.Now().UnixMilli(), MustRawJSON(block))
	WriteJSON(w, map[string]int64{"seq": entry.Seq})
}
