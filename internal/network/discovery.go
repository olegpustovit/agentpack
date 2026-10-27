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
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const tailnetPort = "7421"

// SharingConfig contains explicit machine/tag exceptions to personal identity.
// It does not change Tailscale ACLs or expose anything outside the tailnet.
type SharingConfig struct {
	Enabled bool     `json:"enabled"`
	Allow   []string `json:"allow,omitempty"`
}
type TailnetNode struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"-"`
	Online  bool   `json:"online"`
}
type tailnetPeer struct {
	ID           string
	HostName     string
	DNSName      string
	UserID       int64
	TailscaleIPs []string
	Tags         []string
	Online       bool
}
type tailnetStatus struct {
	BackendState string
	Self         *tailnetPeer
	Peer         map[string]*tailnetPeer
}
type tailnetIdentity struct {
	Node struct {
		StableID  string
		User      int64
		Tags      []string
		Addresses []string
	}
}

// ReadSharingConfig defaults to sharing on, like Agentnet's tailnet discovery:
// peers still need your own untagged identity or an explicit allow entry.
func ReadSharingConfig(home string) (SharingConfig, error) {
	config := SharingConfig{Enabled: true}
	data, err := os.ReadFile(filepath.Join(home, "network", "sharing.json"))
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	err = json.Unmarshal(data, &config)
	return config, err
}
func WriteSharingConfig(home string, config SharingConfig) error {
	for _, value := range config.Allow {
		if value == "" || strings.ContainsAny(value, " /\\\t\r\n") {
			return errors.New("allow requires a stable Tailscale node ID or tag:name")
		}
	}
	directory := filepath.Join(home, "network")
	if err := EnsurePrivateDirectory(directory); err != nil {
		return err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".sharing-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, "sharing.json"))
}
func tailscaleJSON(ctx context.Context, destination any, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "tailscale", args...)
	output, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err = command.Start(); err != nil {
		return errors.New("Tailscale CLI is unavailable")
	}
	data, readErr := io.ReadAll(io.LimitReader(output, 4<<20+1))
	if readErr != nil || len(data) > 4<<20 {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if readErr != nil || waitErr != nil || len(data) > 4<<20 {
		return errors.New("Tailscale identity is unavailable")
	}
	if err = json.Unmarshal(data, destination); err != nil {
		return errors.New("Tailscale returned an invalid identity")
	}
	return nil
}
func tailnetSnapshot(ctx context.Context) (tailnetStatus, error) {
	var status tailnetStatus
	err := tailscaleJSON(ctx, &status, "status", "--json")
	if err == nil && (status.BackendState != "Running" || status.Self == nil || status.Self.ID == "") {
		err = errors.New("Tailscale is not connected")
	}
	return status, err
}
func allowedTailnetPeer(self, peer *tailnetPeer, config SharingConfig) bool {
	if self == nil || peer == nil || peer.ID == "" {
		return false
	}
	if slices.Contains(config.Allow, peer.ID) {
		return true
	}
	for _, tag := range peer.Tags {
		if slices.Contains(config.Allow, tag) {
			return true
		}
	}
	return len(self.Tags) == 0 && len(peer.Tags) == 0 && self.UserID != 0 && self.UserID == peer.UserID
}
func tailnetAddress(peer *tailnetPeer) string {
	if peer == nil {
		return ""
	}
	for _, raw := range peer.TailscaleIPs {
		address, err := netip.ParseAddr(raw)
		if err == nil && (netip.MustParsePrefix("100.64.0.0/10").Contains(address) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(address)) {
			return net.JoinHostPort(address.String(), tailnetPort)
		}
	}
	return ""
}

var tailnetTransport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 5 * time.Second, IdleConnTimeout: 30 * time.Second}
var tailnetMutationTransport = func() *http.Transport {
	transport := tailnetTransport.Clone()
	transport.ResponseHeaderTimeout = 5 * time.Minute
	return transport
}()
var tailnetHTTP = &http.Client{Transport: tailnetTransport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// TailnetNodes probes only allowed, online peers learned from the local daemon.
func TailnetNodes(ctx context.Context, home string) ([]TailnetNode, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	config, err := ReadSharingConfig(home)
	if err != nil {
		return nil, err
	}
	nodes := []TailnetNode{}
	if !config.Enabled {
		return nodes, nil
	}
	status, err := tailnetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var wait sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, peer := range status.Peer {
		if peer == nil || !peer.Online || !allowedTailnetPeer(status.Self, peer, config) || tailnetAddress(peer) == "" {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wait.Wait()
			return nodes, ctx.Err()
		}
		wait.Add(1)
		go func(peer *tailnetPeer) {
			defer wait.Done()
			defer func() { <-slots }()
			request, err := http.NewRequestWithContext(ctx, "GET", "http://"+tailnetAddress(peer)+"/peer", nil)
			if err != nil {
				return
			}
			response, err := tailnetHTTP.Do(request)
			if err != nil {
				return
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				return
			}
			var remote TailnetNode
			if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&remote) != nil || remote.ID != peer.ID {
				return
			}
			remote.Address = tailnetAddress(peer)
			remote.Name = peer.HostName
			remote.Online = true
			mu.Lock()
			nodes = append(nodes, remote)
			mu.Unlock()
		}(peer)
	}
	wait.Wait()
	slices.SortFunc(nodes, func(a, b TailnetNode) int { return strings.Compare(a.Name, b.Name) })
	return nodes, nil
}
func peerRoute(method, path string) bool {
	if method == "DELETE" && path == "/api/recent" {
		return true
	}
	if method == "GET" && (path == "/api/sessions" || path == "/api/recent" || path == "/api/packages") {
		return true
	}
	if method == "POST" && (path == "/api/sessions" || path == "/api/packages") {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 4 || parts[0] != "api" || parts[1] != "sessions" || parts[2] == "" || parts[2] == "." || parts[2] == ".." {
		return false
	}
	return method == "GET" && (parts[3] == "terminal" || parts[3] == "output") || method == "POST" && (parts[3] == "restart" || parts[3] == "stop" || parts[3] == "input")
}
func tailnetTarget(ctx context.Context, home, id string) (*url.URL, error) {
	config, err := ReadSharingConfig(home)
	if err != nil || !config.Enabled {
		return nil, errors.New("tailnet sharing is disabled")
	}
	status, err := tailnetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	for _, peer := range status.Peer {
		if peer != nil && peer.ID == id && peer.Online && allowedTailnetPeer(status.Self, peer, config) {
			if address := tailnetAddress(peer); address != "" {
				return &url.URL{Scheme: "http", Host: address}, nil
			}
		}
	}
	return nil, errors.New("node is unavailable or not allowed")
}

func (g *Gateway) proxyTailnet(w http.ResponseWriter, r *http.Request, id string) {
	if !peerRoute(r.Method, r.URL.Path) {
		http.Error(w, "route unavailable on remote nodes", 400)
		return
	}
	target, err := tailnetTarget(r.Context(), g.options.Home, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	address := target.Host
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(request *http.Request) {
		original(request)
		request.Host = address
		query := request.URL.Query()
		query.Del("node")
		request.URL.RawQuery = query.Encode()
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
		request.Header.Del("Origin")
		request.Header.Del("Forwarded")
		request.Header.Del("X-Forwarded-For")
		request.Header.Del("X-Forwarded-Host")
		request.Header.Del("X-Forwarded-Proto")
	}
	proxy.Transport = tailnetTransport
	if r.Method == http.MethodPost {
		proxy.Transport = tailnetMutationTransport
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "remote node connection failed", 502)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Del("Set-Cookie")
		if response.StatusCode == http.StatusSwitchingProtocols {
			private := r.Header.Get("Origin") == "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.local.Secret)) == 1
			go func() {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-g.done:
						_ = response.Body.Close()
						return
					case <-ticker.C:
						if !private && !g.auth.authenticated(r) {
							_ = response.Body.Close()
							return
						}
					}
				}
			}()
		}
		return nil
	}
	proxy.ServeHTTP(w, r)
}
func allowedTailnetIdentity(self *tailnetPeer, identity tailnetIdentity, address netip.Addr, config SharingConfig) bool {
	peer := &tailnetPeer{ID: identity.Node.StableID, UserID: identity.Node.User, Tags: identity.Node.Tags}
	if !allowedTailnetPeer(self, peer, config) {
		return false
	}
	for _, raw := range identity.Node.Addresses {
		prefix, err := netip.ParsePrefix(raw)
		if err == nil && prefix.Addr() == address && prefix.Bits() == address.BitLen() {
			return true
		}
	}
	return false
}

func (g *Gateway) peerHandler(self *tailnetPeer, address string) http.Handler {
	handler := g.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != address || r.Header.Get("Origin") != "" || r.URL.Query().Get("node") != "" {
			http.Error(w, "invalid peer request", 403)
			return
		}
		if r.URL.Path != "/peer" && !peerRoute(r.Method, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		remote, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "peer identity required", 403)
			return
		}
		ip, err := netip.ParseAddr(remote)
		if err != nil {
			http.Error(w, "peer identity required", 403)
			return
		}
		config, err := ReadSharingConfig(g.options.Home)
		if err != nil || !config.Enabled {
			http.Error(w, "sharing disabled", 403)
			return
		}
		var identity tailnetIdentity
		if tailscaleJSON(r.Context(), &identity, "whois", "--json", ip.String()) != nil {
			http.Error(w, "peer identity unavailable", 403)
			return
		}
		if !allowedTailnetIdentity(self, identity, ip, config) {
			http.Error(w, "peer is not allowed", 403)
			return
		}
		if r.URL.Path == "/peer" {
			if r.Method != "GET" {
				http.Error(w, "method not allowed", 405)
				return
			}
			writeJSON(w, TailnetNode{ID: self.ID, Name: self.HostName, Online: true})
			return
		}
		request := r.Clone(r.Context())
		request.Header = r.Header.Clone()
		request.Header.Del("Cookie")
		request.Header.Set("Authorization", "Bearer "+g.local.Secret)
		request.Host = g.origin.Host
		handler.ServeHTTP(w, request)
	})
}

type peerConnections struct {
	sync.Mutex
	connections map[net.Conn]bool
}
type peerListener struct {
	net.Listener
	tracking *peerConnections
}
type peerConnection struct {
	net.Conn
	tracking *peerConnections
}

func (l peerListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wrapped := &peerConnection{connection, l.tracking}
	l.tracking.Lock()
	l.tracking.connections[wrapped] = true
	l.tracking.Unlock()
	return wrapped, nil
}
func (c *peerConnection) Close() error {
	c.tracking.Lock()
	delete(c.tracking.connections, c)
	c.tracking.Unlock()
	return c.Conn.Close()
}
func (t *peerConnections) close() {
	t.Lock()
	connections := make([]net.Conn, 0, len(t.connections))
	for connection := range t.connections {
		connections = append(connections, connection)
	}
	t.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

func (g *Gateway) runTailnetSharing(ctx context.Context) {
	var server *http.Server
	var tracking *peerConnections
	var bound string
	var policy string
	closeServer := func() {
		if server != nil {
			_ = server.Close()
			tracking.close()
			server = nil
			bound = ""
		}
	}
	defer closeServer()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		config, err := ReadSharingConfig(g.options.Home)
		if err != nil || !config.Enabled {
			closeServer()
		} else {
			status, err := tailnetSnapshot(ctx)
			address := tailnetAddress(status.Self)
			if err != nil || address == "" {
				closeServer()
			} else {
				currentPolicy, _ := json.Marshal(struct {
					Config SharingConfig
					Self   *tailnetPeer
				}{config, status.Self})
				signature := string(currentPolicy)
				if server == nil || bound != address || policy != signature {
					closeServer()
					listener, err := net.Listen("tcp", address)
					if err == nil {
						tracking = &peerConnections{connections: make(map[net.Conn]bool)}
						server = &http.Server{Handler: g.peerHandler(status.Self, address), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
						bound = address
						policy = signature
						go server.Serve(peerListener{listener, tracking})
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// CheckTailnet verifies the machine can use local Tailscale identity services.
func CheckTailnet(ctx context.Context) error {
	_, err := tailnetSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("enable Tailscale first: %w", err)
	}
	return nil
}
