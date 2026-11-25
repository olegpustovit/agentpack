package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"

	"github.com/olegpustovit/agentpack/internal/gateway/mcp"
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
	Install                  func(context.Context, string, string, bool, string) error
	SaveProject              func(context.Context, ProjectSettingsInput) error
	InstallMCP               func(context.Context, string, registry.MCPServerInput, bool) error
	Update                   func(ctx context.Context, version string, progress func(UpdatePhase)) error
	Library                  LibraryOptions
	Assets                   http.Handler
	Version                  string
	UIBuildID                string

	// ProjectMCPLogin saves the login a project uses for a remote MCP server; "" restores the first.
	ProjectMCPLogin func(ctx context.Context, directory, url, credential string) error
}

type Gateway struct {
	handlerOnce sync.Once
	nodeIDCache atomic.Value
	handler     http.Handler
	limits      *limitsCache
	operations  sync.WaitGroup
	operationMu sync.Mutex
	closing     bool

	options           GatewayOptions
	startingChats     sync.Map
	starting          sync.WaitGroup // session launches that outlive a request
	chatLifecycle     sync.Mutex     // serializes resume claims with settling dead chats
	verifiedEndpoints sync.Map       // worker.json path → mod time of an endpoint whose worker answered
	store             *state.Store
	logs              *obs.Store
	auth              *gatewayAuth
	mcp               *mcp.Server
	mcpNodes          func(node string) http.Handler // nil: MCP calls for a node take the mesh; tests stand another gateway in
	local             network.NodeRecord
	origin            *url.URL // loopback origin; public access adds another
	public            publicAccess
	stop              context.CancelFunc
	done              <-chan struct{}
	chatTimings       sync.Map // latest browser duration per measurement
	replicaStatus     sync.Map
	statePulls        sync.Map   // peer → coalesced pull notification
	archiveMu         sync.Mutex // serializes archive copies with edits to archive.db
	heldBlobs         sync.Map   // blobHeldKey -> true once the file's bytes matched its SHA
	archiveWake       chan struct{}
	syncTransferMu    sync.Mutex
	syncTransfers     map[string]*stateTransferFile
	identityMu        sync.Mutex
	updateMu          sync.Mutex
	updateStatus      gatewayUpdateStatus
	identityAt        time.Time
	identitySelf      tailnetSelf
	events            eventHub
	agents            agentAccounts
	tailnet           mesh.Cache
	mcpBroker         mcpBroker
	push              pushWatch
	omp               ompSettings
	found             folderDiscovery
	credentialsMu     sync.Mutex // one agent credential sync at a time

	worktreeHistoryMu sync.Mutex
	worktreeHistory   map[string]worktreeDivergence
}

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
	if ctx.Err() != nil {
		return nil
	}
	nodeID, err := store.NodeID(ctx)
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	logs, err := obs.Open(options.Home, obs.StoreOptions{Metadata: obs.BuildMetadata(options.Version, nodeID, "gateway"), State: store, StateContext: ctx})
	if err != nil {
		return err
	}
	defer func() {
		cancel()
		result = errors.Join(result, logs.Close())
	}()
	restoreDiagnostics := obs.SetDefault(logs, obs.BuildMetadata(options.Version, nodeID, "gateway"))
	defer restoreDiagnostics()
	gateway := &Gateway{options: options, store: store, logs: logs, auth: auth, local: network.NodeRecord{Address: listener.Addr().String(), Secret: network.RandomToken()}, origin: origin, stop: cancel, done: ctx.Done(), archiveWake: make(chan struct{}, 1)}
	gateway.nodeIDCache.Store(nodeID)
	defer func() { result = errors.Join(result, gateway.cleanStateTransfers(time.Time{})) }()
	if err := gateway.loadPublic(ctx); err != nil {
		return err
	}
	gateway.reapStoppedChats(obs.WithSink(ctx, logs, obs.BuildMetadata(options.Version, nodeID, "gateway")))
	gateway.endLeftoverStarts(obs.WithSink(ctx, logs, obs.BuildMetadata(options.Version, nodeID, "gateway")))
	gateway.mcp, err = mcp.New(store, options.Home, origin.String(), gateway)
	if err != nil {
		return err
	}
	handler := gateway.Handler()
	var background sync.WaitGroup
	for _, task := range []struct {
		name string
		run  func(context.Context)
	}{{"gateway.tailnet_sharing", func(ctx context.Context) { gateway.tailnet.Serve(ctx, gateway.store, gateway) }}, {"gateway.log_spool", gateway.runLogSpool}, {"gateway.state_sync", gateway.runStateSync}, {"gateway.replication_effects", gateway.runReplicationEffects}, {"gateway.log_forwarding", gateway.runLogForwarding}, {"gateway.archive", func(ctx context.Context) { gateway.runArchive(ctx, time.Hour, time.Now) }}, {"gateway.content_locations", func(ctx context.Context) { gateway.runContentLocations(ctx, 5*time.Minute) }}, {"gateway.mcp_credentials", func(ctx context.Context) { gateway.runMCPCredentials(ctx, 30*time.Second) }}, {"gateway.limits_after_turn", func(ctx context.Context) { gateway.runLimitsAfterTurns(ctx, time.Minute) }}, {"gateway.reaper", func(ctx context.Context) { gateway.runReaper(ctx, 15*time.Second) }}, {"gateway.push", func(ctx context.Context) { gateway.runPush(ctx, pushInterval) }}, {"gateway.agent_tasks", func(ctx context.Context) {
		gateway.mcp.RunTasks(obs.WithSink(ctx, logs, obs.BuildMetadata(options.Version, nodeID, "gateway")), mcp.TasksInterval)
	}}, {"gateway.omp_settings", func(ctx context.Context) { gateway.runOMPSettings(ctx, 30*time.Second) }}, {"gateway.agent_credentials", func(ctx context.Context) { gateway.runAgentCredentials(ctx, 30*time.Second) }}} {
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
	server := &http.Server{Handler: handler, BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
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
		if p := r.URL.Path; strings.HasPrefix(p, "/api/") || mcp.IsPath(p) {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if g.mcp != nil && mcp.ChatBearer(r) {
			g.mcp.ServeHTTP(w, r) // a chat's own agentpack tools credential; see mcp/chat.go
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
			allowed := (r.Method == "GET" || r.Method == "HEAD") && (r.URL.Path == "/healthz" || r.URL.Path == "/api/status" || !strings.HasPrefix(r.URL.Path, "/api/") && !mcp.IsPath(r.URL.Path)) || r.Method == "POST" && r.URL.Path == "/api/claim"
			if !allowed {
				http.Error(w, "unclaimed", http.StatusForbidden)
				return
			}
		}
		if g.mcp != nil && mcp.IsPath(r.URL.Path) {
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
	g.registerConfig(mux)
	g.registerSettings(mux)
	g.registerPush(mux)
	g.registerLibrary(mux)
	g.registerRegistryReplication(mux)
	g.registerStateSync(mux)
	g.registerCacheTransfer(mux)
	g.registerLimits(mux)
	g.registerAgents(mux)
	g.registerDiagnostics(mux)
	g.registerMCPCredentials(mux)
	g.registerWorktrees(mux)
	g.registerAgentDefaults(mux)
	g.registerOMP(mux)
	g.registerTelemetry(mux)
	g.registerAuth(mux)
	g.registerMachines(mux)
	g.registerUpdates(mux)
	g.registerEvents(mux)
	g.registerSessions(mux)
	g.registerSessionStreams(mux)
	g.registerProjects(mux)
	g.registerGatewayLifecycle(mux)

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
	var worktree *sessionWorktreeError
	if errors.As(err, &worktree) {
		gitFailure(w, r, http.StatusUnprocessableEntity, worktree.error, map[string]any{"field": "worktree"})
		return
	}
	if errors.Is(err, errTerminalStartUnknown) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if errors.Is(err, errStartClaim) {
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
	if errors.Is(err, errUnknownFolder) || errors.Is(err, errInvalidChatID) || errors.Is(err, errInvalidTerminalID) || errors.Is(err, errResumeOwnFolder) {
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
