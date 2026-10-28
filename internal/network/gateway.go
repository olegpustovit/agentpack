package network

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/gorilla/websocket"
)

type GatewayOptions struct {
	Home, Address, PublicURL string
	Start                    func(context.Context, SessionInfo) (string, error)
	Packages                 func(context.Context, string) (any, error)
	Install                  func(context.Context, string, string, bool) error
	Library                  LibraryOptions
	Assets                   http.Handler
}

type nodeRecord struct {
	Address string `json:"address"`
	Secret  string `json:"secret"`
}

type Gateway struct {
	options       GatewayOptions
	auth          *gatewayAuth
	mcp           *GatewayMCP
	local         nodeRecord
	origin        *url.URL
	stop          context.CancelFunc
	done          <-chan struct{}
	replicaMu     sync.Mutex
	replicaStatus sync.Map
}

func ServeGateway(ctx context.Context, options GatewayOptions) error {
	if options.Address == "" {
		options.Address = "127.0.0.1:7420"
	}
	if err := EnsurePrivateDirectory(filepath.Join(options.Home, "network")); err != nil {
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
	if options.PublicURL == "" {
		options.PublicURL = "http://" + listener.Addr().String()
	}
	origin, err := url.Parse(options.PublicURL)
	if err != nil || origin.Host == "" || origin.User != nil || (origin.Scheme != "https" && origin.Scheme != "http") || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" && origin.Path != "/" {
		return errors.New("public URL must be an HTTP(S) origin")
	}
	if origin.Scheme != "https" && origin.Hostname() != "127.0.0.1" && origin.Hostname() != "localhost" {
		return errors.New("remote gateway requires HTTPS")
	}
	auth, err := newGatewayAuth(options.Home)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	gateway := &Gateway{options: options, auth: auth, local: nodeRecord{listener.Addr().String(), randomToken()}, origin: origin, stop: cancel, done: ctx.Done()}
	gateway.mcp, err = NewGatewayMCP(gateway)
	if err != nil {
		return err
	}
	defer gateway.mcp.Close()
	go gateway.runTailnetSharing(ctx)
	go gateway.runRegistryReplication(ctx)
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

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	g.registerSettings(mux)
	g.registerLibrary(mux)
	g.registerRegistryReplication(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /api/status", g.auth.status)
	mux.HandleFunc("POST /api/setup", func(w http.ResponseWriter, r *http.Request) { g.auth.login(w, r, true, g.origin.Scheme == "https") })
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) { g.auth.login(w, r, false, g.origin.Scheme == "https") })
	mux.HandleFunc("POST /api/logout", g.auth.logout)
	mux.HandleFunc("GET /api/nodes", func(w http.ResponseWriter, r *http.Request) {
		value, err := TailnetNodes(r.Context(), g.options.Home)
		respond(w, value, err)
	})
	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		value, err := Sessions(r.Context(), g.options.Home)
		respond(w, value, err)
	})
	mux.HandleFunc("GET /api/recent", func(w http.ResponseWriter, r *http.Request) {
		value, err := RecentFolders(g.options.Home)
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
			Directory string `json:"directory"`
			Harness   string `json:"harness"`
			Mode      string `json:"mode"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		info := SessionInfo{Directory: input.Directory, Harness: input.Harness, Mode: input.Mode}
		if !g.knownDirectory(info.Directory) {
			http.Error(w, "choose a recent folder from this node", 400)
			return
		}
		id, err := g.options.Start(r.Context(), info)
		info.ID = id
		respond(w, info, err)
	})
	mux.HandleFunc("POST /api/sessions/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]bool{"stopped": true}, StopWorker(r.Context(), g.options.Home, r.PathValue("id")))
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
	mux.HandleFunc("GET /api/sessions/{id}/terminal", g.terminal)
	mux.HandleFunc("GET /api/sessions/{id}/output", g.terminalData)
	mux.HandleFunc("POST /api/sessions/{id}/input", g.terminalData)
	mux.HandleFunc("GET /api/packages", func(w http.ResponseWriter, r *http.Request) {
		directory := r.URL.Query().Get("directory")
		if !g.knownDirectory(directory) {
			http.Error(w, "choose a recent folder from this node", 400)
			return
		}
		value, err := g.options.Packages(r.Context(), directory)
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
		sessions, err := Sessions(r.Context(), g.options.Home)
		if err == nil {
			for _, session := range sessions {
				err = errors.Join(err, StopWorker(r.Context(), g.options.Home, session.ID))
			}
		}
		respond(w, map[string]bool{"stopped": err == nil}, err)
	})
	if g.options.Assets != nil {
		mux.Handle("/", g.options.Assets)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; manifest-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		local := r.Header.Get("Origin") == "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.local.Secret)) == 1
		if !local && r.Host != g.origin.Host {
			http.Error(w, "unrecognized gateway host", 403)
			return
		}
		if g.mcp != nil && isMCPPath(r.URL.Path) {
			g.mcp.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && origin != g.origin.Scheme+"://"+g.origin.Host {
			http.Error(w, "cross-origin access refused", 403)
			return
		}
		if !local && r.Method != "GET" && r.Method != "HEAD" && origin == "" {
			http.Error(w, "origin required", 403)
			return
		}
		public := r.URL.Path == "/healthz" || r.URL.Path == "/api/status" || r.URL.Path == "/api/setup" || r.URL.Path == "/api/login" || !strings.HasPrefix(r.URL.Path, "/api/")
		if !public && !local && !g.auth.authenticated(r) {
			http.Error(w, "login required", 401)
			return
		}
		if nodeID := r.URL.Query().Get("node"); nodeID != "" {
			g.proxyTailnet(w, r, nodeID)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, value)
}
func (g *Gateway) knownDirectory(directory string) bool {
	if !filepath.IsAbs(directory) {
		return false
	}
	recent, err := Recent(g.options.Home)
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

func (g *Gateway) terminal(w http.ResponseWriter, r *http.Request) {
	record, err := workerRecord(g.options.Home, r.PathValue("id"))
	if err != nil {
		http.Error(w, "session unavailable", 404)
		return
	}
	worker, err := attachWorker(r.Context(), record, false)
	if err != nil {
		http.Error(w, "session unavailable", 502)
		return
	}
	defer worker.Close()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		return r.Header.Get("Origin") == g.origin.Scheme+"://"+g.origin.Host || (r.Header.Get("Origin") == "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.local.Secret)) == 1)
	}}
	browser, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browser.Close()
	browser.SetReadLimit(chunkSize)
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
			if err = dst.WriteMessage(kind, data); err != nil {
				return
			}
		}
	}
	go copyFrames(worker, browser)
	go copyFrames(browser, worker)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	private := r.Header.Get("Origin") == "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.local.Secret)) == 1
	for {
		select {
		case <-done:
			return
		case <-r.Context().Done():
			return
		case <-g.done:
			return
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
func NodeRequest(ctx context.Context, home, method, path string) ([]byte, error) {
	record, err := loadNode(home)
	if err != nil {
		return nil, err
	}
	response, err := workerRequest(ctx, WorkerRecord{Address: record.Address, Secret: record.Secret}, method, path, "")
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

func EnsureNode(ctx context.Context, home, executable string) error {
	if enabled, err := GatewayEnabled(home); err != nil {
		return err
	} else if !enabled {
		return ErrGatewayDisabled
	}
	if _, err := NodeRequest(ctx, home, "GET", "/healthz"); err == nil {
		return nil
	}
	if err := EnsurePrivateDirectory(filepath.Join(home, "network")); err != nil {
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
