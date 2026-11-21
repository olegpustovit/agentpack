package codex

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"github.com/olegpustovit/agentpack/internal/obs"
)

// repairOnLoad lets a session/load or session/resume through after pointing Codex's thread index
// at the native rollout file, so a chat whose staged home was removed still resumes.
func repairOnLoad(home string) func([]byte) []byte {
	return func(line []byte) []byte {
		if !bytes.Contains(line, []byte(`"session/load"`)) && !bytes.Contains(line, []byte(`"session/resume"`)) {
			return line
		}
		var request struct {
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &request) != nil || request.Params.SessionID == "" ||
			request.Method != "session/load" && request.Method != "session/resume" {
			return line
		}
		native, ok := nativeHome()
		if !ok {
			return line
		}
		if err := repairRollout(sqliteHome(home, native), native, request.Params.SessionID); err != nil {
			_, event := obs.Start(context.Background(), "worker", "codex_rollout_repair")
			event.Fail(err)
			event.End()
		}
		return line
	}
}

// sqliteHome is where the staged Codex keeps its thread index: the staged config's sqlite_home.
func sqliteHome(home, native string) string {
	root := make(map[string]any)
	if data, err := os.ReadFile(filepath.Join(home, "config.toml")); err == nil && jsonOrToml(data, &root) == nil {
		if value, ok := root["sqlite_home"].(string); ok && value != "" {
			return value
		}
	}
	return native
}

// repairRollout rewrites a thread's recorded rollout path when it is gone but the same file exists
// under native sessions. Codex records the path through the staged CODEX_HOME, whose generation is
// later removed, and its thread/resume does not search sessions/ for a missing path.
func repairRollout(sqliteHome, native, threadID string) error {
	indexes, err := filepath.Glob(filepath.Join(sqliteHome, "state_*.sqlite"))
	if err != nil {
		return err
	}
	var errs []error
	for _, index := range indexes {
		errs = append(errs, repairRolloutIn(index, native, threadID))
	}
	return errors.Join(errs...)
}

func repairRolloutIn(index, native, threadID string) error {
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(index))+"?agentpack_plain=1&mode=rw&_busy_timeout=5000")
	if err != nil {
		return err
	}
	defer db.Close()
	var recorded string
	err = db.QueryRow("SELECT rollout_path FROM threads WHERE id=?", threadID).Scan(&recorded)
	if errors.Is(err, sql.ErrNoRows) || err == nil && recorded == "" {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(recorded); err == nil {
		return nil
	}
	separator := string(filepath.Separator)
	for _, name := range durableDirectories {
		marker := separator + name + separator
		at := strings.LastIndex(recorded, marker)
		if at < 0 {
			continue
		}
		candidate := filepath.Join(native, name, recorded[at+len(marker):])
		if info, err := os.Stat(candidate); err != nil || !info.Mode().IsRegular() {
			return nil
		}
		_, err := db.Exec("UPDATE threads SET rollout_path=? WHERE id=? AND rollout_path=?", candidate, threadID, recorded)
		return err
	}
	return nil
}
