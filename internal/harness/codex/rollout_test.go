package codex

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// A thread recorded under a removed staged home resumes from the native rollout file.
func TestRepairOnLoadPointsThreadAtNativeRollout(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("USERPROFILE", user)
	native := filepath.Join(user, ".codex")
	rollout := filepath.Join(native, "sessions", "2026", "11", "21", "rollout-thread.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollout, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(native, "state_5.sqlite")
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(index))+"?agentpack_plain=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stale := filepath.Join(t.TempDir(), "gone", "g-1", "sessions", "2026", "11", "21", "rollout-thread.jsonl")
	if _, err := db.Exec("CREATE TABLE threads(id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL); INSERT INTO threads VALUES ('thread', ?), ('other', ?)", stale, stale); err != nil {
		t.Fatal(err)
	}
	line := []byte(`{"jsonrpc":"2.0","id":3,"method":"session/load","params":{"sessionId":"thread","cwd":"/w"}}`)
	if got := repairOnLoad(t.TempDir())(line); string(got) != string(line) {
		t.Fatalf("request changed: %s", got)
	}
	var path, other string
	if err := db.QueryRow("SELECT (SELECT rollout_path FROM threads WHERE id='thread'), (SELECT rollout_path FROM threads WHERE id='other')").Scan(&path, &other); err != nil {
		t.Fatal(err)
	}
	if path != rollout || other != stale {
		t.Fatalf("thread=%q other=%q", path, other)
	}
}
