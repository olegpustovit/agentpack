package network

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An agentpack update must never look like a package change: a digest from another binary reads as absent,
// so the caller recomputes it with the running code instead of restarting every agent.
func TestSessionEnvironmentIgnoresDigestsFromAnotherBinary(t *testing.T) {
	home := t.TempDir()
	id := "session-TEST"
	if err := os.MkdirAll(filepath.Join(home, "network", "sessions", id), 0700); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("ab", 32)
	if err := RecordSessionEnvironment(home, id, digest); err != nil {
		t.Fatal(err)
	}
	if got, err := SessionEnvironment(home, id); err != nil || got != digest {
		t.Fatalf("same binary: got %q, %v", got, err)
	}
	path := filepath.Join(home, "network", "sessions", id, "environment")
	for name, content := range map[string]string{"older agentpack": digest, "other binary": digest + "\n/old/ap:1:2"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := SessionEnvironment(home, id); err != nil || got != "" {
			t.Fatalf("%s: got %q, %v; want absent", name, got, err)
		}
	}
}
