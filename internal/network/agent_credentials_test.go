package network

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The newest sign-in file wins across nodes, and a web UI sign-out stops sharing without deleting files.
func TestAgentCredentialsShareTheNewestSignIn(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "agy", "token")
	saved := agentLogins
	t.Cleanup(func() { agentLogins = saved })
	agentLogins = []agentLogin{{harness: "agy", credential: func() string { return path }}}
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
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)

	write(`{"a":1}`, base)
	g.syncAgentCredentials(ctx)
	if got := shared(); string(got.Data) != `{"a":1}` || got.Modified != base.UnixMilli() {
		t.Fatalf("local sign-in not published: %+v", got)
	}

	// A machine without the file gets it, with the publisher's time so it is not published back.
	os.Remove(path)
	g.syncAgentCredentials(ctx)
	if info, err := os.Stat(path); read() != `{"a":1}` || err != nil || info.Mode().Perm() != 0o600 || !info.ModTime().Equal(base) {
		t.Fatalf("shared sign-in not installed: %q %v", read(), err)
	}

	// A peer's newer sign-in replaces this one; a newer local one replaces the peer's.
	if err := g.publishAgentCredential(ctx, "agy/token", sharedAgentCredential{Data: []byte(`{"b":2}`), Modified: base.Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	g.syncAgentCredentials(ctx)
	if read() != `{"b":2}` {
		t.Fatalf("newer peer sign-in not installed: %q", read())
	}
	write(`{"c":3}`, base.Add(2*time.Minute))
	g.syncAgentCredentials(ctx)
	if got := shared(); string(got.Data) != `{"c":3}` {
		t.Fatalf("newer local sign-in not published: %+v", got)
	}

	// A file the agent is still writing is left alone.
	write(`{"d":`, base.Add(-time.Hour))
	g.syncAgentCredentials(ctx)
	if read() != `{"d":` {
		t.Fatalf("half-written file was replaced: %q", read())
	}

	// After a sign-out, an older file elsewhere is neither republished nor removed.
	if err := g.stopSharingAgentCredential(ctx, agentLogins[0]); err != nil {
		t.Fatal(err)
	}
	write(`{"c":3}`, base.Add(2*time.Minute))
	g.syncAgentCredentials(ctx)
	if got := shared(); got.Data != nil || read() != `{"c":3}` {
		t.Fatalf("sign-out did not stop sharing: %+v %q", got, read())
	}
}
