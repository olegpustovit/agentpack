package mesh

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/state"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
)

const Port = "7421"

// SharingConfig contains explicit machine/tag exceptions to personal identity.
// It does not change Tailscale ACLs or expose anything outside the tailnet.
type SharingConfig struct {
	Enabled bool     `json:"enabled"`
	Allow   []string `json:"allow,omitempty"`
}
type Node struct {
	Main        bool   `json:"main"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Address     string `json:"-"`
	Online      bool   `json:"online"`
	NeedsUpdate bool   `json:"needs_update,omitempty"`
	// Version is what a peer's /peer answer reports; empty from a peer that predates it.
	Version string `json:"version,omitempty"`
	Schema  int    `json:"schema_version,omitempty"`
}
type Peer struct {
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
type Status struct {
	BackendState string
	Self         *Peer
	Peer         map[string]*Peer
	User         map[string]User
}
type User struct {
	LoginName string
}
type Identity struct {
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
func ReadSharingConfig(store *state.Store) (SharingConfig, error) {
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
	return config, nil
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

// Tailscale finds the daemon itself on every install, including the macOS app builds that
// put no `tailscale` command on PATH. Tests point it at a missing socket.
var Tailscale local.Client
var tailnetStatusRequests singleflight.Group

const tailscaleTimeout = 3 * time.Second

func Snapshot(ctx context.Context) (Status, error) {
	result := tailnetStatusRequests.DoChan("status", func() (any, error) {
		return readSnapshot(context.WithoutCancel(ctx))
	})
	select {
	case <-ctx.Done():
		return Status{}, ctx.Err()
	case answer := <-result:
		return answer.Val.(Status), answer.Err
	}
}

func readSnapshot(ctx context.Context) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, tailscaleTimeout)
	defer cancel()
	raw, err := Tailscale.Status(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("Tailscale is unavailable: %w", err)
	}
	status := statusFrom(raw)
	if status.BackendState != "Running" || status.Self == nil || status.Self.ID == "" {
		return status, errors.New("Tailscale is not connected")
	}
	return status, nil
}

func statusFrom(raw *ipnstate.Status) Status {
	status := Status{BackendState: raw.BackendState, Self: peerFrom(raw.Self), Peer: map[string]*Peer{}, User: map[string]User{}}
	for key, peer := range raw.Peer {
		status.Peer[key.String()] = peerFrom(peer)
	}
	for id, user := range raw.User {
		status.User[strconv.FormatInt(int64(id), 10)] = User{LoginName: user.LoginName}
	}
	return status
}

func peerFrom(peer *ipnstate.PeerStatus) *Peer {
	if peer == nil {
		return nil
	}
	result := &Peer{ID: string(peer.ID), HostName: peer.HostName, DNSName: peer.DNSName, OS: peer.OS, UserID: int64(peer.UserID), Online: peer.Online, CurAddr: peer.CurAddr, Relay: peer.Relay, PeerRelay: peer.PeerRelay, LastSeen: peer.LastSeen, RxBytes: peer.RxBytes, TxBytes: peer.TxBytes}
	for _, address := range peer.TailscaleIPs {
		result.TailscaleIPs = append(result.TailscaleIPs, address.String())
	}
	if peer.Tags != nil {
		result.Tags = peer.Tags.AsSlice()
	}
	return result
}

// cacheTTL bounds how stale a proxied request's view of peers and their identities may be.
const cacheTTL = 5 * time.Second

// Cache spares every proxied or peer request its own Tailscale round trips. Failures are never cached.
type Cache struct {
	mu       sync.Mutex
	statusAt time.Time
	status   Status
	whois    map[netip.Addr]cachedWhois
}

type cachedWhois struct {
	at       time.Time
	identity Identity
}

// Status is the local daemon's status, at most cacheTTL old.
func (c *Cache) Status(ctx context.Context) (Status, error) {
	c.mu.Lock()
	if !c.statusAt.IsZero() && time.Since(c.statusAt) < cacheTTL {
		status := c.status
		c.mu.Unlock()
		return status, nil
	}
	c.mu.Unlock()
	status, err := Snapshot(ctx)
	if err == nil {
		c.mu.Lock()
		c.status, c.statusAt = status, time.Now()
		c.mu.Unlock()
	}
	return status, err
}

// Whois names the node behind a tailnet address, at most cacheTTL old.
func (c *Cache) Whois(ctx context.Context, address netip.Addr) (Identity, error) {
	c.mu.Lock()
	if hit, ok := c.whois[address]; ok && time.Since(hit.at) < cacheTTL {
		c.mu.Unlock()
		return hit.identity, nil
	}
	c.mu.Unlock()
	identity, err := whois(ctx, address)
	if err == nil {
		c.mu.Lock()
		if c.whois == nil || len(c.whois) >= 256 {
			c.whois = map[netip.Addr]cachedWhois{}
		}
		c.whois[address] = cachedWhois{at: time.Now(), identity: identity}
		c.mu.Unlock()
	}
	return identity, err
}

// whois names the node behind a tailnet address. An unknown address is an error.
func whois(ctx context.Context, address netip.Addr) (Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, tailscaleTimeout)
	defer cancel()
	raw, err := Tailscale.WhoIs(ctx, address.String())
	if err != nil {
		return Identity{}, err
	}
	if raw.Node == nil {
		return Identity{}, errors.New("Tailscale returned no node")
	}
	var identity Identity
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

func AllowedPeer(self, peer *Peer, config SharingConfig) bool {
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
func Address(peer *Peer) string {
	if peer == nil {
		return ""
	}
	for _, raw := range peer.TailscaleIPs {
		address, err := netip.ParseAddr(raw)
		if err == nil && (netip.MustParsePrefix("100.64.0.0/10").Contains(address) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(address)) {
			return net.JoinHostPort(address.String(), Port)
		}
	}
	return ""
}

var Transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 5 * time.Second, IdleConnTimeout: 30 * time.Second}
var ProbeTransport = func() *http.Transport {
	transport := Transport.Clone()
	transport.ResponseHeaderTimeout = time.Minute
	return transport
}()
var MutationTransport = func() *http.Transport {
	transport := Transport.Clone()
	transport.ResponseHeaderTimeout = 5 * time.Minute
	return transport
}()
var HTTP = &http.Client{Transport: Transport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// Probe is one successful /peer answer and the round trip it took.
type Probe struct {
	Node Node
	RTT  time.Duration
}
type ProbeFunc func(context.Context, *Peer) (Probe, bool)

// ProbePeer asks one peer's gateway who it is; a failed or mismatched answer is not reachable.
func ProbePeer(ctx context.Context, peer *Peer) (Probe, bool) {
	request, err := http.NewRequestWithContext(ctx, "GET", "http://"+Address(peer)+"/peer", nil)
	if err != nil {
		return Probe{}, false
	}
	start := time.Now()
	response, err := HTTP.Do(request)
	if err != nil {
		return Probe{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return Probe{}, false
	}
	var remote Node
	if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&remote) != nil || remote.ID != peer.ID {
		return Probe{}, false
	}
	remote.Address = Address(peer)
	remote.Name = peer.HostName
	remote.Online = true
	return Probe{Node: remote, RTT: time.Since(start)}, true
}

// probePeers probes peers concurrently, eight at a time, and returns successful answers by peer ID.
func probePeers(ctx context.Context, peers []*Peer, probe ProbeFunc) (map[string]Probe, error) {
	results := map[string]Probe{}
	var mu sync.Mutex
	var group errgroup.Group
	group.SetLimit(8)
	for _, peer := range peers {
		if ctx.Err() != nil {
			break
		}
		group.Go(func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if result, ok := probe(ctx, peer); ok {
				mu.Lock()
				results[peer.ID] = result
				mu.Unlock()
			}
			return nil
		})
	}
	err := group.Wait()
	return results, errors.Join(err, ctx.Err())
}

// Discover returns the allowed, online peers that answer their gateway probe; none while sharing is off.
func Discover(ctx context.Context, config SharingConfig) ([]Node, error) {
	if !config.Enabled {
		return []Node{}, nil
	}
	status, err := Snapshot(ctx)
	if err != nil {
		return []Node{}, err
	}
	return nodesFrom(ctx, status, config, ProbePeer)
}

// nodesFrom returns the allowed, online peers that answer their gateway probe.
func nodesFrom(ctx context.Context, status Status, config SharingConfig, probe ProbeFunc) ([]Node, error) {
	nodes := []Node{}
	if !config.Enabled {
		return nodes, nil
	}
	var peers []*Peer
	for _, peer := range status.Peer {
		if peer != nil && peer.Online && AllowedPeer(status.Self, peer, config) && Address(peer) != "" {
			peers = append(peers, peer)
		}
	}
	probed, err := probePeers(ctx, peers, probe)
	for _, result := range probed {
		nodes = append(nodes, result.Node)
	}
	slices.SortFunc(nodes, func(a, b Node) int { return strings.Compare(a.Name, b.Name) })
	return nodes, err
}

// NameFromTailnet replaces a node name that is only its stable ID with the peer's tailnet host name.
func NameFromTailnet(nodes map[string]Node, status Status) {
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

// Target is the gateway address of an online, allowed peer.
func (c *Cache) Target(ctx context.Context, store *state.Store, id string) (*url.URL, error) {
	config, err := ReadSharingConfig(store)
	if err != nil || !config.Enabled {
		return nil, errors.New("tailnet sharing is disabled")
	}
	status, err := c.Status(ctx)
	if err != nil {
		return nil, err
	}
	for _, peer := range status.Peer {
		if peer != nil && peer.ID == id && peer.Online && AllowedPeer(status.Self, peer, config) {
			if address := Address(peer); address != "" {
				return &url.URL{Scheme: "http", Host: address}, nil
			}
		}
	}
	return nil, errors.New("node is unavailable or not allowed")
}

func allowedIdentity(self *Peer, identity Identity, address netip.Addr, config SharingConfig) bool {
	peer := &Peer{ID: identity.Node.StableID, UserID: identity.Node.User, Tags: identity.Node.Tags}
	if !AllowedPeer(self, peer, config) {
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

// CheckTailnet verifies the machine can use local Tailscale identity services.
func CheckTailnet(ctx context.Context) error {
	_, err := Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("enable Tailscale first: %w", err)
	}
	return nil
}
