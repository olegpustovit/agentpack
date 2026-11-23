package gateway

import (
	"compress/flate"
	"context"
	"crypto/subtle"
	"database/sql"
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
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"
	"github.com/gorilla/websocket"

	"github.com/olegpustovit/agentpack/internal/gateway/mesh"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/registry"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
)

type GatewayOptions struct {
	Home, Address, PublicURL string
	Start                    func(context.Context, session.SessionInfo, string) (string, error)
	Packages                 func(context.Context, string) (network.ProjectContents, error)
	Install                  func(context.Context, string, string, bool) error
	InstallMCP               func(context.Context, string, registry.MCPServerInput, bool) error
	Update                   func(ctx context.Context, version string) error
	Library                  LibraryOptions
	Assets                   http.Handler
	Version                  string
	UIBuildID                string
}

type Gateway struct {
	handlerOnce sync.Once
	nodeIDCache atomic.Value
	handler     http.Handler
	limits      *limitsCache
	operations  sync.WaitGroup
	operationMu sync.Mutex
	closing     bool

	options        GatewayOptions
	startingChats  sync.Map
	starting       sync.WaitGroup // chats launching in the background; tests wait on it
	chatLifecycle  sync.Mutex     // serializes resume claims with settling dead chats
	store          *state.Store
	logs           *obs.Store
	auth           *gatewayAuth
	mcp            *GatewayMCP
	local          network.NodeRecord
	origin         *url.URL // loopback origin; public access adds another
	public         publicAccess
	stop           context.CancelFunc
	done           <-chan struct{}
	chatTimings    sync.Map // latest browser duration per measurement
	replicaStatus  sync.Map
	statePulls     sync.Map   // peer → coalesced pull notification
	archiveMu      sync.Mutex // serializes archive copies with edits to archive.db
	heldBlobs      sync.Map   // blobHeldKey -> true once the file's bytes matched its SHA
	archiveWake    chan struct{}
	syncTransferMu sync.Mutex
	syncTransfers  map[string]*stateTransferFile
	identityMu     sync.Mutex
	updateMu       sync.Mutex
	updating       bool
	updateError    string
	identityAt     time.Time
	identitySelf   tailnetSelf
	events         eventHub
	agents         agentAccounts
	tailnet        mesh.Cache
	mcpBroker      mcpBroker
	push           pushWatch
	agentTasks     sync.Map         // task IDs whose answer is being delivered
	agentTaskSeen  map[string]int64 // last_seq each asked chat had at the last pass; owned by runAgentTasks
	agentTaskRetry time.Time        // when undelivered answers are tried again; owned by runAgentTasks
	omp            ompSettings
	found          folderDiscovery
	credentialsMu  sync.Mutex // one agent credential sync at a time

	worktreeHistoryMu sync.Mutex
	worktreeHistory   map[string]worktreeDivergence
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
	var store *state.Store
	if options.Library.Store != nil {
		store = options.Library.Store.StateStore()
	} else {
		store, err = state.Open(options.Home)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, store.Close()) }()
	}
	nodeID, err := store.NodeID(context.Background())
	if err != nil {
		return err
	}
	if strings.HasPrefix(nodeID, "site:") {
		if snapshot, err := mesh.Snapshot(ctx); err == nil {
			if err := store.BindNode(ctx, snapshot.Self.ID); err != nil {
				return err
			}
			nodeID = snapshot.Self.ID
		}
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
	gateway := &Gateway{options: options, store: store, logs: logs, auth: auth, local: network.NodeRecord{Address: listener.Addr().String(), Secret: network.RandomToken()}, origin: origin, stop: cancel, done: ctx.Done(), archiveWake: make(chan struct{}, 1)}
	gateway.nodeIDCache.Store(nodeID)
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
	handler := gateway.Handler()
	var background sync.WaitGroup
	for _, task := range []struct {
		name string
		run  func(context.Context)
	}{{"gateway.tailnet_sharing", func(ctx context.Context) { gateway.tailnet.Serve(ctx, gateway.store, gateway) }}, {"gateway.log_spool", gateway.runLogSpool}, {"gateway.state_sync", gateway.runStateSync}, {"gateway.replication_effects", gateway.runReplicationEffects}, {"gateway.log_forwarding", gateway.runLogForwarding}, {"gateway.archive", func(ctx context.Context) { gateway.runArchive(ctx, time.Hour, time.Now) }}, {"gateway.content_locations", func(ctx context.Context) { gateway.runContentLocations(ctx, 5*time.Minute) }}, {"gateway.mcp_credentials", func(ctx context.Context) { gateway.runMCPCredentials(ctx, 30*time.Second) }}, {"gateway.limits_after_turn", func(ctx context.Context) { gateway.runLimitsAfterTurns(ctx, time.Minute) }}, {"gateway.reaper", func(ctx context.Context) { gateway.runReaper(ctx, 15*time.Second) }}, {"gateway.push", func(ctx context.Context) { gateway.runPush(ctx, pushInterval) }}, {"gateway.agent_tasks", func(ctx context.Context) { gateway.runAgentTasks(ctx, agentTasksInterval) }}, {"gateway.omp_settings", func(ctx context.Context) { gateway.runOMPSettings(ctx, 30*time.Second) }}, {"gateway.agent_credentials", func(ctx context.Context) { gateway.runAgentCredentials(ctx, 30*time.Second) }}} {
		background.Add(1)
		obs.Go(task.name, func() { defer background.Done(); task.run(ctx) })
	}
	defer func() {
		cancel()
		gateway.operationMu.Lock()
		gateway.closing = true
		gateway.operationMu.Unlock()
		background.Wait()
		gateway.starting.Wait()
		gateway.operations.Wait()
	}()
	data, _ := json.Marshal(gateway.local)
	record := filepath.Join(options.Home, "network", "node.json")
	if err = os.WriteFile(record, data, 0600); err != nil {
		return err
	}
	defer os.Remove(record)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
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

// beginBackground serializes admission with shutdown before the store closes.
func (g *Gateway) beginBackground(group *sync.WaitGroup) bool {
	g.operationMu.Lock()
	defer g.operationMu.Unlock()
	if g.closing {
		return false
	}
	group.Add(1)
	return true
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

// localRequest reports a caller holding the private local secret: this machine's CLI, an enrolled
// peer (through ServePeer) or an in-process MCP call. Browsers never are.
func (g *Gateway) localRequest(r *http.Request) bool {
	kind := callerOf(r.Context())
	return kind == callerCLI || kind == callerPeer || kind == callerMCP
}

func (g *Gateway) Handler() http.Handler {
	g.handlerOnce.Do(func() { g.handler = g.buildHandler() })
	return g.handler
}

func (g *Gateway) buildHandler() http.Handler {
	nodeID, nodeErr := g.nodeID(context.Background())
	if nodeErr != nil {
		log.Printf("gateway node identity unavailable: %v", nodeErr)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "gateway state unavailable", http.StatusServiceUnavailable)
		})
	}
	mux := g.routes()
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
		// Only the private local secret makes a caller local; ServePeer and MCP dispatch name which kind.
		kind := callerBrowser
		if r.Header.Get("Origin") == "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.local.Secret)) == 1 {
			if kind = callerOf(r.Context()); kind == callerUnknown || kind == callerBrowser {
				kind = callerCLI
			}
		}
		r = r.WithContext(withCaller(r.Context(), kind))
		local := kind != callerBrowser
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
			if err := g.store.Reader.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM users)").Scan(&hasAccounts); err != nil {
				authFailure(w, r, "account state unavailable", err)
				return
			}
			if !hasAccounts && (!g.peer(peer).Mesh || !(r.Method == http.MethodGet && (r.URL.Path == "/api/sync/changes" || r.URL.Path == "/api/sync/events" || r.URL.Path == "/api/events"))) {
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
		mux.mux.ServeHTTP(w, r)
	})
	var sink obs.Sink
	if g.logs != nil {
		sink = g.logs
	}
	return observeHTTP(mux.mux, secured, sink, obs.BuildMetadata(g.options.Version, nodeID, "gateway"))
}

// routes binds every handler to its routeTable row.
func (g *Gateway) routes() routeMux {
	mux := routeMux{g, http.NewServeMux()}
	buildID := g.options.UIBuildID
	if buildID == "" {
		files, _ := fs.Sub(webDist, "webdist")
		buildID = uiBuildID(files)
	}
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		config := map[string]any{"ui_build_id": buildID, "conversation_events": true, "conversation_config_log": true}
		if g.localBrowserTiming(r) {
			config["debug_chat_timings"] = true
		}
		network.WriteJSON(w, config)
	})
	g.registerSettings(mux)
	g.registerPush(mux)
	g.registerLibrary(mux)
	g.registerRegistryReplication(mux)
	g.registerStateSync(mux)
	mux.HandleFunc("GET /api/cache/{cache_key}", g.serveCache)
	g.registerLimits(mux)
	g.registerAgents(mux)
	g.registerDiagnostics(mux)
	g.registerMCPCredentials(mux)
	g.registerWorktrees(mux)
	g.registerOMP(mux)
	mux.HandleFunc("POST /api/telemetry", g.telemetry)
	mux.HandleFunc("GET /api/debug/timings", g.readChatTimings)
	mux.HandleFunc("POST /api/debug/timings", g.writeChatTiming)
	// Profiles reveal memory contents, so only this machine's CLI may read them; peers and browsers never can.
	mux.HandleFunc("GET /api/debug/pprof/{name...}", func(w http.ResponseWriter, r *http.Request) {
		switch name := r.PathValue("name"); name {
		case "profile":
			pprof.Profile(w, r)
		case "trace":
			pprof.Trace(w, r)
		default:
			pprof.Handler(name).ServeHTTP(w, r)
		}
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { network.WriteJSON(w, map[string]bool{"ok": true}) })
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
		network.WriteJSON(w, map[string]string{"state": state, "error": g.updateError})
	})
	mux.HandleFunc("GET /api/machine", g.machine)
	mux.HandleFunc("POST /api/node/update", func(w http.ResponseWriter, r *http.Request) {
		if g.options.Update == nil {
			http.Error(w, "gateway updates are unavailable", http.StatusNotImplemented)
			return
		}
		version, status, err := g.updateTarget(r.Context())
		if err != nil {
			if status == http.StatusInternalServerError {
				meshReadError(w, r, err)
				return
			}
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
			http.Error(w, err.Error(), status)
			return
		}
		g.updateMu.Lock()
		if g.updating {
			g.updateMu.Unlock()
			http.Error(w, "an update is already running", http.StatusConflict)
			return
		}
		if !g.beginBackground(&g.operations) {
			g.updateMu.Unlock()
			http.Error(w, "gateway is shutting down", http.StatusServiceUnavailable)
			return
		}
		g.updating = true
		g.updateError = ""
		g.updateMu.Unlock()
		obs.Go("gateway.update", func() {
			defer g.operations.Done()
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
		network.WriteJSON(w, map[string]any{"started": true, "version": version})
	})
	mux.HandleFunc("POST /api/claim", g.auth.claim)
	mux.HandleFunc("POST /api/login", g.auth.login)
	mux.HandleFunc("POST /api/logout", g.logout)
	mux.HandleFunc("POST /api/logout-all", g.auth.logoutAll)
	mux.HandleFunc("GET /api/events", g.serveEvents)
	mux.HandleFunc("GET /api/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("discover") == "1" {
			value, err := g.tailnetNodes(r.Context())
			if err != nil {
				fail(w, r, err)
				return
			}
			network.WriteJSON(w, value)
			return
		}
		value, err := g.meshNodes(r.Context(), nil)
		if err != nil {
			meshReadError(w, r, err)
			return
		}
		network.WriteJSON(w, value)
	})
	mux.HandleFunc("GET /api/topology", func(w http.ResponseWriter, r *http.Request) {
		value, err := mesh.ReadTopology(r.Context(), g.store)
		if err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, value)
	})
	mux.HandleFunc("POST /api/local/sessions/{id}/presence", g.servePresence)
	mux.HandleFunc("DELETE /api/local/sessions/{id}/presence", g.servePresence)

	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		value, err := g.meshSessions(r.Context(), r.URL.Query().Get("node"), true)
		if err != nil {
			meshReadError(w, r, err)
			return
		}
		network.WriteJSON(w, value)
	})
	mux.HandleFunc("GET /api/sessions/{id}", g.serveSessionRow)
	mux.HandleFunc("GET /api/sessions/{id}/shells", g.serveChatShells)
	mux.HandleFunc("POST /api/sessions/{id}/detach-shell", g.detachShell)
	mux.HandleFunc("GET /api/recent", func(w http.ResponseWriter, r *http.Request) {
		value, err := g.meshRecent(r.Context(), r.URL.Query().Get("node"))
		if err != nil {
			meshReadError(w, r, err)
			return
		}
		network.WriteJSON(w, value)
	})
	mux.HandleFunc("DELETE /api/recent", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Directory string `json:"directory"`
			Harness   string `json:"harness"`
			Mode      string `json:"mode"`
		}
		if !network.ReadJSON(w, r, &input) {
			return
		}
		if err := network.ForgetRecent(r.Context(), g.store, session.SessionInfo{Directory: input.Directory, Harness: input.Harness, Mode: input.Mode}); err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, map[string]bool{"forgotten": true})
	})
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		var input chatStartRequest
		if !network.ReadJSON(w, r, &input) {
			return
		}
		actor, ok := session.ActorFrom(r.Context())
		if !ok {
			actor = session.BrowserActor(g.auth.email(r))
		}
		if input.Transport == "shell" {
			g.startShell(w, r, actor, input)
			return
		}
		if input.ID != "" {
			info, err := g.claimChatStart(r.Context(), actor, input)
			writeSessionStart(w, r, info, err)
			return
		}
		if input.Resume != "" && input.AgentSessionID == "" {
			input.AgentSessionID = input.Resume
		}
		definitionRoot := ""
		if input.Worktree != "" {
			if input.AgentSessionID != "" {
				http.Error(w, "a resumed conversation keeps its own folder", http.StatusBadRequest)
				return
			}
			main := filepath.Clean(input.Directory)
			directory, err := g.newSessionWorktree(r.Context(), actor, main, input.Worktree, input.WorktreeName, input.Carry)
			if err != nil {
				gitFailure(w, r, http.StatusUnprocessableEntity, err, map[string]any{"field": "worktree"})
				return
			}
			input.Directory, definitionRoot = directory, g.definitionRootFor(r.Context(), main)
		}
		info, err := g.startSession(r.Context(), actor, session.SessionInfo{
			Directory: input.Directory, DefinitionRoot: definitionRoot, Harness: input.Harness, Mode: input.Mode,
			Transport: input.Transport, AgentSessionID: input.AgentSessionID,
			// agentpack tools are on for new chats unless the request turns them off.
			ParentID: input.ParentID, Tools: input.Transport == "acp" && (input.Tools == nil || *input.Tools),
		}, "")
		writeSessionStart(w, r, info, err)
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
		if !session.AuthorizeWrite(w, r, record.Session) {
			return
		}
		actor, ok := session.ActorFrom(r.Context())
		if !ok {
			actor = session.BrowserActor(g.auth.email(r))
		}
		var input struct {
			ID       string `json:"id"`
			ParentID string `json:"parent_id"`
		}
		// Legacy clients omit the request ID. Agentpack tools can also name the chat a fork helps.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if input.ID != "" {
			info, err := g.claimChatStart(r.Context(), actor, chatStartRequest{
				ID: input.ID, Directory: record.Session.Directory, Harness: record.Session.Harness,
				Mode: record.Session.Mode, Transport: "acp", ParentID: input.ParentID, Tools: &record.Session.Tools,
				forkSessionID: record.Session.AgentSessionID, definitionRoot: record.Session.DefinitionRoot,
			})
			writeSessionStart(w, r, info, err)
			return
		}
		info, err := g.startSession(r.Context(), actor, session.SessionInfo{
			Directory:      record.Session.Directory,
			DefinitionRoot: record.Session.DefinitionRoot,
			Harness:        record.Session.Harness,
			Mode:           record.Session.Mode,
			Transport:      "acp",
			ParentID:       input.ParentID,
			Tools:          record.Session.Tools,
		}, record.Session.AgentSessionID)
		writeSessionStart(w, r, info, err)
	})
	mux.HandleFunc("POST /api/sessions/{id}/harness", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Harness string `json:"harness"`
		}
		if !network.ReadJSON(w, r, &input) {
			return
		}
		if _, err := network.HarnessTarget(input.Harness); err != nil {
			http.Error(w, "unknown agent", http.StatusBadRequest)
			return
		}
		if !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
			return
		}
		info, err := g.switchHarness(r.Context(), r.PathValue("id"), input.Harness)
		switch {
		case errors.Is(err, session.ErrSessionNotFound):
			http.Error(w, "session not found", http.StatusNotFound)
		case errors.Is(err, errResumeConflict):
			http.Error(w, errResumeConflict.Error(), http.StatusConflict)
		case errors.Is(err, errNotResumable):
			http.Error(w, errNotResumable.Error(), http.StatusBadRequest)
		case err != nil:
			http.Error(w, "Couldn't switch the chat's agent.", http.StatusInternalServerError)
		default:
			network.WriteJSON(w, publicSession(info))
		}
	})
	mux.HandleFunc("POST /api/sessions/{id}/tools", g.setChatTools)
	mux.HandleFunc("POST /api/sessions/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		if !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
			return
		}
		requestID := r.Header.Get("Idempotency-Key")
		if requestID != "" && !network.ValidOperationID(requestID) {
			http.Error(w, "invalid stop request ID", http.StatusBadRequest)
			return
		}
		if err := g.stopSession(r.Context(), r.PathValue("id"), requestID); err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, map[string]bool{"stopped": true})
	})
	mux.HandleFunc("PATCH /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name   *string `json:"name"`
			Hidden *bool   `json:"hidden"`
		}
		if !network.ReadJSON(w, r, &input) {
			return
		}
		if !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
			return
		}
		if input.Hidden != nil && *input.Hidden {
			archived, _ := sessionRow(r.Context(), r.PathValue("id"))
			changed, err := network.HideSession(r.Context(), g.store, g.options.Home, r.PathValue("id"))
			if changed && err == nil {
				g.removeArchivedWorktree(r.Context(), archived)
			}
			switch {
			case errors.Is(err, session.ErrSessionNotFound):
				http.Error(w, "session not found", http.StatusNotFound)
			case errors.Is(err, network.ErrSessionRunning):
				http.Error(w, "Stop the session before archiving it.", http.StatusConflict)
			case err != nil:
				http.Error(w, "Couldn't archive the session.", http.StatusInternalServerError)
			default:
				network.WriteJSON(w, map[string]bool{"hidden": true})
			}
			return
		}
		if input.Name == nil {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		name, err := network.NormalizeSessionName(*input.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		info, err := network.RenameSession(r.Context(), g.store, g.options.Home, r.PathValue("id"), name)
		switch {
		case errors.Is(err, session.ErrSessionNotFound):
			http.Error(w, "session not found", http.StatusNotFound)
		case errors.Is(err, session.ErrSessionUnavailable):
			http.Error(w, "session unavailable", http.StatusBadGateway)
		case err != nil:
			http.Error(w, "could not save session name", http.StatusInternalServerError)
		default:
			network.WriteJSON(w, publicSession(info))
		}
	})
	mux.HandleFunc("POST /api/sessions/{id}/restart", func(w http.ResponseWriter, r *http.Request) {
		var input network.RestartOperation
		if !network.ReadJSON(w, r, &input) {
			return
		}
		if !network.ValidOperationID(input.OperationID) {
			http.Error(w, "restart requires a valid operation_id", http.StatusBadRequest)
			return
		}
		if !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
			return
		}
		if err := network.RestartWorker(r.Context(), g.options.Home, r.PathValue("id"), input.OperationID); err != nil {
			if errors.Is(err, network.ErrWorkerControlRejected) {
				http.Error(w, "session restart was rejected", http.StatusConflict)
			} else {
				http.Error(w, "restart outcome unavailable; retry with the same operation_id", http.StatusBadGateway)
			}
			return
		}
		network.WriteJSON(w, map[string]bool{"restarted": true})
	})
	mux.HandleFunc("POST /api/sessions/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Text string `json:"text"`
		}
		if !network.ReadJSON(w, r, &input) {
			return
		}
		if !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
			return
		}
		queueID := r.Header.Get("Idempotency-Key")
		if queueID != "" && !network.ValidOperationID(queueID) {
			http.Error(w, "invalid prompt ID", http.StatusBadRequest)
			return
		}
		info, err := g.resumeSession(r.Context(), r.PathValue("id"), input.Text, queueID)
		switch {
		case errors.Is(err, session.ErrSessionNotFound):
			http.Error(w, "session not found", http.StatusNotFound)
		case errors.Is(err, errResumeConflict):
			http.Error(w, errResumeConflict.Error(), http.StatusConflict)
		case errors.Is(err, errNotResumable), errors.Is(err, errUnknownFolder):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case err != nil:
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
			http.Error(w, "Couldn't resume the chat.", http.StatusInternalServerError)
		default:
			if input.Text == "" {
				queueID = ""
			}
			network.WriteJSON(w, struct {
				session.SessionInfo
				Queued string `json:"queued,omitempty"`
			}{publicSession(info), queueID})
		}
	})
	mux.HandleFunc("GET /api/agent-snapshot", g.serveAgentSnapshot)
	mux.HandleFunc("GET /api/agent-sessions", g.serveAgentSessions)
	mux.HandleFunc("GET /api/sessions/{id}/terminal", g.terminal)
	mux.HandleFunc("GET /api/sessions/{id}/output", g.terminalData)
	mux.HandleFunc("POST /api/sessions/{id}/input", func(w http.ResponseWriter, r *http.Request) {
		if !g.authorizeACPWriteByID(w, r, r.PathValue("id")) {
			return
		}
		g.terminalData(w, r)
	})
	mux.HandleFunc("GET /api/sessions/{id}/conversation", g.ServeConversation)
	mux.HandleFunc("POST /api/sessions/{id}/conversation/prompt", g.servePrompt)
	mux.HandleFunc("POST /api/sessions/{id}/conversation/prompt/images", g.servePrompt)
	mux.HandleFunc("POST /api/sessions/{id}/conversation/message", g.serveAgentMessage)
	mux.HandleFunc("POST /api/sessions/{id}/conversation/show", g.serveShow)
	mux.HandleFunc("POST /api/sessions/{id}/conversation/config", g.serveWantedConfig)
	mux.HandleFunc("DELETE /api/sessions/{id}/conversation/goal", g.clearGoal)
	mux.HandleFunc("GET /api/sessions/{id}/blobs/{sha}", g.serveBlob)
	mux.HandleFunc("GET /api/sessions/{id}/blobs/{sha}/held", g.serveBlobHeld)
	mux.HandleFunc("GET /api/packages", func(w http.ResponseWriter, r *http.Request) {
		directory := r.URL.Query().Get("directory")
		if !g.knownDirectory(r.Context(), directory) {
			http.Error(w, "choose a recent folder from this node", 400)
			return
		}
		contents, err := g.options.Packages(r.Context(), directory)
		if err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, contents.Packages)
	})
	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, r *http.Request) {
		value, err := g.projects(r.Context())
		if err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, value)
	})
	mux.HandleFunc("GET /api/folders", g.serveFolders)
	mux.HandleFunc("GET /api/folders/found", g.serveFoundFolders)
	mux.HandleFunc("POST /api/packages", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Directory string `json:"directory"`
			Spec      string `json:"spec"`
			Restart   *bool  `json:"restart"`
		}
		if !network.ReadJSON(w, r, &input) {
			return
		}
		if !g.knownDirectory(r.Context(), input.Directory) || input.Spec == "" {
			http.Error(w, "choose a recent folder and package", 400)
			return
		}
		if err := g.options.Install(r.Context(), input.Directory, input.Spec, input.Restart == nil || *input.Restart); err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, map[string]bool{"installed": true})
	})
	mux.HandleFunc("POST /api/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			registry.MCPServerInput
			Directory string `json:"directory"`
			Restart   *bool  `json:"restart"`
		}
		if !network.ReadJSON(w, r, &input) {
			return
		}
		if !g.knownDirectory(r.Context(), input.Directory) || input.Name == "" {
			http.Error(w, "choose a recent folder and MCP server", 400)
			return
		}
		err := g.options.InstallMCP(r.Context(), input.Directory, input.MCPServerInput, input.Restart == nil || *input.Restart)
		if err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, map[string]bool{"installed": true})
	})
	mux.HandleFunc("POST /api/node/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := SetGatewayEnabled(g.options.Home, false); err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, map[string]bool{"stopping": true})
		g.stop()
	})
	mux.HandleFunc("POST /api/node/stop-agents", func(w http.ResponseWriter, r *http.Request) {
		sessions, err := network.SessionsIn(r.Context(), g.store.Reader, g.options.Home)
		skipped := 0
		if err == nil {
			actor, ok := session.ActorFrom(r.Context())
			if !ok {
				actor = session.BrowserActor(g.auth.email(r))
			}
			for _, session := range sessions {
				if session.Transport == "shell" || session.Transport == "acp" && !actor.CanWriteSession(session) {
					skipped++
					continue
				}
				err = errors.Join(err, network.StopWorker(r.Context(), g.options.Home, session.ID))
			}
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, map[string]any{"stopped": true, "skipped": skipped})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}` + "\n"))
	})
	if g.options.Assets != nil {
		mux.Handle("/", g.options.Assets)
	}
	for _, row := range routeTable {
		if row.worker != "" {
			mux.HandleFunc(row.pattern, g.forwardToWorker(row.worker))
		}
	}
	return mux
}

// serveRoute applies a routeTable row: login, actor, session routing, ?node= proxying, owner
// policy and ETags, in that order, around the handler registered for the row.
func (g *Gateway) serveRoute(row route, handler http.Handler) http.Handler {
	if row.etag() {
		handler = network.ConditionalAPI(handler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		local := g.localRequest(r)
		email := ""
		if !local {
			email = g.auth.email(r)
		}
		if row.policy != public && !local && email == "" {
			http.Error(w, "login required", 401)
			return
		}
		if row.policy == cli && callerOf(r.Context()) != callerCLI {
			http.Error(w, "local access only", http.StatusForbidden)
			return
		}
		if _, known := session.ActorFrom(r.Context()); !known {
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
				r = r.WithContext(session.WithActor(r.Context(), actor))
			} else if email != "" {
				r = r.WithContext(session.WithActor(r.Context(), session.BrowserActor(email)))
			}
		}
		if row.session {
			var handled bool
			var err error
			if r, handled, err = g.routeMeshRequest(w, r); err != nil {
				meshReadError(w, r, err)
				return
			} else if handled {
				return
			}
		}
		if node := r.URL.Query().Get("node"); node != "" && strings.HasPrefix(r.URL.Path, "/api/") &&
			!(r.Method == http.MethodGet && meshListRoute(r.URL.Path) && g.peer(node).Mesh) {
			w.Header().Del("Cache-Control") // the peer's Cache-Control passes through unchanged
			g.tailnet.Proxy(w, r, g.store, g, node)
			return
		}
		if row.policy == owner && !g.requireOwner(w, r) {
			return
		}
		handler.ServeHTTP(w, r)
	})
}

// forwardToWorker serves a conversation control row by handing the request to the session's worker.
func (g *Gateway) forwardToWorker(suffix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		record, err := g.workerRecord(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return
		}
		if !session.AuthorizeWrite(w, r, record.Session) {
			return
		}
		network.ProxyWorkerConversation(w, r, record, strings.Replace(suffix, "{queueId}", url.PathEscape(r.PathValue("queueId")), 1))
	}
}

func publicSession(info session.SessionInfo) session.SessionInfo {
	if info.StartedBy == nil || info.StartedBy.Kind != "peer" || info.StartedBy.StableNodeID == "" {
		return info
	}
	owner := *info.StartedBy
	owner.StableNodeID = ""
	info.StartedBy = &owner
	return info
}

func badRequest(w http.ResponseWriter, message string) {
	http.Error(w, message, http.StatusBadRequest)
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	if event := obs.From(r.Context()); event != nil {
		event.Fail(err)
	}
	http.Error(w, "gateway operation failed", http.StatusInternalServerError)
}
func writeSessionStart(w http.ResponseWriter, r *http.Request, info session.SessionInfo, err error) {
	if errors.Is(err, errChatStartClaim) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if errors.Is(err, session.ErrSessionArchived) {
		http.Error(w, "archived", http.StatusConflict)
		return
	}
	if errors.Is(err, network.ErrACPResumeDenied) {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if errors.Is(err, network.ErrACPResumeOwnerStorage) {
		http.Error(w, "could not verify ACP session owner", http.StatusInternalServerError)
		return
	}
	if errors.Is(err, errUnknownFolder) || errors.Is(err, errInvalidChatID) || errors.Is(err, errResumeOwnFolder) {
		badRequest(w, err.Error())
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	network.WriteJSON(w, publicSession(info))
}
func (g *Gateway) authorizeACPWriteByID(w http.ResponseWriter, r *http.Request, id string) bool {
	info, ok := sessionRow(r.Context(), id)
	if !ok {
		var err error
		if info, err = session.StoredSession(r.Context(), g.store.Reader, id); errors.Is(err, session.ErrSessionNotFound) {
			http.Error(w, "session unavailable", http.StatusNotFound)
			return false
		} else if err != nil {
			http.Error(w, "session metadata unavailable", http.StatusInternalServerError)
			return false
		}
	}
	return session.AuthorizeWrite(w, r, info)
}

// startSession records the actor that started the session, then starts its worker.
func (g *Gateway) startSession(ctx context.Context, actor session.Actor, info session.SessionInfo, forkSessionID string) (session.SessionInfo, error) {
	source := info
	if forkSessionID != "" {
		if info.Transport != "acp" || info.AgentSessionID != "" {
			return session.SessionInfo{}, errors.New("fork requires an ACP session without resume")
		}
		source.AgentSessionID = forkSessionID
	}
	if err := network.AuthorizeACPResume(ctx, g.store, actor, source); err != nil {
		return session.SessionInfo{}, err
	}
	// A new worktree is accepted only when git lists it for an authorized main folder, which the row records.
	info.WorktreeOf = ""
	if main, _, err := worktreeMain(ctx, g.options.Home, info.Directory); err == nil && g.startableDirectory(ctx, actor, main) {
		info.WorktreeOf = main
		if info.DefinitionRoot == "" {
			info.DefinitionRoot = g.definitionRootFor(ctx, main)
		}
	} else if !g.startableDirectory(ctx, actor, info.Directory) {
		return session.SessionInfo{}, errUnknownFolder
	}
	info.Directory = filepath.Clean(info.Directory)
	folderID, err := network.FolderIdentity(info.Directory)
	if err != nil {
		return session.SessionInfo{}, errUnknownFolder
	}
	info.FolderID = folderID
	local, err := g.nodeID(ctx)
	if err != nil {
		return session.SessionInfo{}, err
	}
	info.NodeID = local
	info.Reachable = true
	info.LastSync = time.Time{}
	info.EndedAt = time.Time{}
	info.ArchivedAt = time.Time{}
	info.StartedBy = &actor
	return g.launchSession(ctx, info, forkSessionID)
}

func (g *Gateway) knownDirectory(ctx context.Context, directory string) bool {
	if !filepath.IsAbs(directory) {
		return false
	}
	identity, err := network.FolderIdentity(directory)
	if err != nil || identity == "" {
		return false
	}
	var known bool
	err = g.store.Reader.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM (
			SELECT folder_id FROM sessions WHERE node_id=`+state.LocalNodeSQL+`
			AND hidden=0 AND transport<>'shell' AND directory=?
			ORDER BY last_used DESC,id DESC LIMIT 1
		) WHERE folder_id=?
	)`, filepath.Clean(directory), identity).Scan(&known)
	return err == nil && known
}

func (g *Gateway) serveAgentSessions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("directory") == "" || r.URL.Query().Get("node") != "" {
		sessions, err := g.meshAgentSessions(r.Context(), r.URL.Query().Get("node"), r.URL.Query().Get("directory"), r.URL.Query().Get("harness"))
		if err != nil {
			meshReadError(w, r, err)
			return
		}
		network.WriteJSON(w, map[string]any{"sessions": sessions})
		return
	}

	directory := r.URL.Query().Get("directory")
	harness := r.URL.Query().Get("harness")
	if !g.knownDirectory(r.Context(), directory) || harness == "" {
		http.Error(w, "choose a recent folder from this node", http.StatusBadRequest)
		return
	}
	var definitionRoot, modeName string
	err := g.store.Reader.QueryRowContext(r.Context(), `SELECT definition_root,mode FROM sessions
		WHERE node_id=`+state.LocalNodeSQL+` AND hidden=0 AND transport<>'shell' AND directory=? AND harness=?
		ORDER BY last_used DESC,id DESC LIMIT 1`, filepath.Clean(directory), harness).Scan(&definitionRoot, &modeName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, r, err)
		return
	}
	sessions, err := network.ListAgentSessions(r.Context(), g.options.Home, directory, harness, definitionRoot, modeName)
	if err != nil {
		fail(w, r, err)
		return
	}
	local, err := g.nodeID(r.Context())
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	folderID, err := network.FolderIdentity(directory)
	if err != nil {
		fail(w, r, err)
		return
	}
	archived, err := network.ArchivedNativeIDs(r.Context(), g.store, folderID, harness)
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	sessions = slices.Clone(sessions)
	sessions = slices.DeleteFunc(sessions, func(session network.AgentSession) bool { return archived[session.ID] })
	for i := range sessions {
		sessions[i].NodeID = local
		sessions[i].Reachable = true
	}
	network.WriteJSON(w, map[string]any{"sessions": sessions})
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

func NodeRequest(ctx context.Context, home, method, path string, body ...io.Reader) ([]byte, error) {
	record, err := network.LoadNode(home)
	if err != nil {
		return nil, err
	}
	response, err := network.LocalRequest(ctx, network.WorkerRecord{Address: record.Address, Secret: record.Secret}, method, path, "", apiRequestTimeout(method, path), body...)
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
	record, err := network.LoadNode(home)
	if err != nil {
		return err
	}
	response, err := network.LocalRequest(ctx, network.WorkerRecord{Address: record.Address, Secret: record.Secret}, method, path, "", apiRequestTimeout(method, path))
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
	network.ConfigureDetached(command)
	command.Stdout, command.Stderr = log, log
	if err = command.Start(); err != nil {
		return err
	}
	go command.Wait()
	return WaitForGateway(ctx, home)
}

// WaitForGateway waits for the selected background launcher to publish a healthy endpoint.
func WaitForGateway(ctx context.Context, home string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("gateway startup timed out")
		case <-ticker.C:
			if _, err := NodeRequest(ctx, home, "GET", "/healthz"); err == nil {
				return nil
			}
		}
	}
}

func (g *Gateway) workerRecord(ctx context.Context, id string) (network.WorkerRecord, error) {
	record, err := network.ReadWorkerRecord(g.options.Home, id)
	if err != nil {
		return record, err
	}
	info, cached := sessionRow(ctx, id)
	if !cached {
		info, err = session.StoredSession(ctx, g.store.Reader, id)
	}
	if err == nil {
		local, localErr := g.nodeID(ctx)
		if localErr != nil {
			return record, localErr
		}
		if info.NodeID != local {
			return record, session.ErrSessionUnavailable
		}
	}
	record.Session = info
	return record, err
}
