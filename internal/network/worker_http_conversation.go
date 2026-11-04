package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/tmaxmax/go-sse"
)

const maxBlobUploadReadTime = 30 * time.Second

func (worker *WorkerHTTP) registerConversation(mux *http.ServeMux) {
	mux.HandleFunc("GET /conversation", func(w http.ResponseWriter, r *http.Request) {
		conv := worker.conversationSession()
		if conv == nil {
			http.Error(w, "conversation unavailable", http.StatusNotFound)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") || r.URL.Query().Get("stream") == "1" {
			worker.serveConversationSSE(w, r, conv)
			return
		}
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		entries, truncated := conv.Attach(since)
		if truncated {
			w.Header().Set("X-Agentpack-Truncated", "true")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		encoder := json.NewEncoder(w)
		for _, entry := range entries {
			if err := encoder.Encode(entry); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("POST /conversation/prompt", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Text  string `json:"text"`
			Image *struct {
				Sha    string `json:"sha"`
				Mime   string `json:"mime"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"image"`
			ResourceLink *struct {
				Sha  string `json:"sha"`
				Name string `json:"name"`
			} `json:"resource_link"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if input.Image != nil && input.ResourceLink != nil {
			http.Error(w, "choose an image or a resource link", http.StatusBadRequest)
			return
		}
		prompt := acp.PromptInput{Text: strings.TrimSpace(input.Text)}
		if input.Image != nil && input.Image.Sha != "" {
			sha, err := validateBlobSHA(input.Image.Sha)
			if err != nil {
				http.Error(w, "invalid image sha", http.StatusBadRequest)
				return
			}
			prompt.Image = &acp.PromptImage{
				Sha:   sha,
				Width: input.Image.Width, Height: input.Image.Height,
			}
		}
		if input.ResourceLink != nil {
			sha, err := validateBlobSHA(input.ResourceLink.Sha)
			if err != nil {
				http.Error(w, "invalid resource link sha", http.StatusBadRequest)
				return
			}
			prompt.ResourceLink = &acp.PromptResourceLink{Sha: sha, Name: input.ResourceLink.Name}
		}
		if prompt.Text == "" && prompt.Image == nil && prompt.ResourceLink == nil {
			http.Error(w, "text or image is required", http.StatusBadRequest)
			return
		}
		if err := worker.conversationSession().Prompt(r.Context(), prompt); err != nil {
			http.Error(w, "could not send prompt", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("POST /conversation/prompt/images", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Text   string `json:"text"`
			Images []struct {
				Sha    string `json:"sha"`
				Name   string `json:"name"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"images"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if input.Images == nil {
			http.Error(w, "images must be an array", http.StatusBadRequest)
			return
		}
		if len(input.Images) > maxPromptImages {
			http.Error(w, "too many images", http.StatusBadRequest)
			return
		}
		prompt := acp.PromptInput{Text: strings.TrimSpace(input.Text), Images: make([]acp.PromptImage, 0, len(input.Images))}
		for _, image := range input.Images {
			sha, err := validateBlobSHA(image.Sha)
			if err != nil || image.Width < 0 || image.Height < 0 {
				http.Error(w, "invalid image", http.StatusBadRequest)
				return
			}
			prompt.Images = append(prompt.Images, acp.PromptImage{
				Sha: sha, Name: sanitizePromptImageName(image.Name), Width: image.Width, Height: image.Height,
			})
		}
		if prompt.Text == "" && len(prompt.Images) == 0 {
			http.Error(w, "text or image is required", http.StatusBadRequest)
			return
		}
		if err := worker.conversationSession().Prompt(r.Context(), prompt); err != nil {
			http.Error(w, "could not send prompt", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("POST /conversation/permission", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			RequestID string `json:"request_id"`
			OptionID  string `json:"option_id"`
		}
		if !readJSON(w, r, &input) || input.RequestID == "" || input.OptionID == "" {
			if input.RequestID == "" || input.OptionID == "" {
				http.Error(w, "request_id and option_id are required", http.StatusBadRequest)
			}
			return
		}
		if err := worker.conversationSession().AnswerPermission(r.Context(), input.RequestID, input.OptionID); err != nil {
			http.Error(w, "could not answer permission", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("POST /conversation/cancel", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
		}
		if err := worker.conversationSession().Cancel(ctx); err != nil {
			http.Error(w, "could not cancel turn", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"cancelled": true})
	})
	mux.HandleFunc("POST /conversation/elicitation", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ElicitationID string `json:"elicitation_id"`
			Value         string `json:"value"`
		}
		if !readJSON(w, r, &input) || input.ElicitationID == "" {
			if input.ElicitationID == "" {
				http.Error(w, "elicitation_id is required", http.StatusBadRequest)
			}
			return
		}
		if err := worker.conversationSession().AnswerElicitation(r.Context(), input.ElicitationID, input.Value); err != nil {
			http.Error(w, "could not answer elicitation", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("POST /conversation/config", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if input.Key == "" || len(input.Value) == 0 {
			http.Error(w, "key and value are required", http.StatusBadRequest)
			return
		}
		if err := worker.conversationSession().SetConfigOption(r.Context(), input.Key, input.Value); err != nil {
			http.Error(w, "could not set config", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("GET /conversation/config-options", func(w http.ResponseWriter, r *http.Request) {
		conv := worker.conversationSession()
		if conv == nil {
			http.Error(w, "conversation unavailable", http.StatusNotFound)
			return
		}
		raw := conv.ConfigJSON()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /blobs/{sha}", func(w http.ResponseWriter, r *http.Request) {
		serveSessionBlob(w, r, worker.sessionDir, r.PathValue("sha"))
	})
	mux.HandleFunc("POST /conversation/blobs", func(w http.ResponseWriter, r *http.Request) {
		if worker.sessionDir == "" {
			http.Error(w, "blob store unavailable", http.StatusConflict)
			return
		}
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(maxBlobUploadReadTime)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			http.Error(w, "could not set blob upload deadline", http.StatusInternalServerError)
			return
		}
		defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
		worker.blobMu.Lock()
		defer worker.blobMu.Unlock()
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBlobBytes+1))
		if err != nil {
			http.Error(w, "invalid blob", http.StatusBadRequest)
			return
		}
		sha, err := storeSessionBlob(worker.sessionDir, data)
		if errors.Is(err, errSessionBlobQuota) {
			http.Error(w, err.Error(), http.StatusInsufficientStorage)
			return
		}
		if err != nil {
			http.Error(w, "could not store blob", http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"sha": sha})
	})
}

func (worker *WorkerHTTP) serveConversationSSE(w http.ResponseWriter, r *http.Request, conv *ConversationSession) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	_, truncated := conv.Attach(since)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	if truncated {
		w.Header().Set("X-Agentpack-Truncated", "true")
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	truncatedSent := false
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			entries, truncated := conv.Attach(since)
			for _, entry := range entries {
				since = entry.Seq
				data, err := json.Marshal(entry)
				if err != nil {
					continue
				}
				var message sse.Message
				message.AppendData(string(data))
				if _, err := message.WriteTo(w); err != nil {
					return
				}
			}
			if err := writeTruncatedSSE(w, truncated, &truncatedSent); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
func writeTruncatedSSE(w io.Writer, truncated bool, sent *bool) error {
	if !truncated || *sent {
		return nil
	}
	var message sse.Message
	message.Type = sse.Type("truncated")
	if _, err := message.WriteTo(w); err != nil {
		return err
	}
	*sent = true
	return nil
}

func proxyWorkerConversation(w http.ResponseWriter, r *http.Request, record WorkerRecord, suffix string) {
	path := "/conversation" + suffix
	var body io.Reader
	if r.Method == http.MethodPost {
		limit := int64(1 << 20)
		if suffix == "/blobs" {
			limit = maxBlobBytes + 1
		}
		body = http.MaxBytesReader(w, r.Body, limit)
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, "http://"+record.Address+path+"?"+r.URL.RawQuery, body)
	if err != nil {
		http.Error(w, "invalid conversation request", http.StatusBadRequest)
		return
	}
	request.Header.Set("Authorization", "Bearer "+record.Secret)
	if r.Method == http.MethodPost {
		contentType := "application/json"
		if suffix == "/blobs" {
			contentType = r.Header.Get("Content-Type")
		}
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
	}
	stream := r.Method == http.MethodGet && (r.URL.Query().Get("stream") == "1" || strings.Contains(r.Header.Get("Accept"), "text/event-stream"))
	client := http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if !stream {
		client.Timeout = 30 * time.Second
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		http.Error(w, "session unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		http.Error(w, "Needs update", http.StatusNotFound)
		return
	}
	for _, key := range []string{"Content-Type", "X-Agentpack-Truncated"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	if stream && response.StatusCode == http.StatusOK {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(response.StatusCode)
		flusher.Flush()
		_, _ = io.Copy(conversationFlushWriter{Writer: w, Flusher: flusher}, response.Body)
		return
	}
	if r.Method == http.MethodGet && suffix == "" && response.StatusCode == http.StatusOK {
		data, err := io.ReadAll(io.LimitReader(response.Body, maxConversationSnapshotBytes+1))
		if err != nil {
			http.Error(w, "conversation snapshot unavailable", http.StatusBadGateway)
			return
		}
		if len(data) > maxConversationSnapshotBytes {
			data = data[:maxConversationSnapshotBytes]
			data = data[:bytes.LastIndexByte(data, '\n')+1]
			w.Header().Set("X-Agentpack-Truncated", "true")
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(data)
		return
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 4<<20))
}

type conversationFlushWriter struct {
	io.Writer
	http.Flusher
}

func (writer conversationFlushWriter) Write(data []byte) (int, error) {
	n, err := writer.Writer.Write(data)
	if n > 0 {
		writer.Flusher.Flush()
	}
	return n, err
}
