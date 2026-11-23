package mesh

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/views"

	"github.com/olegpustovit/agentpack/internal/state"
)

type statusRoundTripper func(*http.Request) (*http.Response, error)

func (f statusRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type closeRecorder chan struct{}

func (c closeRecorder) Close() error { close(c); return nil }

func TestPersonalTailnetPolicy(t *testing.T) {
	self := &Peer{ID: "self", UserID: 7}
	for _, test := range []struct {
		name  string
		peer  *Peer
		allow []string
		want  bool
	}{
		{"own device", &Peer{ID: "own", UserID: 7}, nil, true},
		{"other user", &Peer{ID: "other", UserID: 8}, nil, false},
		{"no user", &Peer{ID: "unknown"}, nil, false},
		{"tag requires permission", &Peer{ID: "server", UserID: 7, Tags: []string{"tag:server"}}, nil, false},
		{"explicit server", &Peer{ID: "server", UserID: 8, Tags: []string{"tag:server"}}, []string{"server"}, true},
		{"explicit tag", &Peer{ID: "server", Tags: []string{"tag:server"}}, []string{"tag:server"}, true},
		{"other tag", &Peer{ID: "server", Tags: []string{"tag:other"}}, []string{"tag:server"}, false},
		{"missing identity", nil, []string{"server"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := AllowedPeer(self, test.peer, SharingConfig{Allow: test.allow}); got != test.want {
				t.Fatalf("allowed=%v want %v", got, test.want)
			}
		})
	}
	self.Tags = []string{"tag:gateway"}
	if AllowedPeer(self, &Peer{ID: "own", UserID: 7}, SharingConfig{}) {
		t.Fatal("tagged gateway must explicitly allow personal clients")
	}
}

func TestWhoIsRequiresExactDeviceAddress(t *testing.T) {
	self := &Peer{ID: "self", UserID: 7}
	var identity Identity
	identity.Node.StableID = "own"
	identity.Node.User = 7
	address := netip.MustParseAddr("100.100.1.2")
	for _, test := range []struct {
		prefix string
		want   bool
	}{{"100.100.1.2/32", true}, {"100.100.1.3/32", false}, {"100.100.1.0/24", false}, {"invalid", false}} {
		identity.Node.Addresses = []string{test.prefix}
		if got := allowedIdentity(self, identity, address, SharingConfig{}); got != test.want {
			t.Fatalf("%s allowed=%v", test.prefix, got)
		}
	}
	identity.Node.Addresses = []string{"100.100.1.2/32"}
	identity.Node.User = 8
	if allowedIdentity(self, identity, address, SharingConfig{}) {
		t.Fatal("different user admitted")
	}
}

func TestSharingConfigPersistsAndValidates(t *testing.T) {
	home := t.TempDir()
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	empty, err := ReadSharingConfig(store)
	if err != nil || !empty.Enabled {
		t.Fatalf("sharing should default on: %+v %v", empty, err)
	}
	if err := WriteSharingConfig(store, SharingConfig{}); err != nil {
		t.Fatal(err)
	}
	if off, err := ReadSharingConfig(store); err != nil || off.Enabled {
		t.Fatalf("explicit off must persist: %+v %v", off, err)
	}
	want := SharingConfig{Enabled: true, Allow: []string{"tag:server", "stable-node"}}
	if err := WriteSharingConfig(store, want); err != nil {
		t.Fatal(err)
	}
	actual, err := ReadSharingConfig(store)
	if err != nil || !actual.Enabled || !slices.Equal(actual.Allow, want.Allow) {
		t.Fatalf("loaded: %+v %v", actual, err)
	}
	if err := WriteSharingConfig(store, SharingConfig{Allow: []string{"http://arbitrary"}}); err == nil {
		t.Fatal("accepted arbitrary URL")
	}
}

func TestCloseUpgradeOnEndClosesWhenGatewayStopsOrLoginEnds(t *testing.T) {
	wait := func(t *testing.T, closed closeRecorder) {
		t.Helper()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("connection was not closed")
		}
	}
	t.Run("gateway stops", func(t *testing.T) {
		closed, done := make(closeRecorder), make(chan struct{})
		go closeUpgradeOnEnd(context.Background(), done, closed, time.Hour, func() bool { return true })
		close(done)
		wait(t, closed)
	})
	t.Run("login ends", func(t *testing.T) {
		closed := make(closeRecorder)
		go closeUpgradeOnEnd(context.Background(), nil, closed, time.Millisecond, func() bool { return false })
		wait(t, closed)
	})
	t.Run("request ends", func(t *testing.T) {
		closed := make(closeRecorder)
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() {
			closeUpgradeOnEnd(ctx, nil, closed, time.Hour, func() bool { return true })
			close(finished)
		}()
		cancel()
		<-finished
		select {
		case <-closed:
			t.Fatal("the proxy owns the connection once the request ends")
		default:
		}
	})
}

func TestTailnetStatusFromKeepsWhatPolicyReads(t *testing.T) {
	tags := views.SliceOf([]string{"tag:ci"})
	peer := &ipnstate.PeerStatus{ID: "n1", HostName: "mac", UserID: 7, Online: true, TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.100.1.2")}, Tags: &tags}
	var nodeKey key.NodePublic
	status := statusFrom(&ipnstate.Status{
		BackendState: "Running",
		Self:         &ipnstate.PeerStatus{ID: "self", UserID: 7},
		Peer:         map[key.NodePublic]*ipnstate.PeerStatus{nodeKey: peer},
		User:         map[tailcfg.UserID]tailcfg.UserProfile{7: {LoginName: "me@"}},
	})
	got := status.Peer[nodeKey.String()]
	if got == nil || got.ID != "n1" || got.UserID != 7 || !got.Online || !slices.Equal(got.Tags, []string{"tag:ci"}) {
		t.Fatalf("peer: %+v", got)
	}
	if Address(got) != "100.100.1.2:7421" {
		t.Fatalf("address: %q", Address(got))
	}
	if status.User["7"].LoginName != "me@" || status.Self.UserID != 7 {
		t.Fatalf("status: %+v", status)
	}
	if peerFrom(nil) != nil {
		t.Fatal("a stopped daemon reports no Self")
	}
}

func TestNameFromTailnetNamesOnlyUnnamedPeers(t *testing.T) {
	nodes := map[string]Node{
		"nA": {ID: "nA", Name: "nA"},
		"nB": {ID: "nB", Name: "kept"},
		"nC": {ID: "nC", Name: "nC"},
	}
	status := Status{Peer: map[string]*Peer{
		"k1": {ID: "nA", HostName: "gigacomp"},
		"k2": {ID: "nB", HostName: "other"},
		"k3": nil,
	}}
	NameFromTailnet(nodes, status)
	if nodes["nA"].Name != "gigacomp" || nodes["nB"].Name != "kept" || nodes["nC"].Name != "nC" {
		t.Fatalf("names = %v", nodes)
	}
}

func TestConcurrentTailnetSnapshotsShareLocalAPI(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		Tailscale = local.Client{OmitAuth: true, Transport: statusRoundTripper(func(_ *http.Request) (*http.Response, error) {
			calls.Add(1)
			<-release
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
				`{"BackendState":"Running","Self":{"ID":"node"}}`,
			))}, nil
		})}
		t.Cleanup(func() { Tailscale = local.Client{} })
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				status, err := Snapshot(t.Context())
				if err != nil || status.Self == nil || status.Self.ID != "node" {
					t.Errorf("status = %+v, err = %v", status, err)
				}
			})
		}
		synctest.Wait()
		close(release)
		wg.Wait()
		if got := calls.Load(); got != 1 {
			t.Fatalf("LocalAPI calls = %d, want 1", got)
		}
	})
}

func TestTailnetAddressesAreBounded(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "192.168.1.1", "example.com", "169.254.169.254", "::1"} {
		if Address(&Peer{TailscaleIPs: []string{address}}) != "" {
			t.Fatalf("unsafe address %s", address)
		}
	}
	if got := Address(&Peer{TailscaleIPs: []string{"100.100.1.2"}}); got != "100.100.1.2:7421" {
		t.Fatal(got)
	}
	if got := Address(&Peer{TailscaleIPs: []string{"fd7a:115c:a1e0::1"}}); got != "[fd7a:115c:a1e0::1]:7421" {
		t.Fatal(got)
	}
}
