package network

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/olegpustovit/agentpack/internal/cache"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
)

const tailnetPort = "7421"

// SharingConfig contains explicit machine/tag exceptions to personal identity.
// It does not change Tailscale ACLs or expose anything outside the tailnet.
type SharingConfig struct {
	Enabled bool     `json:"enabled"`
	Allow   []string `json:"allow,omitempty"`
}
type TailnetNode struct {
	Main        bool   `json:"main"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Address     string `json:"-"`
	Online      bool   `json:"online"`
	NeedsUpdate bool   `json:"needs_update,omitempty"`
	// Version is what a peer's /peer answer reports; empty from a peer that predates it.
	Version string `json:"version,omitempty"`
}
type tailnetPeer struct {
	ID           string
	HostName     string
	DNSName      string
	OS           string
	UserID       int64
	TailscaleIPs []string
	Tags         []string
	Online       bool
	CurAddr      string
	Relay        string
	PeerRelay    string
	LastSeen     time.Time
	RxBytes      int64
	TxBytes      int64
}
type tailnetStatus struct {
	BackendState string
	Self         *tailnetPeer
	Peer         map[string]*tailnetPeer
	User         map[string]tailnetUser
}
type tailnetUser struct {
	LoginName string
}
type tailnetIdentity struct {
	Node struct {
		StableID     string
		ComputedName string
		User         int64
		Tags         []string
		Addresses    []string
	}
	UserProfile struct {
		LoginName string
	}
}

// ReadSharingConfig defaults to sharing on, like Agentnet's tailnet discovery:
// peers still need your own untagged identity or an explicit allow entry.
func ReadSharingConfig(store *state.Store, home string) (SharingConfig, error) {
	config := SharingConfig{Enabled: true}
	if store == nil {
		return config, errors.New("gateway state unavailable")
	}
	var data []byte
	err := store.Reader.QueryRow("SELECT value FROM node_settings WHERE node_id = " + state.LocalNodeSQL + " AND key = 'sharing'").Scan(&data)
	if err == nil {
		err = json.Unmarshal(data, &config)
		return config, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return config, err
	}
	path := filepath.Join(home, "network", "sharing.json")
	data, err = os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return config, err
	}
	if err := validateSharingConfig(config); err != nil {
		return config, err
	}
	// A concurrent settings write wins over a legacy import.
	if _, err := store.DB.Exec("INSERT INTO node_settings(node_id, key, value) VALUES ("+state.LocalNodeSQL+", 'sharing', ?) ON CONFLICT DO NOTHING", string(data)); err != nil {
		return config, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return config, err
	}
	return ReadSharingConfig(store, home)
}

func validateSharingConfig(config SharingConfig) error {
	for _, value := range config.Allow {
		if value == "" || strings.ContainsAny(value, " /\\\t\r\n") {
			return errors.New("allow requires a stable Tailscale node ID or tag:name")
		}
	}
	return nil
}

func WriteSharingConfig(store *state.Store, config SharingConfig) error {
	if store == nil {
		return errors.New("gateway state unavailable")
	}
	if err := validateSharingConfig(config); err != nil {
		return err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = store.DB.Exec("INSERT INTO node_settings(node_id, key, value) VALUES ("+state.LocalNodeSQL+", 'sharing', ?) ON CONFLICT(node_id, key) DO UPDATE SET value = excluded.value", string(data))
	return err
}

// tailscaleLocal finds the daemon itself on every install, including the macOS app builds that
// put no `tailscale` command on PATH. Tests point it at a missing socket.
var tailscaleLocal local.Client

const tailscaleTimeout = 3 * time.Second

func tailnetSnapshot(ctx context.Context) (tailnetStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, tailscaleTimeout)
	defer cancel()
	raw, err := tailscaleLocal.Status(ctx)
	if err != nil {
		return tailnetStatus{}, fmt.Errorf("Tailscale is unavailable: %w", err)
	}
	status := tailnetStatusFrom(raw)
	if status.BackendState != "Running" || status.Self == nil || status.Self.ID == "" {
		return status, errors.New("Tailscale is not connected")
	}
	return status, nil
}

func tailnetStatusFrom(raw *ipnstate.Status) tailnetStatus {
	status := tailnetStatus{BackendState: raw.BackendState, Self: tailnetPeerFrom(raw.Self), Peer: map[string]*tailnetPeer{}, User: map[string]tailnetUser{}}
	for key, peer := range raw.Peer {
		status.Peer[key.String()] = tailnetPeerFrom(peer)
	}
	for id, user := range raw.User {
		status.User[strconv.FormatInt(int64(id), 10)] = tailnetUser{LoginName: user.LoginName}
	}
	return status
}

func tailnetPeerFrom(peer *ipnstate.PeerStatus) *tailnetPeer {
	if peer == nil {
		return nil
	}
	result := &tailnetPeer{ID: string(peer.ID), HostName: peer.HostName, DNSName: peer.DNSName, OS: peer.OS, UserID: int64(peer.UserID), Online: peer.Online, CurAddr: peer.CurAddr, Relay: peer.Relay, PeerRelay: peer.PeerRelay, LastSeen: peer.LastSeen, RxBytes: peer.RxBytes, TxBytes: peer.TxBytes}
	for _, address := range peer.TailscaleIPs {
		result.TailscaleIPs = append(result.TailscaleIPs, address.String())
	}
	if peer.Tags != nil {
		result.Tags = peer.Tags.AsSlice()
	}
	return result
}

// tailnetWhois names the node behind a tailnet address. An unknown address is an error.
// tailnetCacheTTL bounds how stale a proxied request's view of peers and their identities may be.
const tailnetCacheTTL = 5 * time.Second

// tailnetCache spares every proxied or peer request its own Tailscale round trips. Failures are never cached.
type tailnetCache struct {
	mu       sync.Mutex
	statusAt time.Time
	status   tailnetStatus
	whois    map[netip.Addr]cachedWhois
}

type cachedWhois struct {
	at       time.Time
	identity tailnetIdentity
}

func (g *Gateway) cachedTailnetStatus(ctx context.Context) (tailnetStatus, error) {
	cache := &g.tailnet
	cache.mu.Lock()
	if !cache.statusAt.IsZero() && time.Since(cache.statusAt) < tailnetCacheTTL {
		status := cache.status
		cache.mu.Unlock()
		return status, nil
	}
	cache.mu.Unlock()
	status, err := tailnetSnapshot(ctx)
	if err == nil {
		cache.mu.Lock()
		cache.status, cache.statusAt = status, time.Now()
		cache.mu.Unlock()
	}
	return status, err
}

func (g *Gateway) cachedTailnetWhois(ctx context.Context, address netip.Addr) (tailnetIdentity, error) {
	cache := &g.tailnet
	cache.mu.Lock()
	if hit, ok := cache.whois[address]; ok && time.Since(hit.at) < tailnetCacheTTL {
		cache.mu.Unlock()
		return hit.identity, nil
	}
	cache.mu.Unlock()
	identity, err := tailnetWhois(ctx, address)
	if err == nil {
		cache.mu.Lock()
		if cache.whois == nil || len(cache.whois) >= 256 {
			cache.whois = map[netip.Addr]cachedWhois{}
		}
		cache.whois[address] = cachedWhois{at: time.Now(), identity: identity}
		cache.mu.Unlock()
	}
	return identity, err
}

func tailnetWhois(ctx context.Context, address netip.Addr) (tailnetIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, tailscaleTimeout)
	defer cancel()
	raw, err := tailscaleLocal.WhoIs(ctx, address.String())
	if err != nil {
		return tailnetIdentity{}, err
	}
	if raw.Node == nil {
		return tailnetIdentity{}, errors.New("Tailscale returned no node")
	}
	var identity tailnetIdentity
	identity.Node.StableID = string(raw.Node.StableID)
	identity.Node.ComputedName = raw.Node.ComputedName
	identity.Node.User = int64(raw.Node.User)
	identity.Node.Tags = raw.Node.Tags
	for _, prefix := range raw.Node.Addresses {
		identity.Node.Addresses = append(identity.Node.Addresses, prefix.String())
	}
	if raw.UserProfile != nil {
		identity.UserProfile.LoginName = raw.UserProfile.LoginName
	}
	return identity, nil
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
var tailnetProbeTransport = func() *http.Transport {
	transport := tailnetTransport.Clone()
	transport.ResponseHeaderTimeout = time.Minute
	return transport
}()
var tailnetMutationTransport = func() *http.Transport {
	transport := tailnetTransport.Clone()
	transport.ResponseHeaderTimeout = 5 * time.Minute
	return transport
}()
var tailnetHTTP = &http.Client{Transport: tailnetTransport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// peerProbe is one successful /peer answer and the round trip it took.
type peerProbe struct {
	Node TailnetNode
	RTT  time.Duration
}
type probeFunc func(context.Context, *tailnetPeer) (peerProbe, bool)

// probeTailnetPeer asks one peer's gateway who it is; a failed or mismatched answer is not reachable.
func probeTailnetPeer(ctx context.Context, peer *tailnetPeer) (peerProbe, bool) {
	request, err := http.NewRequestWithContext(ctx, "GET", "http://"+tailnetAddress(peer)+"/peer", nil)
	if err != nil {
		return peerProbe{}, false
	}
	start := time.Now()
	response, err := tailnetHTTP.Do(request)
	if err != nil {
		return peerProbe{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return peerProbe{}, false
	}
	var remote TailnetNode
	if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&remote) != nil || remote.ID != peer.ID {
		return peerProbe{}, false
	}
	remote.Address = tailnetAddress(peer)
	remote.Name = peer.HostName
	remote.Online = true
	return peerProbe{Node: remote, RTT: time.Since(start)}, true
}

// probeTailnetPeers probes peers concurrently, eight at a time, and returns successful answers by peer ID.
func probeTailnetPeers(ctx context.Context, peers []*tailnetPeer, probe probeFunc) (map[string]peerProbe, error) {
	results := map[string]peerProbe{}
	var mu sync.Mutex
	var wait sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, peer := range peers {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wait.Wait()
			return results, ctx.Err()
		}
		wait.Add(1)
		go func(peer *tailnetPeer) {
			defer wait.Done()
			defer func() { <-slots }()
			if result, ok := probe(ctx, peer); ok {
				mu.Lock()
				results[peer.ID] = result
				mu.Unlock()
			}
		}(peer)
	}
	wait.Wait()
	return results, nil
}

// tailnetNodesFrom returns the allowed, online peers that answer their gateway probe.
func tailnetNodesFrom(ctx context.Context, status tailnetStatus, config SharingConfig, probe probeFunc) ([]TailnetNode, error) {
	nodes := []TailnetNode{}
	if !config.Enabled {
		return nodes, nil
	}
	var peers []*tailnetPeer
	for _, peer := range status.Peer {
		if peer != nil && peer.Online && allowedTailnetPeer(status.Self, peer, config) && tailnetAddress(peer) != "" {
			peers = append(peers, peer)
		}
	}
	probed, err := probeTailnetPeers(ctx, peers, probe)
	for _, result := range probed {
		nodes = append(nodes, result.Node)
	}
	slices.SortFunc(nodes, func(a, b TailnetNode) int { return strings.Compare(a.Name, b.Name) })
	return nodes, err
}

// nameFromTailnet replaces a node name that is only its stable ID with the peer's tailnet host name.
func nameFromTailnet(nodes map[string]TailnetNode, status tailnetStatus) {
	for _, peer := range status.Peer {
		if peer == nil {
			continue
		}
		if node, ok := nodes[peer.ID]; ok && node.Name == node.ID && peer.HostName != "" {
			node.Name = peer.HostName
			nodes[peer.ID] = node
		}
	}
}

// autoEnroll pairs every discovered peer the owner has not removed, so starting ap on an allowed tailnet machine is enough to be visible.
func (g *Gateway) autoEnroll(ctx context.Context, discovered []TailnetNode) {
	if g.options.Library.Store == nil {
		return
	}
	for _, node := range discovered {
		if g.meshPeer(ctx, node.ID) {
			continue
		}
		removed, err := g.options.Library.Store.PeerRemoved(node.ID)
		if err == nil && removed {
			continue
		}
		if err == nil {
			err = g.enrollPeer(ctx, node.ID)
		}
		if err != nil {
			_, event := obs.Start(ctx, "mesh", "auto_enroll")
			event.Fail(err)
			event.End()
		}
	}
}

// tailnetNodes probes only allowed, online peers learned from the local daemon.
func (g *Gateway) tailnetNodes(ctx context.Context) ([]TailnetNode, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	config, err := ReadSharingConfig(g.auth.store, g.options.Home)
	if err != nil {
		return nil, err
	}
	discovered := []TailnetNode{}
	var discoveryErr error
	if config.Enabled {
		status, err := tailnetSnapshot(ctx)
		if err == nil {
			discovered, discoveryErr = tailnetNodesFrom(ctx, status, config, probeTailnetPeer)
		} else {
			discoveryErr = err
		}
	}
	g.autoEnroll(ctx, discovered)
	known, err := g.meshNodes(ctx, discovered)
	if err != nil {
		return nil, err
	}
	if len(known) == 0 && discoveryErr != nil {
		return nil, discoveryErr
	}
	return known, nil
}

func (g *Gateway) meshNodes(ctx context.Context, discovered []TailnetNode) ([]TailnetNode, error) {
	local, err := g.auth.store.NodeID(ctx)
	if err != nil {
		return nil, err
	}
	nodes := map[string]TailnetNode{}
	for _, node := range discovered {
		nodes[node.ID] = node
	}
	rows, err := g.auth.store.Reader.QueryContext(ctx, "SELECT node_id,name,app_version FROM mesh_nodes WHERE node_id<>?", local)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var node TailnetNode
		if err := rows.Scan(&node.ID, &node.Name, &node.Version); err != nil {
			return nil, err
		}
		if node.Name == "" {
			node.Name = node.ID
		}
		node.Online, _ = g.nodeContact(node.ID, local)
		nodes[node.ID] = node
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if store := g.options.Library.Store; store != nil {
		peers, err := store.ListPeers()
		if err != nil {
			return nil, err
		}
		for _, peer := range peers {
			node, ok := nodes[peer.Node]
			if !ok && !peer.Mesh {
				// An unpaired legacy Library peer is not a machine until discovery finds it or it is paired.
				continue
			}
			if !ok {
				node = TailnetNode{ID: peer.Node, Name: peer.Node}
			}
			if peer.Mesh || !ok {
				node.Online, _ = g.nodeContact(peer.Node, local)
			}
			node.NeedsUpdate = !peer.Mesh
			if value, ok := g.replicaStatus.Load(peer.Node); ok {
				node.NeedsUpdate = node.NeedsUpdate || value.(replicaContact).NeedsUpdate
			}
			nodes[peer.Node] = node
		}
	}
	for _, node := range nodes {
		if node.Name == node.ID {
			// Legacy Library peers store only the stable ID; name them from the local daemon when it answers.
			if status, err := tailnetSnapshot(ctx); err == nil {
				nameFromTailnet(nodes, status)
			}
			break
		}
	}
	roles, err := g.auth.store.Main(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]TailnetNode, 0, len(nodes))
	for _, node := range nodes {
		node.Main = node.ID == roles.MainNode
		result = append(result, node)
	}
	slices.SortFunc(result, func(a, b TailnetNode) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

func peerRoute(method, path string) bool {
	if method == http.MethodGet && strings.HasPrefix(path, "/api/cache/") {
		return cache.ValidKey(strings.TrimPrefix(path, "/api/cache/"))
	}
	if method == http.MethodPost && path == "/api/logs/ingest" {
		return true
	}
	if method == http.MethodGet && strings.HasPrefix(path, "/api/logs/trace/") {
		trace := strings.TrimPrefix(path, "/api/logs/trace/")
		decoded, err := hex.DecodeString(trace)
		return err == nil && len(decoded) == 16 && strings.Trim(trace, "0") != ""
	}
	if method == http.MethodGet && (path == "/api/sync/changes" || path == "/api/sync/events") {
		return true
	}
	if libraryPeerRoute(method, path) {
		return true
	}
	if method == "DELETE" && path == "/api/recent" {
		return true
	}
	if method == "GET" && (path == "/api/identity" || path == "/api/events" || path == "/api/agent-sessions") {
		return true
	}
	if method == "GET" && (path == "/api/sessions" || path == "/api/recent" || path == "/api/packages" || path == "/api/projects" || path == "/api/limits") {
		return true
	}
	if method == "POST" && (path == "/api/sessions" || path == "/api/packages" || path == "/api/limits/refresh") {
		return true
	}
	if (method == "GET" || method == "POST") && path == "/api/node/update" {
		return true
	}
	if method == "POST" && strings.HasPrefix(path, "/api/mcp/credentials/") && strings.HasSuffix(path, "/refresh") && strings.Count(path, "/") == 5 {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	conversation := len(parts) >= 5 && parts[0] == "api" && parts[1] == "sessions" &&
		validPeerSessionID(parts[2]) && parts[3] == "conversation"
	if len(parts) == 6 {
		return conversation && (method == "POST" && parts[4] == "prompt" && parts[5] == "images" ||
			(method == "PATCH" || method == "DELETE") && parts[4] == "queue" && validPeerSessionID(parts[5]))
	}
	if len(parts) == 7 {
		return conversation && method == "POST" && parts[4] == "queue" && validPeerSessionID(parts[5]) && parts[6] == "steer"
	}
	if conversation && len(parts) == 5 && parts[4] == "goal" {
		return method == "POST" || method == "PATCH" || method == "DELETE"
	}
	if len(parts) == 3 && parts[0] == "api" && parts[1] == "sessions" && validPeerSessionID(parts[2]) {
		return method == "PATCH"
	}
	if (len(parts) != 4 && len(parts) != 5) || parts[0] != "api" || parts[1] != "sessions" || !validPeerSessionID(parts[2]) {
		return false
	}
	if len(parts) == 4 {
		return method == "GET" && (parts[3] == "terminal" || parts[3] == "output" || parts[3] == "conversation") ||
			method == "POST" && (parts[3] == "restart" || parts[3] == "stop" || parts[3] == "input" || parts[3] == "fork" || parts[3] == "tools")
	}
	if parts[4] == "" || parts[4] == "." || parts[4] == ".." {
		return false
	}
	if parts[3] == "blobs" && method == "GET" {
		_, err := validateBlobSHA(parts[4])
		return err == nil
	}
	if parts[3] != "conversation" {
		return false
	}
	if method == "GET" {
		return parts[4] == "config-options"
	}
	return method == "POST" && (parts[4] == "prompt" || parts[4] == "permission" || parts[4] == "cancel" || parts[4] == "elicitation" || parts[4] == "config" || parts[4] == "blobs")
}

func validPeerSessionID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\")
}
func (g *Gateway) tailnetTarget(ctx context.Context, id string) (*url.URL, error) {
	config, err := ReadSharingConfig(g.store, g.options.Home)
	if err != nil || !config.Enabled {
		return nil, errors.New("tailnet sharing is disabled")
	}
	status, err := g.cachedTailnetStatus(ctx)
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
	target, err := g.tailnetTarget(r.Context(), id)
	if err != nil {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/conversation") && g.meshPeer(r.Context(), id) {
			g.serveConversation(w, r)
			return
		}
		status := http.StatusBadGateway
		if g.meshPeer(r.Context(), id) {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), status)
		return
	}
	g.proxyTailnetTo(w, r, id, target)
}

func (g *Gateway) proxyTailnetTo(w http.ResponseWriter, r *http.Request, id string, target *url.URL) {
	address := target.Host
	proxy := &httputil.ReverseProxy{}
	proxy.Rewrite = func(out *httputil.ProxyRequest) {
		out.SetURL(target)
		request := out.Out
		request.Host = address
		query := request.URL.Query()
		query.Del("node")
		request.URL.RawQuery = query.Encode()
		request.Header.Del("Authorization")
		if trace := obs.TraceParent(r.Context()); trace != "" {
			request.Header.Set("traceparent", trace)
		}
		request.Header.Del("Cookie")
		request.Header.Del("X-Agentpack-User")
		if actor, ok := actorFrom(r.Context()); ok && (actor.Account || actor.Kind == "browser" || actor.Kind == "mcp") {
			request.Header.Set("X-Agentpack-User", actor.User)
		}
		request.Header.Del("Origin")
		request.Header.Del("Forwarded")
		request.Header.Del("X-Forwarded-For")
		request.Header.Del("X-Forwarded-Host")
		request.Header.Del("X-Forwarded-Proto")
	}
	proxy.Transport = tailnetRequestTransport(r.Method, r.URL.Path)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		if event := obs.From(r.Context()); event != nil {
			event.Fail(err)
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/conversation") && g.meshPeer(r.Context(), id) {
			g.serveConversation(w, r)
			return
		}
		log.Printf("tailnet proxy failed for node %s: %v", id, err)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "remote node connection failed; the gateway logged the network error", http.StatusBadGateway)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.StatusCode >= 500 && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/conversation") && g.meshPeer(r.Context(), id) {
			return errors.New("remote conversation is temporarily unavailable")
		}

		response.Header.Del("Set-Cookie")
		response.Header.Del("X-Agentpack-Trace")
		if response.Header.Get("Cache-Control") == "" {
			response.Header.Set("Cache-Control", "no-store")
		}
		if response.StatusCode == http.StatusSwitchingProtocols {
			private := r.Header.Get("Origin") == "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.local.Secret)) == 1
			// The proxy clears response.Body once it owns the upgraded connection, so keep this reference.
			go closeUpgradeOnEnd(r.Context(), g.done, response.Body, time.Second, func() bool { return private || g.auth.authenticated(r) })
		}
		return nil
	}
	proxy.ServeHTTP(w, r)
}

func slowTailnetRequest(method, path string) bool {
	return path == "/api/agent-sessions" || path == "/api/limits" || strings.HasPrefix(path, "/api/library/") ||
		method != http.MethodGet && method != http.MethodHead
}

func tailnetRequestTransport(method, path string) *http.Transport {
	if path == "/api/agent-sessions" {
		return tailnetProbeTransport
	}
	if slowTailnetRequest(method, path) {
		return tailnetMutationTransport
	}
	return tailnetTransport
}

// closeUpgradeOnEnd holds an upgraded proxy connection open until its request ends, and closes it
// when the gateway stops or the viewer's login no longer holds.
func closeUpgradeOnEnd(ctx context.Context, done <-chan struct{}, connection io.Closer, every time.Duration, authenticated func() bool) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			_ = connection.Close()
			return
		case <-ticker.C:
			if !authenticated() {
				_ = connection.Close()
				return
			}
		}
	}
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
		config, err := ReadSharingConfig(g.store, g.options.Home)
		if err != nil || !config.Enabled {
			http.Error(w, "sharing disabled", 403)
			return
		}
		identity, err := g.cachedTailnetWhois(r.Context(), ip)
		if err != nil {
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
			writeJSON(w, TailnetNode{ID: self.ID, Name: self.HostName, Online: true, Version: g.options.Version})
			return
		}
		actor := Actor{Kind: "peer", User: identity.UserProfile.LoginName, Node: identity.Node.ComputedName, StableNodeID: identity.Node.StableID}
		actor, err = g.forwardedAccount(r.Context(), actor, r.Header.Get("X-Agentpack-User"))
		if err != nil {
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
			http.Error(w, "forwarded account refused", http.StatusForbidden)
			return
		}

		request := r.Clone(withActor(context.WithValue(r.Context(), replicaPeerKey{}, identity.Node.StableID), actor))
		request.Header = r.Header.Clone()
		request.Header.Del("X-Agentpack-User")
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
	// The loop retries every five seconds, so log a problem when it changes, not on every tick.
	var problem string
	report := func(err error) {
		text := ""
		if err != nil {
			text = err.Error()
		}
		if text != problem && text != "" {
			log.Printf("tailnet sharing is not listening: %s", text)
		}
		problem = text
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		config, err := ReadSharingConfig(g.store, g.options.Home)
		if err != nil || !config.Enabled {
			closeServer()
			report(err)
		} else {
			status, err := tailnetSnapshot(ctx)
			if err == nil {
				err = g.store.BindNode(ctx, status.Self.ID)
			}
			address := tailnetAddress(status.Self)
			if err != nil || address == "" {
				closeServer()
				if err == nil {
					err = errors.New("this machine has no Tailscale address")
				}
				report(err)
			} else {
				currentPolicy, _ := json.Marshal(struct {
					Config SharingConfig
					Self   *tailnetPeer
				}{config, status.Self})
				signature := string(currentPolicy)
				if server == nil || bound != address || policy != signature {
					closeServer()
					listener, err := net.Listen("tcp", address)
					report(err)
					if err == nil {
						tracking = &peerConnections{connections: make(map[net.Conn]bool)}
						server = &http.Server{Handler: g.peerHandler(status.Self, address), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
						bound = address
						policy = signature
						serving, peer := server, peerListener{listener, tracking}
						obs.Go("gateway.peer_listener", func() { _ = serving.Serve(peer) })
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
