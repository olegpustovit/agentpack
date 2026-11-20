package network

import (
	"compress/flate"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/gorilla/websocket"
	"github.com/olegpustovit/agentpack/internal/state"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// Error spools written before opening state are attributed by the local gateway.
const localStateNode = "local"

type GatewayOptions struct {
	Home, Address, PublicURL string
	Start                    func(context.Context, SessionInfo, string) (string, error)
	Packages                 func(context.Context, string) (ProjectContents, error)
	Install                  func(context.Context, string, string, bool) error
	Update                   func(ctx context.Context, version string) error
	Library                  LibraryOptions
	Assets                   http.Handler
	Version                  string
	UIBuildID                string
}

type nodeRecord struct {
	Address string `json:"address"`
	Secret  string `json:"secret"`
}

type Gateway struct {
	options        GatewayOptions
	starting       sync.WaitGroup // chats launching in the background; tests wait on it
	chatLifecycle  sync.Mutex     // serializes resume claims with settling dead chats
	store          *state.Store
	logs           *obs.Store
	auth           *gatewayAuth
	mcp            *GatewayMCP
	local          nodeRecord
	origin         *url.URL // loopback origin; public access adds another
	public         publicAccess
	stop           context.CancelFunc
	done           <-chan struct{}
	replicaStatus  sync.Map
	statePulls     sync.Map // peer → coalesced pull notification
	syncTransferMu sync.Mutex
	syncTransfers  map[string]*stateTransferFile
	identityMu     sync.Mutex
	updateMu       sync.Mutex
	updating       bool
	updateError    string
	identityAt     time.Time
	identitySelf   tailnetSelf
	events         eventHub
	tailnet        tailnetCache
	mcpBroker      mcpBroker
	push           pushWatch
}

// browserPingInterval keeps idle terminal connections alive through proxies. Tests shorten it.
var browserPingInterval = 15 * time.Second

func ServeGateway(ctx context.Context, options GatewayOptions) (result error) {
	if options.Address == "" {
		options.Address = "127.0.0.1:7420"
	}
	if err := paths.EnsurePrivateDirectory(filepath.Join(options.Home, "network")); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(options.Home, "network", "gateway.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("gateway already running")
	}
	defer lock.Unlock()
	listener, err := net.Listen("tcp", options.Address)
	if err != nil {
		return err
	}
	defer listener.Close()
	// The loopback origin is always served; a public HTTPS origin is added by the Public access switch.
	origin := &url.URL{Scheme: "http", Host: listener.Addr().String()}
	if address, ok := listener.Addr().(*net.TCPAddr); !ok || !address.IP.IsLoopback() && options.PublicURL == "" {
		return errors.New("remote gateway requires HTTPS")
	}
	store, err := state.Open(options.Home)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, store.Close()) }()
	if snapshot, err := tailnetSnapshot(ctx); err == nil {
		if err := store.BindNode(ctx, snapshot.Self.ID); err != nil {
			return err
		}
	}
	nodeID, err := store.NodeID(context.Background())
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(options.Home, "network", "limits.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy limits cache: %w", err)
	}
	auth, err := newGatewayAuth(options.Home, store)
	if err != nil {
		return err
	}
	logs, err := obs.Open(options.Home, obs.StoreOptions{Metadata: obs.BuildMetadata(options.Version, nodeID, "gateway"), State: store})
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, logs.Close()) }()
	restoreDiagnostics := obs.SetDefault(logs, obs.BuildMetadata(options.Version, nodeID, "gateway"))
	defer restoreDiagnostics()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	gateway := &Gateway{options: options, store: store, logs: logs, auth: auth, local: nodeRecord{listener.Addr().String(), randomToken()}, origin: origin, stop: cancel, done: ctx.Done()}
	defer func() { result = errors.Join(result, gateway.cleanStateTransfers(time.Time{})) }()
	if err := gateway.loadPublic(context.Background()); err != nil {
		return err
	}
	gateway.reapStoppedChats(obs.WithSink(ctx, logs, obs.BuildMetadata(options.Version, nodeID, "gateway")))
	gateway.endLeftoverStarts(obs.WithSink(ctx, logs, obs.BuildMetadata(options.Version, nodeID, "gateway")))
	gateway.mcp, err = NewGatewayMCP(gateway)
	if err != nil {
		return err
	}
	var background sync.WaitGroup
	for _, task := range []struct {
		name string
		run  func(context.Context)
	}{{"gateway.tailnet_sharing", gateway.runTailnetSharing}, {"gateway.log_spool", gateway.runLogSpool}, {"gateway.state_sync", gateway.runStateSync}, {"gateway.log_forwarding", gateway.runLogForwarding}, {"gateway.archive", func(ctx context.Context) { gateway.runArchive(ctx, time.Hour, time.Now) }}, {"gateway.content_locations", func(ctx context.Context) { gateway.runContentLocations(ctx, 5*time.Minute) }}, {"gateway.mcp_credentials", func(ctx context.Context) { gateway.runMCPCredentials(ctx, 30*time.Second) }}, {"gateway.limits_after_turn", func(ctx context.Context) { gateway.runLimitsAfterTurns(ctx, time.Minute) }}, {"gateway.reaper", func(ctx context.Context) { gateway.runReaper(ctx, 15*time.Second) }}, {"gateway.push", func(ctx context.Context) { gateway.runPush(ctx, pushInterval) }}} {
		background.Add(1)
		obs.Go(task.name, func() { defer background.Done(); task.run(ctx) })
	}
	defer func() { cancel(); background.Wait() }()
	data, _ := json.Marshal(gateway.local)
	record := filepath.Join(options.Home, "network", "node.json")
	if err = os.WriteFile(record, data, 0600); err != nil {
		return err
	}
	defer os.Remove(record)
	server := &http.Server{Handler: gateway.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func (g *Gateway) runLogSpool(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := g.logs.IngestSpool(ctx); err != nil && ctx.Err() == nil {
			log.Printf("diagnostic spool ingestion failed: %v", obs.Redact(err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// localRequest reports a same-machine CLI call: it carries the private local secret and no browser origin.
func (g *Gateway) localRequest(r *http.Request) bool {
	return r.Header.Get("Origin") == "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.local.Secret)) == 1
}

func (g *Gateway) Handler() http.Handler {
	nodeID, nodeErr := g.auth.store.NodeID(context.Background())
	if nodeErr != nil {
		log.Printf("gateway node identity unavailable: %v", nodeErr)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "gateway state unavailable", http.StatusServiceUnavailable)
		})
	}
	mux := http.NewServeMux()
	buildID := g.options.UIBuildID
	if buildID == "" {
		files, _ := fs.Sub(webDist, "webdist")
		buildID = uiBuildID(files)
	}
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"ui_build_id": buildID})
	})
	g.registerSettings(mux)
	g.registerPush(mux)
	g.registerLibrary(mux)
	g.registerRegistryReplication(mux)
	g.registerStateSync(mux)
	mux.HandleFunc("GET /api/cache/{cache_key}", g.serveCache)
	g.registerLimits(mux)
	g.registerDiagnostics(mux)
	g.registerMCPCredentials(mux)
	mux.HandleFunc("POST /api/telemetry", g.telemetry)
	// Profiles reveal memory contents, so only this machine's CLI may read them; peers and browsers never can.
	mux.HandleFunc("GET /api/debug/pprof/{name...}", func(w http.ResponseWriter, r *http.Request) {
		if !g.localRequest(r) {
			http.Error(w, "local access only", http.StatusForbidden)
			return
		}
		switch name := r.PathValue("name"); name {
		case "profile":
			pprof.Profile(w, r)
		case "trace":
			pprof.Trace(w, r)
		default:
			pprof.Handler(name).ServeHTTP(w, r)
		}
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /api/status", g.auth.status)
	mux.HandleFunc("GET /api/identity", g.identity)
	mux.HandleFunc("GET /api/version/latest", latestAgentpackVersion)
	mux.HandleFunc("GET /api/node/update", func(w http.ResponseWriter, r *http.Request) {
		g.updateMu.Lock()
		defer g.updateMu.Unlock()
		state := "idle"
		if g.updating {
			state = "running"
		} else if g.updateError != "" {
			state = "failed"
		}
		writeJSON(w, map[string]string{"state": state, "error": g.updateError})
	})
	mux.HandleFunc("GET /api/machine", g.machine)
	mux.HandleFunc("POST /api/node/update", func(w http.ResponseWriter, r *http.Request) {
		if g.options.Update == nil {
			http.Error(w, "gateway updates are unavailable", http.StatusNotImplemented)
			return
		}
		// Installs this machine's view of the latest release, never a version the caller names.
		version, _, err := fetchLatestRelease(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		g.updateMu.Lock()
		if g.updating {
			g.updateMu.Unlock()
			http.Error(w, "an update is already running", http.StatusConflict)
			return
		}
		g.updating = true
		g.updateError = ""
		g.updateMu.Unlock()
		obs.Go("gateway.update", func() {
			ctx, event := obs.Start(obs.WithRecording(context.Background(), r.Context()), "gateway", "update")
			event.Set("target_version", version)
			err := g.options.Update(ctx, version)
			if err != nil {
				event.Fail(err)
			}
			event.End()
			g.updateMu.Lock()
			g.updating = false
			if err != nil {
				g.updateError = err.Error()
			}
			g.updateMu.Unlock()
		})
		writeJSON(w, map[string]any{"started": true, "version": version})
	})
	mux.HandleFunc("POST /api/claim", g.auth.claim)
	mux.HandleFunc("POST /api/login", g.auth.login)
	mux.HandleFunc("POST /api/logout", g.auth.logout)
	mux.HandleFunc("POST /api/logout-all", g.auth.logoutAll)
	mux.HandleFunc("GET /api/events", g.serveEvents)
	mux.HandleFunc("GET /api/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("discover") == "1" {
			value, err := g.tailnetNodes(r.Context())
			respond(w, value, err)
			return
		}
		value, err := g.meshNodes(r.Context(), nil)
		if err != nil {
			meshReadError(w, r, err)
			return
		}
		respond(w, value, nil)
	})
	mux.HandleFunc("GET /api/topology", func(w http.ResponseWriter, r *http.Request) {
		value, err := g.tailnetTopology(r.Context())
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/local/sessions/{id}/presence", g.servePresence)
	mux.HandleFunc("DELETE /api/local/sessions/{id}/presence", g.servePresence)

	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		value, err := g.meshSessions(r.Context(), r.URL.Query().Get("node"))
		if err != nil {
			meshReadError(w, r, err)
			return
		}
		if err == nil {
			for i := range value {
				value[i] = publicSession(value[i])
			}
		}
		respond(w, value, err)
	})
	mux.HandleFunc("GET /api/recent", func(w http.ResponseWriter, r *http.Request) {
		value, err := g.meshRecent(r.Context(), r.URL.Query().Get("node"))
		if err != nil {
			meshReadError(w, r, err)
			return
		}
		respond(w, value, err)
	})
	mux.HandleFunc("DELETE /api/recent", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Directory string `json:"directory"`
			Harness   string `json:"harness"`
			Mode      string `json:"mode"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		respond(w, map[string]bool{"forgotten": true}, ForgetRecent(g.options.Home, SessionInfo{Directory: input.Directory, Harness: input.Harness, Mode: input.Mode}))
	})
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Directory      string `json:"directory"`
			Harness        string `json:"harness"`
			Mode           string `json:"mode"`
			Transport      string `json:"transport,omitempty"`
			AgentSessionID string `json:"agent_session_id,omitempty"`
			Resume         string `json:"resume,omitempty"`
			ParentID       string `json:"parent_id,omitempty"`
			Tools          *bool  `json:"tools,omitempty"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if input.Resume != "" && input.AgentSessionID == "" {
			input.AgentSessionID = input.Resume
		}
		actor, ok := actorFrom(r.Context())
		if !ok {
			actor = browserActor(g.auth.email(r))
		}
		info, err := g.startSession(r.Context(), actor, SessionInfo{
			Directory: input.Directory, Harness: input.Harness, Mode: input.Mode,
			Transport: input.Transport, AgentSessionID: input.AgentSessionID,
			// agentpack tools are on for new chats unless the request turns them off.
			ParentID: input.ParentID, Tools: input.Transport == "acp" && (input.Tools == nil || *input.Tools),
		}, "")
		writeSessionStart(w, info, err)
	})
	mux.HandleFunc("POST /api/sessions/{id}/fork", func(w http.ResponseWriter, r *http.Request) {
		record, err := g.workerRecord(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return
		}
		if record.Session.Transport != "acp" || record.Session.AgentSessionID == "" {
			http.Error(w, "only ACP sessions with a native session ID can be forked", http.StatusBadRequest)
			return
		}
		if !authorizeACPWrite(w, r, record.Session) {
			return
		}
		actor, ok := actorFrom(r.Context())
		if !ok {
			actor = browserActor(g.auth.email(r))
		}
		var input struct {
			ParentID string `json:"parent_id"`
		}
		// The body is optional: only agentpack tools name the chat a fork helps.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		info, err := g.startSession(r.Context(), actor, SessionInfo{
			Directory: record.Session.Directory,
			Harness:   record.Session.Harness,
			Mode:      record.Session.Mode,
			Transport: "acp",
			ParentID:  input.ParentID,
			Tools:     record.Session.Tools,
		}, record.Session.AgentSessionID)
		writeSessionStart(w, info, err)
	})
	mux.HandleFunc("POST /api/sessions/{id}/tools", g.setChatTools)
	mux.HandleFunc("POST /api/sessions/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		if !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
			return
		}
		respond(w, map[string]bool{"stopped": true}, StopWorker(r.Context(), g.options.Home, r.PathValue("id")))
	})
	mux.HandleFunc("PATCH /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
			return
		}
		var input struct {
			Name   *string `json:"name"`
			Hidden *bool   `json:"hidden"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if input.Hidden != nil && *input.Hidden {
			err := HideSession(r.Context(), g.options.Home, r.PathValue("id"))
			switch {
			case errors.Is(err, errSessionNotFound):
				http.Error(w, "session not found", http.StatusNotFound)
			case errors.Is(err, errSessionRunning):
				http.Error(w, "Stop the session before archiving it.", http.StatusConflict)
			case err != nil:
				http.Error(w, "Couldn't archive the session.", http.StatusInternalServerError)
			default:
				writeJSON(w, map[string]bool{"hidden": true})
			}
			return
		}
		if input.Name == nil {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		name, err := normalizeSessionName(*input.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		info, err := RenameSession(r.Context(), g.options.Home, r.PathValue("id"), name)
		switch {
		case errors.Is(err, errSessionNotFound):
			http.Error(w, "session not found", http.StatusNotFound)
		case errors.Is(err, errSessionUnavailable):
			http.Error(w, "session unavailable", http.StatusBadGateway)
		case err != nil:
			http.Error(w, "could not save session name", http.StatusInternalServerError)
		default:
			writeJSON(w, publicSession(info))
		}
	})
	mux.HandleFunc("POST /api/sessions/{id}/restart", func(w http.ResponseWriter, r *http.Request) {
		var input restartOperation
		if !readJSON(w, r, &input) {
			return
		}
		if !validRestartID(input.OperationID) {
			http.Error(w, "restart requires a valid operation_id", http.StatusBadRequest)
			return
		}
		if !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
			return
		}
		if err := RestartWorker(r.Context(), g.options.Home, r.PathValue("id"), input.OperationID); err != nil {
			if errors.Is(err, errWorkerControlRejected) {
				http.Error(w, "session restart was rejected", http.StatusConflict)
			} else {
				http.Error(w, "restart outcome unavailable; retry with the same operation_id", http.StatusBadGateway)
			}
			return
		}
		writeJSON(w, map[string]bool{"restarted": true})
	})
	mux.HandleFunc("POST /api/sessions/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Text string `json:"text"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
			return
		}
		info, err := g.resumeSession(r.Context(), r.PathValue("id"), input.Text)
		switch {
		case errors.Is(err, errSessionNotFound):
			http.Error(w, "session not found", http.StatusNotFound)
		case errors.Is(err, errResumeConflict):
			http.Error(w, errResumeConflict.Error(), http.StatusConflict)
		case errors.Is(err, errNotResumable):
			http.Error(w, errNotResumable.Error(), http.StatusBadRequest)
		case err != nil:
			http.Error(w, "Couldn't resume the chat.", http.StatusInternalServerError)
		default:
			writeJSON(w, publicSession(info))
		}
	})
	mux.HandleFunc("GET /api/agent-sessions", g.serveAgentSessions)
	mux.HandleFunc("GET /api/sessions/{id}/terminal", g.terminal)
	mux.HandleFunc("GET /api/sessions/{id}/output", g.terminalData)
	mux.HandleFunc("POST /api/sessions/{id}/input", func(w http.ResponseWriter, r *http.Request) {
		if !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
			return
		}
		g.terminalData(w, r)
	})
	mux.HandleFunc("GET /api/sessions/{id}/conversation", g.serveConversation)
	mux.HandleFunc("POST /api/sessions/{id}/conversation/prompt", func(w http.ResponseWriter, r *http.Request) {
		record, err := g.workerRecord(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return
		}
		if !authorizeACPWrite(w, r, record.Session) {
			return
		}
		proxyWorkerConversation(w, r, record, "/prompt")
	})
	mux.HandleFunc("POST /api/sessions/{id}/conversation/prompt/images", func(w http.ResponseWriter, r *http.Request) {
		record, err := g.workerRecord(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return
		}
		if !authorizeACPWrite(w, r, record.Session) {
			return
		}
		proxyWorkerConversation(w, r, record, "/prompt/images")
	})
	mux.HandleFunc("POST /api/sessions/{id}/conversation/permission", func(w http.ResponseWriter, r *http.Request) {
		record, err := g.workerRecord(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return
		}
		if !authorizeACPWrite(w, r, record.Session) {
			return
		}
		proxyWorkerConversation(w, r, record, "/permission")
	})
	mux.HandleFunc("POST /api/sessions/{id}/conversation/revert", func(w http.ResponseWriter, r *http.Request) {
		record, err := g.workerRecord(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return
		}
		if !authorizeACPWrite(w, r, record.Session) {
			return
		}
		proxyWorkerConversation(w, r, record, "/revert")
	})
	mux.HandleFunc("POST /api/sessions/{id}/conversation/cancel", func(w http.ResponseWriter, r *http.Request) {
		record, err := g.workerRecord(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return
		}
		if !authorizeACPWrite(w, r, record.Session) {
			return
		}
		proxyWorkerConversation(w, r, record, "/cancel")
	})
	mux.HandleFunc("POST /api/sessions/{id}/conversation/elicitation", func(w http.ResponseWriter, r *http.Request) {
		record, err := g.workerRecord(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return
		}
		if !authorizeACPWrite(w, r, record.Session) {
			return
		}
		proxyWorkerConversation(w, r, record, "/elicitation")
	})
	mux.HandleFunc("POST /api/sessions/{id}/conversation/config", func(w http.ResponseWriter, r *http.Request) {
		record, err := g.workerRecord(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return
		}
		if !authorizeACPWrite(w, r, record.Session) {
			return
		}
		proxyWorkerConversation(w, r, record, "/config")
	})
	mux.HandleFunc("GET /api/sessions/{id}/conversation/config-options", func(w http.ResponseWriter, r *http.Request) {
		record, err := g.workerRecord(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return
		}
		proxyWorkerConversation(w, r, record, "/config-options")
	})
	mux.HandleFunc("POST /api/sessions/{id}/conversation/blobs", func(w http.ResponseWriter, r *http.Request) {
		record, err := g.workerRecord(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return
		}
		if !authorizeACPWrite(w, r, record.Session) {
			return
		}
		proxyWorkerConversation(w, r, record, "/blobs")
	})
	for _, route := range []string{
		"PATCH /queue/{queueId}", "DELETE /queue/{queueId}", "POST /queue/{queueId}/steer",
		"POST /goal", "PATCH /goal", "DELETE /goal",
	} {
		method, suffix, _ := strings.Cut(route, " ")
		mux.HandleFunc(method+" /api/sessions/{id}/conversation"+suffix, func(w http.ResponseWriter, r *http.Request) {
			record, err := g.workerRecord(r.Context(), r.PathValue("id"))
			if err != nil {
				http.Error(w, "session unavailable", http.StatusNotFound)
				return
			}
			if !authorizeACPWrite(w, r, record.Session) {
				return
			}
			proxyWorkerConversation(w, r, record, strings.Replace(suffix, "{queueId}", url.PathEscape(r.PathValue("queueId")), 1))
		})
	}
	mux.HandleFunc("GET /api/sessions/{id}/blobs/{sha}", g.serveBlob)
	mux.HandleFunc("GET /api/packages", func(w http.ResponseWriter, r *http.Request) {
		directory := r.URL.Query().Get("directory")
		if !g.knownDirectory(directory) {
			http.Error(w, "choose a recent folder from this node", 400)
			return
		}
		contents, err := g.options.Packages(r.Context(), directory)
		respond(w, contents.Packages, err)
	})
	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, r *http.Request) {
		value, err := g.projects(r.Context())
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/packages", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Directory string `json:"directory"`
			Spec      string `json:"spec"`
			Restart   *bool  `json:"restart"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if !g.knownDirectory(input.Directory) || input.Spec == "" {
			http.Error(w, "choose a recent folder and package", 400)
			return
		}
		respond(w, map[string]bool{"installed": true}, g.options.Install(r.Context(), input.Directory, input.Spec, input.Restart == nil || *input.Restart))
	})
	mux.HandleFunc("POST /api/node/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := SetGatewayEnabled(g.options.Home, false); err != nil {
			http.Error(w, "could not persist gateway stop", 500)
			return
		}
		writeJSON(w, map[string]bool{"stopping": true})
		g.stop()
	})
	mux.HandleFunc("POST /api/node/stop-agents", func(w http.ResponseWriter, r *http.Request) {
		sessions, err := sessionsIn(r.Context(), g.auth.store.Reader, g.options.Home)
		skipped := 0
		if err == nil {
			actor, ok := actorFrom(r.Context())
			if !ok {
				actor = browserActor(g.auth.email(r))
			}
			for _, session := range sessions {
				if session.Transport == "acp" && !actor.canWriteSession(session) {
					skipped++
					continue
				}
				err = errors.Join(err, StopWorker(r.Context(), g.options.Home, session.ID))
			}
		}
		respond(w, map[string]any{"stopped": err == nil, "skipped": skipped}, err)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}` + "\n"))
	})
	if g.options.Assets != nil {
		mux.Handle("/", g.options.Assets)
	}
	api := conditionalAPI(mux)
	secured := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(WithPeerCache(r.Context(), g.options.Home))
		if p := r.URL.Path; strings.HasPrefix(p, "/api/") || isMCPPath(p) {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if g.mcp != nil && chatBearer(r) {
			g.mcp.ServeHTTP(w, r) // a chat's own agentpack tools credential; see mcp_chat.go
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' blob:; manifest-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		local := g.localRequest(r)
		publicOrigin, _ := g.public.current()
		source := g.resolveSource(r, publicOrigin)
		r = withRequestSource(r, source)
		if g.serveProbe(w, r) {
			return
		}
		if !local && source.origin == "" {
			if source.proxied && publicOrigin == nil {
				g.servePublicOff(w, r, source)
				return
			}
			http.Error(w, "unrecognized gateway host", 403)
			return
		}
		claimed, err := g.auth.claimed(r.Context())
		if err != nil {
			authFailure(w, r, "account state unavailable", err)
			return
		}
		// An unclaimed gateway is claimed locally (or over SSH), never from the internet, even when
		// pinned; status and health stay readable for deploy checks.
		if !local && !claimed && publicOrigin != nil && source.origin == publicOrigin.String() && !(r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/api/status")) {
			g.servePublicOff(w, r, source)
			return
		}
		if peer, _ := r.Context().Value(replicaPeerKey{}).(string); peer != "" {
			var hasAccounts bool
			if err := g.auth.store.Reader.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM users)").Scan(&hasAccounts); err != nil {
				authFailure(w, r, "account state unavailable", err)
				return
			}
			if !hasAccounts && (!g.meshPeer(r.Context(), peer) || !(r.Method == http.MethodGet && (r.URL.Path == "/api/sync/changes" || r.URL.Path == "/api/sync/events" || r.URL.Path == "/api/events"))) {
				http.Error(w, "waiting for mesh accounts", http.StatusForbidden)
				return
			}
		}
		if !claimed && !local {
			allowed := (r.Method == "GET" || r.Method == "HEAD") && (r.URL.Path == "/healthz" || r.URL.Path == "/api/status" || !strings.HasPrefix(r.URL.Path, "/api/") && !isMCPPath(r.URL.Path)) || r.Method == "POST" && r.URL.Path == "/api/claim"
			if !allowed {
				http.Error(w, "unclaimed", http.StatusForbidden)
				return
			}
		}
		if g.mcp != nil && isMCPPath(r.URL.Path) {
			g.mcp.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && origin != source.origin {
			http.Error(w, "cross-origin access refused", 403)
			return
		}
		if !local && r.Method != "GET" && r.Method != "HEAD" && origin == "" {
			http.Error(w, "origin required", 403)
			return
		}
		public := r.URL.Path == "/healthz" || r.URL.Path == "/api/config" || r.URL.Path == "/api/status" || r.URL.Path == "/api/claim" || r.URL.Path == "/api/login" || !strings.HasPrefix(r.URL.Path, "/api/")
		email := ""
		if !local {
			email = g.auth.email(r)
		}
		if !public && !local && email == "" {
			http.Error(w, "login required", 401)
			return
		}
		if _, known := actorFrom(r.Context()); !known {
			if local {
				actor, err := g.localActor(r.Context())
				if errors.Is(err, errLocalAccountUnavailable) {
					http.Error(w, err.Error(), http.StatusForbidden)
					return
				}
				if err != nil {
					authFailure(w, r, "local account unavailable", err)
					return
				}
				r = r.WithContext(withActor(r.Context(), actor))
			} else if email != "" {
				r = r.WithContext(withActor(r.Context(), browserActor(email)))
			}
		}

		if handled, err := g.routeMeshRequest(w, r); err != nil {
			meshReadError(w, r, err)
			return
		} else if handled {
			return
		}
		if r.Method == http.MethodGet && meshListRoute(r.URL.Path) && (r.URL.Query().Get("node") == "" || g.meshPeer(r.Context(), r.URL.Query().Get("node"))) {
			api.ServeHTTP(w, r)
			return
		}
		if nodeID := r.URL.Query().Get("node"); nodeID != "" && strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Del("Cache-Control") // the peer's Cache-Control passes through unchanged
			g.proxyTailnet(w, r, nodeID)
			return
		}
		api.ServeHTTP(w, r)
	})
	var sink obs.Sink
	if g.logs != nil {
		sink = g.logs
	}
	return observeHTTP(mux, secured, sink, obs.BuildMetadata(g.options.Version, nodeID, "gateway"))
}

func publicSession(info SessionInfo) SessionInfo {
	if info.StartedBy == nil || info.StartedBy.Kind != "peer" || info.StartedBy.StableNodeID == "" {
		return info
	}
	owner := *info.StartedBy
	owner.StableNodeID = ""
	info.StartedBy = &owner
	return info
}

func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, value)
}
func writeSessionStart(w http.ResponseWriter, info SessionInfo, err error) {
	if errors.Is(err, errSessionArchived) {
		http.Error(w, "archived", http.StatusConflict)
		return
	}
	if errors.Is(err, errACPResumeDenied) {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if errors.Is(err, errACPResumeOwnerStorage) {
		http.Error(w, "could not verify ACP session owner", http.StatusInternalServerError)
		return
	}
	respond(w, publicSession(info), err)
}
func authorizeACPWrite(w http.ResponseWriter, r *http.Request, session SessionInfo) bool {
	if !session.ArchivedAt.IsZero() {
		http.Error(w, "archived", http.StatusConflict)
		return false
	}
	if session.Transport != "acp" {
		return true
	}
	actor, ok := actorFrom(r.Context())
	if !ok {
		actor = browserActor("")
	}
	if actor.canWriteSession(session) {
		return true
	}
	http.Error(w, "read-only actor cannot modify this ACP session", http.StatusForbidden)
	return false
}

func (g *Gateway) authorizeACPWriteByID(w http.ResponseWriter, r *http.Request, id string) bool {
	info, err := storedSession(r.Context(), g.auth.store.Reader, id)
	if errors.Is(err, errSessionNotFound) {
		record, recordErr := g.workerRecord(r.Context(), id)
		if recordErr != nil {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return false
		}
		info, err = record.Session, nil
	}
	if err != nil {
		http.Error(w, "session metadata unavailable", http.StatusInternalServerError)
		return false
	}
	return authorizeACPWrite(w, r, info)
}

// startSession records the actor that started the session, then starts its worker.
func (g *Gateway) startSession(ctx context.Context, actor Actor, info SessionInfo, forkSessionID string) (SessionInfo, error) {
	source := info
	if forkSessionID != "" {
		if info.Transport != "acp" || info.AgentSessionID != "" {
			return SessionInfo{}, errors.New("fork requires an ACP session without resume")
		}
		source.AgentSessionID = forkSessionID
	}
	if err := authorizeACPResume(ctx, g.auth.store, actor, source); err != nil {
		return SessionInfo{}, err
	}
	if !g.knownDirectory(info.Directory) {
		return SessionInfo{}, errors.New("choose a recent folder from this node")
	}
	local, err := g.auth.store.NodeID(ctx)
	if err != nil {
		return SessionInfo{}, err
	}
	info.NodeID = local
	info.Reachable = true
	info.LastSync = time.Time{}
	info.EndedAt = time.Time{}
	info.ArchivedAt = time.Time{}
	info.StartedBy = &actor
	return g.launchSession(ctx, info, forkSessionID)
}

func (g *Gateway) knownDirectory(directory string) bool {
	if !filepath.IsAbs(directory) {
		return false
	}
	recent, err := recentIn(context.Background(), g.auth.store.Reader)
	if err != nil {
		return false
	}
	for _, entry := range recent {
		if filepath.Clean(entry.Directory) == filepath.Clean(directory) {
			identity, err := FolderIdentity(directory)
			return err == nil && entry.FolderID != "" && entry.FolderID == identity
		}
	}
	return false
}

func (g *Gateway) serveAgentSessions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("directory") == "" || r.URL.Query().Get("node") != "" {
		sessions, err := g.meshAgentSessions(r.Context(), r.URL.Query().Get("node"), r.URL.Query().Get("directory"), r.URL.Query().Get("harness"))
		if err != nil {
			meshReadError(w, r, err)
			return
		}
		writeJSON(w, map[string]any{"sessions": sessions})
		return
	}

	directory := r.URL.Query().Get("directory")
	harness := r.URL.Query().Get("harness")
	if !g.knownDirectory(directory) || harness == "" {
		http.Error(w, "choose a recent folder from this node", http.StatusBadRequest)
		return
	}
	var definitionRoot, modeName string
	recent, err := recentIn(r.Context(), g.auth.store.Reader)
	if err != nil {
		respond(w, nil, err)
		return
	}
	clean := filepath.Clean(directory)
	for _, entry := range recent {
		if filepath.Clean(entry.Directory) == clean && entry.Harness == harness {
			definitionRoot = entry.DefinitionRoot
			modeName = entry.Mode
			break
		}
	}
	sessions, err := ListAgentSessions(r.Context(), g.options.Home, directory, harness, definitionRoot, modeName)
	if err != nil {
		respond(w, nil, err)
		return
	}
	local, err := g.auth.store.NodeID(r.Context())
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	folderID, err := FolderIdentity(directory)
	if err != nil {
		respond(w, nil, err)
		return
	}
	archived, err := archivedNativeIDs(r.Context(), g.auth.store, folderID, harness)
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	sessions = slices.Clone(sessions)
	sessions = slices.DeleteFunc(sessions, func(session AgentSession) bool { return archived[session.ID] })
	for i := range sessions {
		sessions[i].NodeID = local
		sessions[i].Reachable = true
	}
	writeJSON(w, map[string]any{"sessions": sessions})
}

func (g *Gateway) terminal(w http.ResponseWriter, r *http.Request) {
	record, err := g.workerRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "session unavailable", 404)
		return
	}
	since, err := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	if err != nil {
		since = 0
	}
	worker, err := attachWorker(r.Context(), record, false, since)
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
	browser.SetReadLimit(chunkSize)
	_ = browser.SetReadDeadline(time.Now().Add(45 * time.Second))
	browser.SetPongHandler(func(string) error { return browser.SetReadDeadline(time.Now().Add(45 * time.Second)) })
	worker.SetReadLimit(replayLimit + 4096)
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
			for len(data) > chunkSize && kind == websocket.BinaryMessage {
				if err = dst.WriteMessage(kind, data[:chunkSize]); err != nil {
					return
				}
				data = data[chunkSize:]
			}
			if err = dst.WriteMessage(kind, data); err != nil {
				return
			}
		}
	}
	go copyFrames(worker, browser)
	go copyFrames(browser, worker)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ping := time.NewTicker(browserPingInterval)
	defer ping.Stop()
	private := g.localRequest(r)
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
		case <-ticker.C:
			if !private && !g.auth.authenticated(r) {
				return
			}
		}
	}
}

func loadNode(home string) (nodeRecord, error) {
	var record nodeRecord
	data, err := os.ReadFile(filepath.Join(home, "network", "node.json"))
	if err != nil {
		return record, err
	}
	err = json.Unmarshal(data, &record)
	host, _, hostErr := net.SplitHostPort(record.Address)
	if err != nil || hostErr != nil || host != "127.0.0.1" || len(record.Secret) != 64 {
		return record, errors.New("invalid node record")
	}
	return record, nil
}
func NodeRequest(ctx context.Context, home, method, path string, body ...io.Reader) ([]byte, error) {
	record, err := loadNode(home)
	if err != nil {
		return nil, err
	}
	response, err := workerRequest(ctx, WorkerRecord{Address: record.Address, Secret: record.Secret}, method, path, "", body...)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("node: %s", strings.TrimSpace(string(data)))
	}
	return data, err
}

// NodeRequestStream writes a successful node response to dst, stopping after maxBytes.
func NodeRequestStream(ctx context.Context, home, method, path string, dst io.Writer, maxBytes int64) error {
	if dst == nil || maxBytes < 0 {
		return errors.New("node: invalid response destination or limit")
	}
	record, err := loadNode(home)
	if err != nil {
		return err
	}
	response, err := workerRequest(ctx, WorkerRecord{Address: record.Address, Secret: record.Secret}, method, path, "")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if readErr != nil {
			return errors.Join(fmt.Errorf("node: %s", strings.TrimSpace(string(data))), readErr)
		}
		return fmt.Errorf("node: %s", strings.TrimSpace(string(data)))
	}
	written, err := io.Copy(dst, io.LimitReader(response.Body, maxBytes))
	if err != nil {
		return err
	}
	if written == maxBytes {
		var extra [1]byte
		n, err := io.ReadFull(response.Body, extra[:])
		if n > 0 {
			return fmt.Errorf("node: response exceeds %d bytes", maxBytes)
		}
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
	}
	return nil
}

// StopGateway asks the gateway to stop and waits until its process has exited, which it shows by
// releasing gateway.lock; its listener closes earlier, so a start right after could still collide.
func StopGateway(ctx context.Context, home string) error {
	if _, err := NodeRequest(ctx, home, "POST", "/api/node/stop"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lock := flock.New(filepath.Join(home, "network", "gateway.lock"))
	if ok, err := lock.TryLockContext(ctx, 100*time.Millisecond); !ok {
		return errors.Join(errors.New("gateway did not stop; sessions were left running"), err)
	}
	return lock.Unlock()
}

func EnsureNode(ctx context.Context, home, executable string) error {
	if enabled, err := GatewayEnabled(home); err != nil {
		return err
	} else if !enabled {
		return ErrGatewayDisabled
	}
	if _, err := NodeRequest(ctx, home, "GET", "/healthz"); err == nil {
		return nil
	}
	if err := paths.EnsurePrivateDirectory(filepath.Join(home, "network")); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(home, "network", "gateway.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	command := exec.Command(executable, "gateway", "serve", "--listen", "127.0.0.1:0")
	ConfigureDetached(command)
	command.Stdout, command.Stderr = log, log
	if err = command.Start(); err != nil {
		return err
	}
	go command.Wait()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("gateway startup timed out; inspect %s", log.Name())
		case <-ticker.C:
			if _, err = NodeRequest(ctx, home, "GET", "/healthz"); err == nil {
				return nil
			}
		}
	}
}
