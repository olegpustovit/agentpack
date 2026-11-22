package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/tmaxmax/go-sse"
)

const maxBlobUploadReadTime = 30 * time.Second

// promptAcceptWait caps the wait for brokers that never report acceptance (adapters fail or log the prompt sooner).
var promptAcceptWait = 2 * time.Second

// startPrompt runs a turn beyond the request: a turn lasts until the agent finishes, which can take far longer
// than any HTTP hop allows. Failures after acceptance are recorded in the conversation as a failed turn.
// While a turn runs the prompt is queued instead, and its queue ID is returned.
func startPrompt(ctx context.Context, session *ConversationSession, prompt acp.PromptInput, steer bool) (string, error) {
	queued, first, err := session.Submit(ctx, prompt, steer)
	if err != nil || first == nil {
		return queued, err
	}
	select {
	case err := <-first:
		return "", err
	case <-time.After(promptAcceptWait):
		return "", nil
	}
}

func writePromptAccepted(w http.ResponseWriter, queued string) {
	body := map[string]any{"accepted": true}
	if queued != "" {
		body["queued"] = queued
	}
	writeJSON(w, body)
}

func (worker *WorkerHTTP) registerConversation(mux *http.ServeMux) {
	mux.HandleFunc("GET /conversation", func(w http.ResponseWriter, r *http.Request) {
		conv := worker.conversationSession()
		if conv == nil {
			http.Error(w, "conversation unavailable", http.StatusNotFound)
			return
		}
		if isConversationStream(r) {
			worker.serveConversationSSE(w, r, conv)
			return
		}
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		// Every append moves the sequence, so it names the snapshot without encoding or hashing it.
		tag := fmt.Sprintf(`"c%d-%t"`, conv.log.Seq(), conv.log.Truncated())
		w.Header().Set("ETag", tag)
		if etagMatches(r.Header.Get("If-None-Match"), tag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		entries, truncated := conv.Attach(since)
		writeConversationEntries(w, r, entries, truncated)
	})
	mux.HandleFunc("POST /conversation/prompt", worker.receivePrompt)
	mux.HandleFunc("POST /conversation/prompt/images", worker.receivePrompt)
	worker.registerQueueAndGoal(mux)
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
			http.Error(w, "This request is no longer waiting for an answer.", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("POST /conversation/revert", worker.handleRevert)
	mux.HandleFunc("POST /conversation/cancel", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
		}
		// An optional {"subagent": id} body cancels one subagent session instead of the turn.
		var input struct {
			Subagent string `json:"subagent"`
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
		if err != nil || len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &input) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if input.Subagent != "" {
			err := worker.conversationSession().CancelSubagent(ctx, input.Subagent)
			if errors.Is(err, acp.ErrSubagentNotCancellable) {
				http.Error(w, "subagent cannot be cancelled", http.StatusBadRequest)
				return
			}
			if err != nil {
				obs.From(r.Context()).Fail(err)
				http.Error(w, "could not cancel subagent", http.StatusConflict)
				return
			}
			writeJSON(w, map[string]bool{"cancelled": true})
			return
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
	mux.HandleFunc("POST /conversation/config", worker.receiveConfig)
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
		if err := advertiseBlob(r.Context(), worker.stateStore, filepath.Base(worker.sessionDir), sha, int64(len(data))); err != nil {
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
			http.Error(w, "could not record blob location", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"sha": sha})
	})
}

func (worker *WorkerHTTP) receiveConfig(w http.ResponseWriter, r *http.Request) {
	var input configSelection
	if !readJSON(w, r, &input) {
		return
	}
	if !input.valid() {
		http.Error(w, "valid key and value are required", http.StatusBadRequest)
		return
	}
	conv := worker.conversationSession()
	if conv == nil {
		http.Error(w, "conversation unavailable", http.StatusNotFound)
		return
	}
	conv.configMu.Lock()
	defer conv.configMu.Unlock()
	conv.mu.Lock()
	starting := conv.starting
	conv.mu.Unlock()
	if !starting {
		if err := conv.SetConfigOption(r.Context(), input.Key, input.Value); err != nil {
			http.Error(w, "could not set config", http.StatusConflict)
			return
		}
	}
	if worker.stateStore != nil {
		if err := saveWantedConfig(r.Context(), worker.stateStore.DB, filepath.Base(worker.sessionDir), input, starting); err != nil {
			configWriteError(w, r, err)
			return
		}
	}
	if !starting {
		conv.logConfiguration()
	}
	writeJSON(w, map[string]bool{"accepted": true})
}

func (worker *WorkerHTTP) receivePrompt(w http.ResponseWriter, r *http.Request) {
	conv := worker.conversationSession()
	if conv == nil {
		http.Error(w, "conversation unavailable", http.StatusNotFound)
		return
	}
	prompt, steer, ok := readPromptRequest(w, r)
	if !ok {
		return
	}
	prompt.From = promptFromHeader(r)
	queued, err := startPrompt(r.Context(), conv, prompt, steer)
	if err != nil {
		message := "could not send prompt"
		if errors.Is(err, errSessionArchived) {
			message = "archived"
		}
		http.Error(w, message, http.StatusConflict)
		return
	}
	worker.setTitle(r.Context(), prompt.Text)
	writePromptAccepted(w, queued)
}

func readPromptRequest(w http.ResponseWriter, r *http.Request) (acp.PromptInput, bool, bool) {
	if strings.HasSuffix(r.URL.Path, "/images") {
		return readPromptImages(w, r)
	}
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
		Steer bool `json:"steer"`
	}
	if !readJSON(w, r, &input) {
		return acp.PromptInput{}, false, false
	}
	if input.Image != nil && input.ResourceLink != nil {
		http.Error(w, "choose an image or a resource link", http.StatusBadRequest)
		return acp.PromptInput{}, false, false
	}
	prompt := acp.PromptInput{Text: strings.TrimSpace(input.Text), QueueID: r.Header.Get("Idempotency-Key")}
	if input.Image != nil && input.Image.Sha != "" {
		sha, err := validateBlobSHA(input.Image.Sha)
		if err != nil {
			http.Error(w, "invalid image sha", http.StatusBadRequest)
			return acp.PromptInput{}, false, false
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
			return acp.PromptInput{}, false, false
		}
		prompt.ResourceLink = &acp.PromptResourceLink{Sha: sha, Name: input.ResourceLink.Name}
	}
	if prompt.Text == "" && prompt.Image == nil && prompt.ResourceLink == nil {
		http.Error(w, "text or image is required", http.StatusBadRequest)
		return acp.PromptInput{}, false, false
	}
	if prompt.QueueID != "" && !validOperationID(prompt.QueueID) {
		http.Error(w, "invalid Idempotency-Key", http.StatusBadRequest)
		return acp.PromptInput{}, false, false
	}
	return prompt, input.Steer, true
}

func readPromptImages(w http.ResponseWriter, r *http.Request) (acp.PromptInput, bool, bool) {
	var input struct {
		Text   string `json:"text"`
		Images []struct {
			Sha    string `json:"sha"`
			Name   string `json:"name"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"images"`
		Steer bool `json:"steer"`
	}
	if !readJSON(w, r, &input) {
		return acp.PromptInput{}, false, false
	}
	if input.Images == nil {
		http.Error(w, "images must be an array", http.StatusBadRequest)
		return acp.PromptInput{}, false, false
	}
	if len(input.Images) > maxPromptImages {
		http.Error(w, "too many images", http.StatusBadRequest)
		return acp.PromptInput{}, false, false
	}
	prompt := acp.PromptInput{Text: strings.TrimSpace(input.Text), QueueID: r.Header.Get("Idempotency-Key"), Images: make([]acp.PromptImage, 0, len(input.Images))}
	for _, image := range input.Images {
		sha, err := validateBlobSHA(image.Sha)
		if err != nil || image.Width < 0 || image.Height < 0 {
			http.Error(w, "invalid image", http.StatusBadRequest)
			return acp.PromptInput{}, false, false
		}
		prompt.Images = append(prompt.Images, acp.PromptImage{
			Sha: sha, Name: sanitizePromptImageName(image.Name), Width: image.Width, Height: image.Height,
		})
	}
	if prompt.Text == "" && len(prompt.Images) == 0 {
		http.Error(w, "text or image is required", http.StatusBadRequest)
		return acp.PromptInput{}, false, false
	}
	if prompt.QueueID != "" && !validOperationID(prompt.QueueID) {
		http.Error(w, "invalid Idempotency-Key", http.StatusBadRequest)
		return acp.PromptInput{}, false, false
	}
	return prompt, input.Steer, true
}

func (worker *WorkerHTTP) serveConversationSSE(w http.ResponseWriter, r *http.Request, conv *ConversationSession) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	since := conversationPage(r).since
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	if conv.log.Truncated() {
		w.Header().Set("X-Agentpack-Truncated", "true")
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	truncatedSent := false
	coalesce := time.NewTimer(conversationStreamCoalesce)
	defer coalesce.Stop()
	heartbeat := time.NewTicker(conversationHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		// Take the wake channel before reading so an append between the two is never missed.
		changed := conv.log.Changed()
		entries, truncated := conv.Attach(since)
		for _, entry := range entries {
			since = entry.Seq
			if err := writeConversationSSE(w, entry); err != nil {
				return
			}
		}
		if err := writeTruncatedSSE(w, truncated, &truncatedSent); err != nil {
			return
		}
		if len(entries) > 0 || truncated {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			var ping sse.Message
			ping.AppendComment("ping")
			if _, err := ping.WriteTo(w); err != nil {
				return
			}
			flusher.Flush()
			continue
		case <-changed:
		}
		// Batch a token burst into one write without adding visible latency.
		coalesce.Reset(conversationStreamCoalesce)
		select {
		case <-r.Context().Done():
			return
		case <-coalesce.C:
		}
	}
}

var conversationHeartbeatInterval = 25 * time.Second

const conversationStreamCoalesce = 16 * time.Millisecond

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

var conversationWorkerTransport = &http.Transport{
	MaxIdleConns:          32,
	MaxIdleConnsPerHost:   2,
	IdleConnTimeout:       30 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
}

// A false result leaves the response untouched so a history read can fall back to SQL.
func proxyWorkerConversation(w http.ResponseWriter, r *http.Request, record WorkerRecord, suffix string) bool {
	path := "/conversation" + suffix
	var body io.Reader
	sendsBody := r.Method == http.MethodPost || r.Method == http.MethodPatch
	if sendsBody {
		limit := int64(1 << 20)
		if suffix == "/blobs" {
			limit = maxBlobBytes + 1
		}
		body = http.MaxBytesReader(w, r.Body, limit)
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, "http://"+record.Address+path+"?"+r.URL.RawQuery, body)
	if err != nil {
		http.Error(w, "invalid conversation request", http.StatusBadRequest)
		return true
	}
	request.Header.Set("Authorization", "Bearer "+record.Secret)
	if trace := obs.TraceParent(r.Context()); trace != "" {
		request.Header.Set("traceparent", trace)
	}
	if match := r.Header.Get("If-None-Match"); match != "" && r.Method == http.MethodGet {
		request.Header.Set("If-None-Match", match)
	}
	if sendsBody {
		if key := r.Header.Get("Idempotency-Key"); key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		if from := promptFromContext(r.Context()); from != nil {
			request.Header.Set(promptFromHeaderName, string(mustRawJSON(from)))
		}
		contentType := "application/json"
		if suffix == "/blobs" {
			contentType = r.Header.Get("Content-Type")
		}
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
	}
	stream := r.Method == http.MethodGet && isConversationStream(r)
	if stream {
		request.Header.Set("Accept", "text/event-stream")
		request.Header.Set("Last-Event-ID", r.Header.Get("Last-Event-ID"))
	}
	client := http.Client{Transport: conversationWorkerTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if !stream {
		client.Timeout = 30 * time.Second
	}
	response, err := client.Do(request)
	if err != nil {
		if r.Method == http.MethodGet && suffix == "" {
			return false
		}
		http.Error(w, "session unavailable", http.StatusBadGateway)
		return true
	}
	defer response.Body.Close()
	// A worker's 404 passes through as is: Go's "404 page not found" tells the UI the worker needs
	// an update, and anything else ("no goal is set") is the worker's own answer.
	for _, key := range []string{"Content-Type", "X-Agentpack-Truncated", "ETag"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	if stream && response.StatusCode == http.StatusOK {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return true
		}
		w.WriteHeader(response.StatusCode)
		flusher.Flush()
		_, _ = io.Copy(conversationFlushWriter{Writer: w, Flusher: flusher}, response.Body)
		return true
	}
	if r.Method == http.MethodGet && suffix == "" && response.StatusCode == http.StatusOK {
		data, err := io.ReadAll(io.LimitReader(response.Body, maxConversationSnapshotBytes+1))
		if err != nil {
			http.Error(w, "conversation snapshot unavailable", http.StatusBadGateway)
			return true
		}
		if len(data) > maxConversationSnapshotBytes {
			data = data[:maxConversationSnapshotBytes]
			data = data[:bytes.LastIndexByte(data, '\n')+1]
			w.Header().Set("X-Agentpack-Truncated", "true")
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(data)
		return true
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 4<<20))
	return true
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
