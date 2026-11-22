package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// A terminal Codex run by a worker reports finished turns through notify, chaining a notify the user set.
func TestSessionNotifyChainsTheUsersNotify(t *testing.T) {
	decode := func(t *testing.T, override string) []string {
		t.Helper()
		var parsed struct {
			Notify []string `toml:"notify"`
		}
		if err := toml.Unmarshal([]byte(override), &parsed); err != nil {
			t.Fatalf("override %q is not TOML: %v", override, err)
		}
		return parsed.Notify
	}
	report := []string{"/bin/ap", "hook-exec", "session-state", "--target", "codex", "--session", "s-1", "--state", "idle"}
	for _, c := range []struct {
		name, config string
		want         []string
	}{
		{"no config", "", report},
		{"user notify", `notify = ["notify-send", "Codex \"done\""]` + "\n", append(append(append([]string(nil), report...), "--then"), "notify-send", `Codex "done"`)},
		{"notify Codex would reject", `notify = ["a", 1]` + "\n", report},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			if c.config != "" {
				if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(c.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			override, err := sessionNotify(home, "/bin/ap", "s-1")
			if err != nil {
				t.Fatal(err)
			}
			got := decode(t, override)
			if len(got) != len(c.want) {
				t.Fatalf("notify = %q, want %q", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("notify = %q, want %q", got, c.want)
				}
			}
		})
	}
}
