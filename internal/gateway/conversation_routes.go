package gateway

import (
	"compress/flate"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

// browserPingInterval keeps idle terminal connections alive through proxies. Tests shorten it.
var browserPingInterval = 15 * time.Second

func (g *Gateway) registerSessionStreams(mux routeRegistrar) {
	mux.HandleFunc("GET /api/sessions/{id}/terminal", g.terminal)
	mux.HandleFunc("GET /api/sessions/{id}/output", g.terminalData)
	mux.HandleFunc("POST /api/sessions/{id}/input", func(w http.ResponseWriter, r *http.Request) {
		if !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
			return
		}
		g.terminalData(w, r)
	})
	mux.HandleFunc("GET /api/sessions/{id}/conversation", g.ServeConversation)
	mux.HandleFunc("POST /api/sessions/{id}/conversation/prompt", g.ServePrompt)
	mux.HandleFunc("POST /api/sessions/{id}/conversation/prompt/images", g.ServePrompt)
	mux.HandleFunc("POST /api/sessions/{id}/conversation/blobs", g.uploadConversationBlob)
	mux.HandleFunc("POST /api/sessions/{id}/conversation/message", g.mcp.ServeAgentMessage)
	mux.HandleFunc("POST /api/sessions/{id}/conversation/show", g.serveShow)
	mux.HandleFunc("POST /api/sessions/{id}/conversation/config", g.serveWantedConfig)
	mux.HandleFunc("DELETE /api/sessions/{id}/conversation/goal", g.clearGoal)
	mux.HandleFunc("GET /api/sessions/{id}/blobs/{sha}", g.serveBlob)
	mux.HandleFunc("GET /api/sessions/{id}/blobs/{sha}/held", g.serveBlobHeld)
}

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

func (g *Gateway) terminal(w http.ResponseWriter, r *http.Request) {
	record, err := g.workerRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "session unavailable", 404)
		return
	}
	if record.Session.Transport == "shell" && !session.AuthorizeWrite(w, r, record.Session) {
		return
	}
	since, err := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	if err != nil {
		since = 0
	}
	worker, err := network.AttachWorker(r.Context(), record, false, since)
	if err != nil {
		http.Error(w, "session unavailable", 502)
		return
	}
	defer worker.Close()
	upgrader := websocket.Upgrader{EnableCompression: true, CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin != "" && origin == sourceOf(r).origin || g.localRequest(r)
	}}
	browser, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	if err := browser.SetCompressionLevel(flate.BestSpeed); err == nil {
		browser.EnableWriteCompression(true)
	}
	defer browser.Close()
	browser.SetReadLimit(network.ChunkSize)
	_ = browser.SetReadDeadline(time.Now().Add(45 * time.Second))
	browser.SetPongHandler(func(string) error { return browser.SetReadDeadline(time.Now().Add(45 * time.Second)) })
	worker.SetReadLimit(network.ReplayLimit + 4096)
	done := make(chan struct{}, 1)
	copyFrames := func(dst, src *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			kind, data, err := src.ReadMessage()
			if err != nil {
				return
			}
			_ = dst.SetWriteDeadline(time.Now().Add(5 * time.Second))
			// Stream replay in bounded messages so browsers can render before the full history arrives.
			for len(data) > network.ChunkSize && kind == websocket.BinaryMessage {
				if err = dst.WriteMessage(kind, data[:network.ChunkSize]); err != nil {
					return
				}
				data = data[network.ChunkSize:]
			}
			if err = dst.WriteMessage(kind, data); err != nil {
				return
			}
		}
	}
	go copyFrames(worker, browser)
	go copyFrames(browser, worker)
	private := g.localRequest(r)
	peer, _ := r.Context().Value(replicaPeerKey{}).(string)
	var events <-chan eventMessage
	var checkAuth <-chan time.Time
	if !private || peer != "" {
		checkAuth = g.events.checkAuth
		if checkAuth == nil {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			checkAuth = ticker.C
		}
		stream, unsubscribe := g.subscribe()
		defer unsubscribe()
		events = stream
	}
	ping := time.NewTicker(browserPingInterval)
	defer ping.Stop()
	allowed := func() bool {
		if peer == "" {
			return private || g.auth.authenticated(r)
		}
		actor, _ := session.ActorFrom(r.Context())
		if !actor.Account {
			return false
		}
		actor.Account = false
		forwarded, err := g.forwardedAccount(r.Context(), actor, actor.User)
		return err == nil && forwarded.Account
	}
	for {
		select {
		case <-done:
			return
		case <-r.Context().Done():
			return
		case <-g.done:
			return
		case <-ping.C:
			if browser.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		case event, open := <-events:
			if !open || authChanged(event) && !allowed() {
				return
			}
		case <-checkAuth:
			if !allowed() {
				return
			}
		}
	}
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

// setChatTools serves POST /api/sessions/{id}/tools on the machine that runs the chat.
func (g *Gateway) setChatTools(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if !network.ReadJSON(w, r, &input) || !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
		return
	}
	result, err := g.store.DB.ExecContext(r.Context(), "UPDATE sessions SET tools=? WHERE id=? AND transport='acp' AND node_id="+state.LocalNodeSQL, input.Enabled, r.PathValue("id"))
	var changed int64
	if err == nil {
		changed, err = result.RowsAffected()
	}
	switch {
	case err != nil:
		obs.From(r.Context()).Fail(err)
		http.Error(w, "Couldn't change agentpack tools.", http.StatusInternalServerError)
	case changed == 0:
		http.Error(w, "chat not found on this machine", http.StatusNotFound)
	default:
		network.WriteJSON(w, map[string]bool{"tools": input.Enabled})
	}
}
