package gateway

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
)

func (g *Gateway) ServeConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	info, err := session.StoredSession(r.Context(), g.store.Reader, id)
	if err == nil {
		local, _ := g.store.NodeID(r.Context())
		g.serveStoredSessionConversation(w, r, info, local != "" && info.NodeID == local)
		return
	}
	if errors.Is(err, session.ErrSessionNotFound) && network.ConversationPage(r).Before == 0 {
		if record, recordErr := g.workerRecord(r.Context(), id); recordErr == nil && network.ProxyWorkerConversation(w, r, record, "") {
			return
		}
	}
	g.writeStoredConversation(w, r, id, err)
}

func (g *Gateway) serveStoredSessionConversation(w http.ResponseWriter, r *http.Request, info session.SessionInfo, localOwner bool) {
	if !info.ArchivedAt.IsZero() {
		g.serveArchivedConversation(w, r, info, g.tailnetTarget)
		return
	}
	if info.Transport != "acp" {
		http.Error(w, "conversation unavailable", http.StatusNotFound)
		return
	}
	if localOwner && info.EndedAt.IsZero() && network.IsConversationStream(r) && network.ConversationPage(r).Before == 0 {
		if record, err := network.ReadWorkerRecord(g.options.Home, info.ID); err == nil && network.ProxyWorkerConversation(w, r, record, "") {
			return
		}
	}
	g.writeStoredConversation(w, r, info.ID, nil)
}

func (g *Gateway) writeStoredConversation(w http.ResponseWriter, r *http.Request, id string, err error) {
	if r.Context().Err() != nil {
		return
	}
	if errors.Is(err, session.ErrSessionNotFound) {
		http.Error(w, "conversation unavailable", http.StatusNotFound)
		return
	}
	var entries []acp.LogEntry
	var truncated bool
	if err == nil {
		entries, _, truncated, err = network.StoredConversationRange(r.Context(), g.store.Reader, id, network.ConversationPage(r))
	}
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if event := obs.From(r.Context()); event != nil {
			event.Fail(err)
		}
		http.Error(w, "conversation history unavailable", http.StatusInternalServerError)
		return
	}
	network.WriteConversationEntries(w, r, entries, truncated)
}

func (g *Gateway) terminalData(w http.ResponseWriter, r *http.Request) {
	record, err := g.workerRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "session unavailable", 404)
		return
	}
	if record.Session.Transport == "shell" && !session.AuthorizeWrite(w, r, record.Session) {
		return
	}
	path := "/output"
	if r.Method == http.MethodPost {
		path = "/input"
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, "http://"+record.Address+path+"?"+r.URL.RawQuery, http.MaxBytesReader(w, r.Body, network.ChunkSize))
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
