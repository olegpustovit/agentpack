package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/mcp"
)

// startCatchingUp enrolls two peers on g, which starts now on a clock the test moves.
func startCatchingUp(t *testing.T, g *Gateway) (now *time.Time) {
	t.Helper()
	g.options.Library.Store = replicaStore(t)
	for _, peer := range []string{"birch", "cedar"} {
		if err := g.options.Library.Store.EnrollPeer(peer); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().Round(0)
	g.clock = func() time.Time { return at }
	g.syncSince.Store(at.UnixNano())
	return &at
}

// pass moves the clock as a running gateway sees it: the credential loop looks every 30 s.
func pass(g *Gateway, now *time.Time, elapsed time.Duration) {
	for ; elapsed > 0; elapsed -= 30 * time.Second {
		*now = now.Add(min(elapsed, 30*time.Second))
		g.noticeWake()
	}
}

// countedRenewal publishes a sign-in as this gateway's own and returns how often a round asked to renew it.
func countedRenewal(t *testing.T, g *Gateway, node string) func() int {
	t.Helper()
	asked := 0
	agent := registry.Entry{Harness: harness.Harness{Target: harness.Agy}, Login: &harness.LoginSpec{Refresh: func(context.Context, []byte) ([]byte, error) {
		asked++
		return nil, nil
	}}}
	path := filepath.Join(t.TempDir(), "token")
	writeCredential(t, path, []byte(`{"n":1}`))
	round := func() int {
		t.Helper()
		before := asked
		if _, err := g.syncAgentCredential(t.Context(), agent, path, node, false); err != nil {
			t.Fatal(err)
		}
		return asked - before
	}
	round() // publishes
	return round
}

func TestCaughtUpGatesAgentRenewal(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	now := startCatchingUp(t, g)
	renews := countedRenewal(t, g, "local")
	if renews() != 0 {
		t.Fatal("renewed with no peer contacted")
	}
	g.replicaStatus.Store("birch", replicaContact{At: *now, Error: "unreachable"})
	g.replicaStatus.Store("cedar", replicaContact{At: *now, Error: "unreachable", LastSuccess: now.Add(-time.Hour)})
	if renews() != 0 {
		t.Fatal("renewed after failed attempts and a pull from before the start")
	}
	g.replicaStatus.Store("birch", replicaContact{LastSuccess: now.Add(time.Second)})
	if renews() != 1 {
		t.Fatal("did not renew after a successful pull")
	}

	g.replicaStatus.Delete("birch")
	library := g.options.Library.Store
	g.options.Library.Store = nil
	if renews() != 1 {
		t.Fatal("did not renew with state sync off")
	}
	g.options.Library.Store = library

	pass(g, now, catchUpLimit-time.Second)
	if renews() != 0 {
		t.Fatal("gave up before the limit")
	}
	pass(g, now, time.Second)
	if renews() != 1 {
		t.Fatal("an all-offline mesh never renews")
	}
}

func TestCaughtUpNeedsMainAndStartsOverAfterAWake(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	now := startCatchingUp(t, g)
	archiveRoles(t, g, "cedar", "cedar")
	g.replicaStatus.Store("birch", replicaContact{LastSuccess: now.Add(time.Second)})
	if g.caughtUp() {
		t.Fatal("a pull from another peer counted while Main is cedar")
	}
	g.replicaStatus.Store("cedar", replicaContact{LastSuccess: now.Add(time.Second)})
	if !g.caughtUp() {
		t.Fatal("a pull from Main did not count")
	}
	wake := make(chan struct{}, 1)
	g.statePulls.Store("cedar", wake)
	*now = now.Add(10 * time.Minute) // asleep: nothing looked
	if g.caughtUp() {
		t.Fatal("still caught up after a wake")
	}
	select {
	case <-wake:
	default:
		t.Fatal("peers were not pulled again")
	}
	g.replicaStatus.Store("cedar", replicaContact{LastSuccess: now.Add(time.Second)})
	if !g.caughtUp() {
		t.Fatal("the pull after the wake did not count")
	}
}

func TestMCPCredentialRenewalWaitsUntilCaughtUp(t *testing.T) {
	fake := newFakeMCPAuthServer(t)
	fake.refresh = "rt"
	g, _ := startCredentialGateway(t)
	now := startCatchingUp(t, g)
	expired := mcpToken{AccessToken: "old", RefreshToken: "rt", IssuedAt: time.Now().Add(-2 * time.Hour), Expiry: time.Now().Add(-time.Hour)}
	id, err := g.saveMCPCredential(t.Context(), mcpCredential{Kind: mcp.CredentialOAuth, Match: fake.URL + "/mcp", Label: "X", Account: testBrowserEmail,
		Client: mcpOAuthClient{ClientID: "dynamic-client", TokenURL: fake.URL + "/token"},
		State:  mcpCredentialState{Generation: 1, Status: mcpStatusConnected, Token: &expired}})
	if err != nil {
		t.Fatal(err)
	}
	g.renewMCPTokens(t.Context())
	if got := fake.refreshes.Load(); got != 0 {
		t.Fatalf("renewed in the background before catching up: %d", got)
	}
	// An agent asking for the secret waits, then gets a refresh although no peer ever answers.
	start := *now
	g.catchUpPoll = time.Millisecond
	g.clock = func() time.Time {
		*now = now.Add(time.Second)
		return *now
	}
	secret, err := g.freshMCPSecret(t.Context(), id, 0)
	if err != nil || secret.Generation != 2 || fake.refreshes.Load() != 1 {
		t.Fatalf("on demand: generation %d, refreshes %d, %v", secret.Generation, fake.refreshes.Load(), err)
	}
	if waited := now.Sub(start); waited < onDemandCatchUp || waited >= catchUpLimit {
		t.Fatalf("waited %v", waited)
	}
	// Once a pull succeeds, the background renewal runs again.
	expired.RefreshToken = "rt-1"
	if err := g.writeMCPState(t.Context(), id, mcpCredentialState{Generation: 3, Status: mcpStatusConnected, Token: &expired}); err != nil {
		t.Fatal(err)
	}
	g.replicaStatus.Store("birch", replicaContact{LastSuccess: now.Add(time.Hour)})
	g.renewMCPTokens(t.Context())
	if got := fake.refreshes.Load(); got != 2 {
		t.Fatalf("not renewed after catching up: %d", got)
	}
}
