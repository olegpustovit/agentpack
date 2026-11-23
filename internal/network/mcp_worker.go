package network

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/olegpustovit/agentpack/internal/session"
)

type TerminalOutput struct {
	Offset     uint64                `json:"offset"`
	NextOffset uint64                `json:"next_offset"`
	Output     string                `json:"output"`
	Truncated  bool                  `json:"truncated"`
	State      session.TerminalState `json:"state"`
}

func (worker *WorkerHTTP) registerMCP(mux *http.ServeMux) {
	mux.HandleFunc("GET /output", func(w http.ResponseWriter, r *http.Request) {
		offset, err := strconv.ParseUint(r.URL.Query().Get("offset"), 10, 64)
		if r.URL.Query().Get("offset") == "" {
			offset, err = 0, nil
		}
		limit := ChunkSize
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var limitErr error
			limit, limitErr = strconv.Atoi(raw)
			err = errors.Join(err, limitErr)
		}
		if err != nil || limit <= 0 || limit > ChunkSize {
			http.Error(w, "invalid terminal output range", 400)
			return
		}
		if raw := r.URL.Query().Get("tail"); raw != "" {
			tail, tailErr := strconv.Atoi(raw)
			err = errors.Join(err, tailErr)
			if err != nil || tail <= 0 || tail > ReplayLimit {
				http.Error(w, "invalid terminal output range", 400)
				return
			}
			session := worker.current()
			session.mu.Lock()
			end := session.offset + uint64(len(session.Replay))
			start := session.offset
			if len(session.Replay) > tail {
				start = end - uint64(tail)
			}
			slice := session.Replay[start-session.offset:]
			result := TerminalOutput{
				Offset: start, NextOffset: end,
				Output: string(slice), Truncated: start > session.offset,
				State: session.Status,
			}
			session.mu.Unlock()
			WriteJSON(w, result)
			return
		}
		session := worker.current()
		session.mu.Lock()
		truncated := offset < session.offset
		if truncated {
			offset = session.offset
		}
		end := session.offset + uint64(len(session.Replay))
		if offset > end {
			session.mu.Unlock()
			http.Error(w, "terminal offset is past available output", 400)
			return
		}
		next := min(end, offset+uint64(limit))
		result := TerminalOutput{Offset: offset, NextOffset: next, Output: string(session.Replay[offset-session.offset : next-session.offset]), Truncated: truncated, State: session.Status}
		session.mu.Unlock()
		WriteJSON(w, result)
	})
	mux.HandleFunc("POST /input", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, ChunkSize))
		if err != nil || len(data) == 0 {
			http.Error(w, "input must contain 1 to 32768 bytes", 400)
			return
		}
		session := worker.current()
		err = session.writeInput(data)
		if err != nil {
			message := "terminal input failed"
			if errors.Is(err, ErrSessionEnded) {
				message = err.Error()
			}
			http.Error(w, message, 409)
			return
		}
		WriteJSON(w, struct {
			Accepted int `json:"accepted"`
		}{len(data)})
	})
}
