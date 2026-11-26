package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
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

// waitingResume is a gateway that restarted with one cut-off chat and has not caught up. Its clock
// only moves by advance, and every launch reports the sign-in the agent would read.
func waitingResume(t *testing.T) (g *Gateway, launched chan string, advance func(time.Duration), path string) {
	t.Helper()
	home := t.TempDir()
	path = filepath.Join(t.TempDir(), "token")
	launched = make(chan string, 4)
	g, _ = identityGateway(t, home, func(_ context.Context, info session.SessionInfo, _ string) (string, error) {
		data, _ := os.ReadFile(path)
		launched <- string(data)
		return info.ID, nil
	})
	// The resume syncs sign-ins: never let a test reach this machine's real ones.
	saved := network.Agents
	t.Cleanup(func() { network.Agents = saved })
	network.Agents = []registry.Entry{{Harness: harness.Harness{Target: harness.Agy}, Login: &harness.LoginSpec{Credential: func() string { return path }}}}
	writeCredential(t, path, []byte(`{"n":1}`))
	g.syncAgentCredentials(t.Context(), "")
	startCatchingUp(t, g)
	var elapsed atomic.Int64
	start := time.Now().Round(0)
	g.clock = func() time.Time { return start.Add(time.Duration(elapsed.Load())) }
	g.syncSince.Store(start.UnixNano())
	g.catchUpPoll = time.Millisecond
	advance = func(by time.Duration) {
		now := start.Add(time.Duration(elapsed.Add(int64(by))))
		// Until the waiting resume has looked at the new time.
		for deadline := time.Now().Add(time.Second); g.lastLook.Load() < now.UnixNano() && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
	}
	stuckChat(t, g, seedFolder(t, home), "session-dead")
	return g, launched, advance, path
}

func notLaunched(t *testing.T, launched chan string) {
	t.Helper()
	select {
	case got := <-launched:
		t.Fatal("resumed early with", got)
	default:
	}
}

func TestResumeWaitsUntilCaughtUp(t *testing.T) {
	g, launched, advance, _ := waitingResume(t)
	events, unsubscribe := g.listen()
	defer unsubscribe()
	g.reapStoppedChats(t.Context())
	g.reapStoppedChats(t.Context()) // the 15 s reaper sees it again
	if message := <-events; message.Topic != "sessions" {
		t.Fatalf("event %+v", message)
	}
	row := httptest.NewRequest("GET", "/api/sessions/session-dead", nil)
	row.SetPathValue("id", "session-dead")
	one, all := httptest.NewRecorder(), httptest.NewRecorder()
	g.serveSessionRow(one, row)
	g.serveSessions(all, httptest.NewRequest("GET", "/api/sessions", nil))
	if !strings.Contains(one.Body.String(), `"resume_pending":true`) || !strings.Contains(all.Body.String(), `"resume_pending":true`) {
		t.Fatalf("pending not shown: %s %s", one.Body, all.Body)
	}
	advance(resumeCatchUp - time.Second)
	notLaunched(t, launched)

	// The peer's newer sign-in arrives with the pull, and is installed before the agent starts.
	_, peerPublish := credentialRow(t, g, "agy/token")
	value, _ := json.Marshal(map[string]int{"n": 2})
	peerPublish(sharedAgentCredential{Data: value, Rev: 2, Node: "birch"})
	g.replicaStatus.Store("birch", replicaContact{LastSuccess: g.wallNow()})
	g.starting.Wait()
	if got := <-launched; got != `{"n":2}` {
		t.Fatal("resumed with", got)
	}
	notLaunched(t, launched)
	if g.ResumePending("session-dead") {
		t.Fatal("still pending after the resume")
	}
}

func TestResumeHappensAnywayAtTheLimit(t *testing.T) {
	g, launched, advance, _ := waitingResume(t)
	g.reapStoppedChats(t.Context())
	notLaunched(t, launched)
	advance(resumeCatchUp)
	g.starting.Wait()
	if got := <-launched; got != `{"n":1}` {
		t.Fatal("resumed with", got)
	}
}

func TestResumeByHandEndsTheWait(t *testing.T) {
	g, launched, advance, _ := waitingResume(t)
	g.reapStoppedChats(t.Context())
	if _, err := g.resumeSession(t.Context(), "session-dead", "", ""); err != nil {
		t.Fatal(err)
	}
	<-launched
	// The chat stops again before the wait is over; it is left alone.
	if _, err := g.store.DB.Exec("UPDATE sessions SET ended_at=2 WHERE id='session-dead'"); err != nil {
		t.Fatal(err)
	}
	advance(resumeCatchUp)
	g.starting.Wait()
	notLaunched(t, launched)
}
