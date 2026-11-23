package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
)

// After a sign-in, only the running chats of that agent whose last word was a sign-in request restart.
func TestSignInRestartsTheChatsThatWaitedForIt(t *testing.T) {
	home := t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: auth.store, auth: auth, options: GatewayOptions{Home: home}}
	var mu sync.Mutex
	var restarted []string
	chat := func(id, harness string, notice map[string]any) {
		t.Helper()
		secret := strings.Repeat("s", 64)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/state":
				network.WriteJSON(w, session.TerminalState{Running: true, ExitCode: -1})
			case "/restart":
				mu.Lock()
				restarted = append(restarted, id)
				mu.Unlock()
				network.WriteJSON(w, map[string]bool{"restarted": true})
			}
		}))
		t.Cleanup(server.Close)
		data, _ := json.Marshal(network.WorkerRecord{ID: id, Secret: secret, Address: strings.TrimPrefix(server.URL, "http://")})
		directory := filepath.Join(home, "network", "sessions", id)
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := session.UpsertSession(t.Context(), auth.store.DB, session.SessionInfo{ID: id, Harness: harness, Transport: "acp"}); err != nil {
			t.Fatal(err)
		}
		notice["sessionUpdate"] = "notice"
		entries := []acp.LogEntry{{Seq: 1, Kind: "notice", Payload: network.MustRawJSON(notice)}}
		if err := network.PersistConversationChangesSince(t.Context(), auth.store.DB, id, acp.LogChanges{Entries: entries, LastSeq: 1}, 0, false); err != nil {
			t.Fatal(err)
		}
	}
	chat("session-waiting", "claude", map[string]any{"code": "auth_required", "harness": "claude"})
	chat("session-other-agent", "codex", map[string]any{"code": "auth_required", "harness": "codex"})
	chat("session-unrelated", "claude", map[string]any{"title": "Interrupted to steer"})
	chat("session-older-adapter", "claude", map[string]any{"title": "The agent could not run this prompt", "description": `{"code":-32000,"message":"Authentication required"}`})
	queue := obs.NewQueue()
	g.reloadSignedOutChats(obs.WithSink(t.Context(), queue, obs.Metadata{}), "claude")
	slices.Sort(restarted)
	if !slices.Equal(restarted, []string{"session-older-adapter", "session-waiting"}) {
		t.Fatalf("restarted %v", restarted)
	}
	for len(queue.Events) > 0 {
		if event := <-queue.Events; event.Level == "error" {
			t.Fatalf("recorded failure: %v", event.Attrs)
		}
	}
}
