package network

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

type TerminalOutput struct {
	Offset     uint64        `json:"offset"`
	NextOffset uint64        `json:"next_offset"`
	Output     string        `json:"output"`
	Truncated  bool          `json:"truncated"`
	State      TerminalState `json:"state"`
}

func (worker *WorkerHTTP) registerMCP(mux *http.ServeMux) {
	mux.HandleFunc("GET /output", func(w http.ResponseWriter, r *http.Request) {
		offset, err := strconv.ParseUint(r.URL.Query().Get("offset"), 10, 64)
		if r.URL.Query().Get("offset") == "" {
			offset, err = 0, nil
		}
		limit := chunkSize
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var limitErr error
			limit, limitErr = strconv.Atoi(raw)
			err = errors.Join(err, limitErr)
		}
		if err != nil || limit <= 0 || limit > chunkSize {
			http.Error(w, "invalid terminal output range", 400)
			return
		}
		session := worker.current()
		session.mu.Lock()
		truncated := offset < session.offset
		if truncated {
			offset = session.offset
		}
		end := session.offset + uint64(len(session.replay))
		if offset > end {
			session.mu.Unlock()
			http.Error(w, "terminal offset is past available output", 400)
			return
		}
		next := min(end, offset+uint64(limit))
		result := TerminalOutput{Offset: offset, NextOffset: next, Output: string(session.replay[offset-session.offset : next-session.offset]), Truncated: truncated, State: session.state}
		session.mu.Unlock()
		writeJSON(w, result)
	})
	mux.HandleFunc("POST /input", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, chunkSize))
		if err != nil || len(data) == 0 {
			http.Error(w, "input must contain 1 to 32768 bytes", 400)
			return
		}
		session := worker.current()
		attachment, _, err := session.Attach(false)
		if err != nil {
			http.Error(w, "terminal attachment unavailable", 409)
			return
		}
		defer session.Detach(attachment)
		state, err := session.TakeControl(attachment)
		if err == nil {
			err = session.WriteInput(attachment, state.Epoch, data)
		}
		if err != nil {
			message := "terminal input failed"
			if errors.Is(err, ErrNotOwner) || errors.Is(err, ErrStaleControl) || errors.Is(err, ErrSessionEnded) {
				message = err.Error()
			}
			http.Error(w, message, 409)
			return
		}
		writeJSON(w, struct {
			Accepted int `json:"accepted"`
		}{len(data)})
	})
}

func (g *Gateway) terminalData(w http.ResponseWriter, r *http.Request) {
	record, err := workerRecord(g.options.Home, r.PathValue("id"))
	if err != nil {
		http.Error(w, "session unavailable", 404)
		return
	}
	path := "/output"
	if r.Method == http.MethodPost {
		path = "/input"
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, "http://"+record.Address+path+"?"+r.URL.RawQuery, http.MaxBytesReader(w, r.Body, chunkSize))
	if err != nil {
		http.Error(w, "invalid terminal request", 400)
		return
	}
	request.Header.Set("Authorization", "Bearer "+record.Secret)
	client := http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		http.Error(w, "session unavailable", 502)
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 256<<10))
}
