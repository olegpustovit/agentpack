package gateway

import (
	"context"
	"encoding/json"
	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/network"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The newest sign-in file wins across nodes, and a web UI sign-out stops sharing without deleting files.
func TestAgentCredentialsShareTheNewestSignIn(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "agy", "token")
	saved := network.Agents
	t.Cleanup(func() { network.Agents = saved })
	renewals := 0
	agentRewrite := "" // the agent writes this file while a renewal is in flight
	network.Agents = []registry.Entry{{Harness: harness.Harness{Target: harness.Agy}, Login: &harness.LoginSpec{Credential: func() string { return path }, Refresh: func(_ context.Context, data []byte) ([]byte, error) {
		if !strings.HasPrefix(string(data), `{"renew"`) {
			return nil, nil // not due
		}
		renewals++
		if agentRewrite != "" {
			if err := os.WriteFile(path, []byte(agentRewrite), 0o600); err != nil {
				return nil, err
			}
		}
		return []byte(`{"renewed":1}`), nil
	}}}}
	g, _ := identityGateway(t, home, nil)
	ctx := context.Background()
	write := func(data string, modified time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		data, _ := os.ReadFile(path)
		return string(data)
	}
	shared := func() sharedAgentCredential {
		t.Helper()
		credential, err := g.sharedAgentCredential(ctx, "agy/token")
		if err != nil {
			t.Fatal(err)
		}
		return credential
	}
	// peerPublish writes the row as replication would, leaving this machine's install record alone.
	peerPublish := func(credential sharedAgentCredential) error {
		value, _ := json.Marshal(credential)
		_, err := g.store.DB.Exec("INSERT INTO agent_credentials(key,value) VALUES ('agy/token',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(value))
		return err
	}
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	future := time.Now().Add(24 * time.Hour).UnixMilli() // a peer whose clock runs a day ahead

	write(`{"a":1}`, base)
	g.syncAgentCredentials(ctx, "")
	if got := shared(); string(got.Data) != `{"a":1}` || got.Rev != 1 {
		t.Fatalf("local sign-in not published: %+v", got)
	}

	// A machine without the file gets it.
	os.Remove(path)
	g.syncAgentCredentials(ctx, "")
	if info, err := os.Stat(path); read() != `{"a":1}` || err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("shared sign-in not installed: %q %v", read(), err)
	}

	// A peer's later revision replaces this one, whatever its clock says.
	if err := peerPublish(sharedAgentCredential{Data: []byte(`{"b":2}`), Rev: 2, Modified: future}); err != nil {
		t.Fatal(err)
	}
	g.syncAgentCredentials(ctx, "")
	if read() != `{"b":2}` {
		t.Fatalf("newer peer sign-in not installed: %q", read())
	}
	// A local sign-in made afterwards is published, though its file time is older than the peer's row.
	write(`{"c":3}`, base)
	g.syncAgentCredentials(ctx, "")
	if got := shared(); string(got.Data) != `{"c":3}` || got.Rev != 3 || read() != `{"c":3}` {
		t.Fatalf("local sign-in was overwritten by a future-dated row: %+v %q", got, read())
	}
	g.syncAgentCredentials(ctx, "")
	if got := shared(); got.Rev != 3 {
		t.Fatalf("unchanged sign-in republished: %+v", got)
	}

	// Only the machine that published a sign-in renews it, so a rotating token is spent once.
	if err := peerPublish(sharedAgentCredential{Data: []byte(`{"renew":1}`), Rev: 4, Node: "peer"}); err != nil {
		t.Fatal(err)
	}
	g.syncAgentCredentials(ctx, "") // installs the peer's
	g.syncAgentCredentials(ctx, "") // and leaves its renewal to the peer
	if renewals != 0 || read() != `{"renew":1}` {
		t.Fatalf("renewed a peer's sign-in: %d %q", renewals, read())
	}
	write(`{"renew":2}`, base)
	g.syncAgentCredentials(ctx, "") // publishes its own
	g.syncAgentCredentials(ctx, "") // and renews it
	if got := shared(); renewals != 1 || read() != `{"renewed":1}` || string(got.Data) != `{"renewed":1}` || got.Rev != 6 {
		t.Fatalf("own sign-in not renewed: %d %q %+v", renewals, read(), got)
	}

	// A row without a publisher is adopted by a machine holding the same file.
	if err := peerPublish(sharedAgentCredential{Data: []byte(`{"renewed":1}`), Rev: shared().Rev}); err != nil {
		t.Fatal(err)
	}
	g.syncAgentCredentials(ctx, "")
	if got := shared(); got.Node == "" || string(got.Data) != `{"renewed":1}` {
		t.Fatalf("unowned sign-in not adopted: %+v", got)
	}

	// A file the agent is still writing is left alone.
	write(`{"d":`, base)
	g.syncAgentCredentials(ctx, "")
	if read() != `{"d":` {
		t.Fatalf("half-written file was replaced: %q", read())
	}

	// After a sign-out, the file a machine still holds is neither republished nor removed, nor is
	// a later change to it (an agent rotating its token looks the same as a CLI sign-in);
	// only a web UI sign-in shares again.
	if err := g.stopSharingAgentCredential(ctx, network.Agents[0]); err != nil {
		t.Fatal(err)
	}
	write(`{"renewed":1}`, base)
	g.syncAgentCredentials(ctx, "")
	if got := shared(); got.Data != nil || read() != `{"renewed":1}` {
		t.Fatalf("sign-out did not stop sharing: %+v %q", got, read())
	}
	write(`{"rotated":1}`, base)
	g.syncAgentCredentials(ctx, "")
	if got := shared(); got.Data != nil {
		t.Fatalf("a rotated token undid the sign-out: %+v", got)
	}
	write(`{"e":5}`, base)
	g.syncAgentCredentials(ctx, string(harness.Agy))
	if got := shared(); string(got.Data) != `{"e":5}` {
		t.Fatalf("web sign-in after sign-out not shared: %+v", got)
	}

	// On first sight the shared sign-in wins over a local file whatever the clocks say; the local one is kept.
	forget := func() {
		t.Helper()
		if _, err := g.store.DB.Exec("DELETE FROM agent_credential_installs"); err != nil {
			t.Fatal(err)
		}
	}
	forget()
	write(`{"stale":1}`, time.Now().Add(48*time.Hour))
	g.syncAgentCredentials(ctx, "")
	backup, _ := os.ReadFile(path + ".bak")
	if got := shared(); read() != `{"e":5}` || string(got.Data) != `{"e":5}` || string(backup) != `{"stale":1}` {
		t.Fatalf("first sight published a stale file: shared %+v local %q backup %q", got, read(), backup)
	}
	// And a machine first seeing a sign-out keeps its file to itself.
	if err := g.stopSharingAgentCredential(ctx, network.Agents[0]); err != nil {
		t.Fatal(err)
	}
	forget()
	g.syncAgentCredentials(ctx, "")
	if got := shared(); got.Data != nil || read() != `{"e":5}` {
		t.Fatalf("first sight republished after a sign-out: %+v %q", got, read())
	}

	// A token the agent writes while this machine renews is kept, not overwritten, and shared next round.
	write(`{"renew":3}`, base)
	g.syncAgentCredentials(ctx, string(harness.Agy)) // a web sign-in here publishes it
	agentRewrite = `{"agent":1}`
	g.syncAgentCredentials(ctx, "") // renews, but the agent rewrote the file meanwhile
	agentRewrite = ""
	if read() != `{"agent":1}` {
		t.Fatalf("renewal overwrote the agent's newer token: %q", read())
	}
	g.syncAgentCredentials(ctx, "")
	if got := shared(); string(got.Data) != `{"agent":1}` {
		t.Fatalf("the agent's newer token was not shared: %+v", got)
	}
}
