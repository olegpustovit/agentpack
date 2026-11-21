package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
)

type revertFile struct {
	path     string // as recorded by the agent
	resolved string
	original *string // oldText of the turn's first edit; nil = the turn created the file
	final    string  // newText of the turn's last edit
}

// Turn numbering is shared with ui/src/features/conversation/diff-summary.ts:
// reduce the log with acp.ReduceLog, then a turn starts at every reduced item of
// kind "prompt" or "user_message_chunk" (1-based); a tool item belongs to the latest turn.
func revertPlan(log []acp.LogEntry, turn int) ([]revertFile, int, error) {
	var files []revertFile
	index := map[string]int{}
	n := 0
	for _, item := range acp.ReduceLog(log).Items {
		if item.Kind == "prompt" || item.Kind == "user_message_chunk" {
			n++
		}
		if item.Kind != "tool" || n != turn {
			continue
		}
		var tool struct {
			Content []struct {
				Type    string  `json:"type"`
				Path    string  `json:"path"`
				OldText *string `json:"oldText"`
				NewText *string `json:"newText"`
			} `json:"content"`
		}
		if json.Unmarshal(item.Data, &tool) != nil {
			continue
		}
		for _, block := range tool.Content {
			if block.Type != "diff" || block.Path == "" {
				continue
			}
			if block.NewText == nil {
				return nil, n, errors.New("the change is missing its new text")
			}
			if i, ok := index[block.Path]; ok {
				files[i].final = *block.NewText
				continue
			}
			index[block.Path] = len(files)
			files = append(files, revertFile{path: block.Path, original: block.OldText, final: *block.NewText})
		}
	}
	return files, n, nil
}

// RevertTurn undoes the file edits of one turn, or changes nothing and says why.
// The status is the HTTP code for a refusal.
func (session *ConversationSession) RevertTurn(turn int) (int, int, error) {
	if session.workspace == "" {
		return 0, http.StatusConflict, errors.New("session has no working directory")
	}
	session.writes.RLock()
	defer session.writes.RUnlock()
	if !session.promptMu.TryLock() { // blocks a new prompt while files change
		return 0, http.StatusConflict, errors.New("a turn is running")
	}
	defer session.promptMu.Unlock()
	if session.ConversationState() != "idle" {
		return 0, http.StatusConflict, errors.New("a turn is running")
	}
	files, n, err := revertPlan(session.log.Snapshot(), turn)
	if err != nil {
		return 0, http.StatusConflict, err
	}
	if turn < 1 || turn > n {
		return 0, http.StatusBadRequest, errors.New("unknown turn")
	}
	if len(files) == 0 {
		return 0, http.StatusConflict, errors.New("this turn changed no files")
	}
	for i := range files {
		f := &files[i]
		resolved, err := acp.ResolveWorkspacePath(session.workspace, nil, f.path)
		if errors.Is(err, fs.ErrNotExist) {
			return 0, http.StatusConflict, fmt.Errorf("%s changed since this turn", f.path)
		}
		if err != nil {
			return 0, http.StatusBadRequest, fmt.Errorf("%s is outside the working directory", f.path)
		}
		f.resolved = resolved
		current, err := os.ReadFile(resolved)
		if err != nil || string(current) != f.final {
			return 0, http.StatusConflict, fmt.Errorf("%s changed since this turn", f.path)
		}
	}
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		var err error
		if f.original == nil {
			err = os.Remove(f.resolved)
		} else {
			err = writeFileAtomic(f.resolved, []byte(*f.original))
		}
		if err != nil {
			return 0, http.StatusInternalServerError, fmt.Errorf("could not revert %s", f.path)
		}
	}
	return len(files), 0, nil
}

func writeFileAtomic(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agentpack-revert-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (worker *WorkerHTTP) handleRevert(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Turn int `json:"turn"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	count, status, err := worker.conversationSession().RevertTurn(input.Turn)
	if err != nil {
		if event := obs.From(r.Context()); event != nil {
			event.Fail(err) // names paths only, never contents
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, map[string]int{"reverted": count})
}
