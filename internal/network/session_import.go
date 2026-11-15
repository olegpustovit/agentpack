package network

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

type legacySessionFile struct {
	path string
	info os.FileInfo
	data []byte
}

func legacySessionDirectories(path string) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("legacy session source must be a directory, not a link or file")
	}
	return os.ReadDir(path)
}

func readLegacySessionFile(path string, limit int64) (*legacySessionFile, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("legacy session file is not regular or exceeds its byte limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.Join(err, errors.New("legacy session file changed while opening"))
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("legacy session file exceeds its byte limit")
	}
	return &legacySessionFile{path: path, info: info, data: data}, nil
}

func (file *legacySessionFile) unchanged() error {
	current, err := os.Lstat(file.path)
	if err != nil {
		return err
	}
	if !os.SameFile(file.info, current) || current.Size() != file.info.Size() || !current.ModTime().Equal(file.info.ModTime()) {
		return errors.New("legacy session file changed during import; left in place")
	}
	return nil
}

func (g *Gateway) reportSessionImport(ctx context.Context, path string, err error) {
	_, event := obs.Start(ctx, "state", "import_legacy_session")
	event.Set("source", filepath.Base(path))
	event.Fail(err)
	event.End()
	log.Printf("legacy session import left %s in place: %s", filepath.Base(path), obs.Redact(err.Error()))
}

func (g *Gateway) removeImportedFiles(ctx context.Context, files ...*legacySessionFile) {
	for _, file := range files {
		if file == nil {
			continue
		}
		err := file.unchanged()
		if err == nil {
			err = os.Remove(file.path)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			g.reportSessionImport(ctx, file.path, err)
		}
	}
}

func (g *Gateway) importLegacySessions(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	root := filepath.Join(g.options.Home, "network", "sessions")
	directories, err := legacySessionDirectories(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		g.reportSessionImport(ctx, root, err)
	}
	type stoppedWorker struct {
		file   *legacySessionFile
		record WorkerRecord
	}
	var stopped []stoppedWorker
	liveOwners := map[string]bool{}
	for _, directory := range directories {
		if ctx.Err() != nil {
			return
		}
		if !directory.IsDir() || !strings.HasPrefix(directory.Name(), "session-") {
			continue
		}
		path := filepath.Join(root, directory.Name(), "worker.json")
		file, err := readLegacySessionFile(path, 1<<20)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var record WorkerRecord
		if err == nil {
			record, err = parseWorkerRecord(file.data, directory.Name())
		}
		if err != nil {
			g.reportSessionImport(ctx, path, err)
			continue
		}
		alive := false
		if record.PID > 0 {
			alive, err = workerProcessAlive(record.PID)
		} else {
			_, probeErr := sessionState(ctx, record)
			alive = probeErr == nil
		}
		if err != nil {
			g.reportSessionImport(ctx, path, err)
			continue
		}
		if alive {
			if key, _, err := acpOwnerKey(record.Session.FolderID, record.Session.Harness, record.Session.AgentSessionID); err == nil {
				liveOwners[key] = true
			}
			continue
		}
		stopped = append(stopped, stoppedWorker{file, record})
	}
	g.importLegacyOwners(ctx, liveOwners)
	for _, worker := range stopped {
		if ctx.Err() != nil {
			return
		}
		if err := g.importStoppedWorker(ctx, worker.file, worker.record); err != nil {
			g.reportSessionImport(ctx, worker.file.path, err)
		}
	}
	if err := g.importLegacyRecent(ctx); err != nil && !errors.Is(err, os.ErrNotExist) {
		g.reportSessionImport(ctx, filepath.Join(g.options.Home, "network", "recent.json"), err)
	}
}

func (g *Gateway) importLegacyOwners(ctx context.Context, live map[string]bool) {
	directory := filepath.Join(g.options.Home, "network", "acp-session-owners")
	entries, err := legacySessionDirectories(directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		g.reportSessionImport(ctx, directory, err)
		if err := g.markOwnerImportPending(ctx, "pending"); err != nil {
			g.reportSessionImport(ctx, directory, err)
		}
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		key := strings.TrimSuffix(entry.Name(), ".json")
		path := filepath.Join(directory, entry.Name())
		decoded, err := hex.DecodeString(key)
		if err != nil || len(decoded) != sha256.Size {
			g.reportSessionImport(ctx, path, errors.New("invalid legacy ownership key"))
			continue
		}
		file, err := readLegacySessionFile(path, 1<<20)
		var record acpConversationOwnerRecord
		if err == nil {
			err = json.Unmarshal(file.data, &record)
		}
		if err == nil {
			var target string
			_, target, err = acpOwnerKey(record.FolderID, record.Harness, "legacy-key-validation")
			record.Harness = target
		}
		if err == nil {
			err = state.Transaction(ctx, g.store.DB, func(tx *sql.Tx) error {
				if err := file.unchanged(); err != nil {
					return err
				}
				return insertOwnerBinding(ctx, tx, strings.ToLower(key), record)
			})
		}
		if err != nil {
			g.reportSessionImport(ctx, path, err)
			if markerErr := g.markOwnerImportPending(ctx, strings.ToLower(key)); markerErr != nil {
				g.reportSessionImport(ctx, path, markerErr)
			}
			continue
		}
		// A live legacy worker keeps its support files until a later gateway start.
		if !live[key] {
			g.removeImportedFiles(ctx, file)
		}
	}
	if err := state.Transaction(ctx, g.store.DB, func(tx *sql.Tx) error {
		id, err := ownerBindingID(ctx, tx, "pending")
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE id=? AND node_id="+state.LocalNodeSQL, id)
		return err
	}); err != nil {
		g.reportSessionImport(ctx, directory, err)
	}
}

func (g *Gateway) markOwnerImportPending(ctx context.Context, key string) error {
	return state.Transaction(ctx, g.store.DB, func(tx *sql.Tx) error {
		id, err := ownerBindingID(ctx, tx, key)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO sessions(id,node_id,hidden,ended_at) VALUES (?,"+state.LocalNodeSQL+",1,?)", id, time.Now().UnixNano())
		return err
	})
}

func (g *Gateway) importStoppedWorker(ctx context.Context, workerFile *legacySessionFile, record WorkerRecord) error {
	info := record.Session
	info.ID = record.ID
	if info.Directory != "" {
		if !filepath.IsAbs(info.Directory) {
			return errors.New("legacy session directory must be absolute")
		}
		info.Directory = filepath.Clean(info.Directory)
	}
	if info.StartedAt.IsZero() {
		info.StartedAt = workerFile.info.ModTime().UTC()
	}
	if info.LastUsed.IsZero() {
		info.LastUsed = info.StartedAt
	}
	if info.State == (TerminalState{}) || info.State.Running {
		info.State.ExitCode = -1
	}
	info.State.Running, info.State.Restarting = false, false
	files := []*legacySessionFile{workerFile}
	for _, name := range []string{"name", "conversation.ndjson"} {
		limit := int64(maxConversationSnapshotBytes)
		if name == "name" {
			limit = 4096
		}
		file, err := readLegacySessionFile(filepath.Join(filepath.Dir(workerFile.path), name), limit)
		if errors.Is(err, os.ErrNotExist) {
			files = append(files, nil)
			continue
		}
		if err != nil {
			return err
		}
		files = append(files, file)
	}
	if files[1] != nil {
		var err error
		info.Name, err = normalizeSessionName(string(files[1].data))
		if err != nil {
			return err
		}
	}
	var entries []byte
	if files[2] != nil {
		entries = files[2].data
	}
	events, err := readConversationNDJSON(bytes.NewReader(entries))
	if err != nil {
		return err
	}
	err = state.Transaction(ctx, g.store.DB, func(tx *sql.Tx) error {
		if info.Transport == "acp" && info.AgentSessionID != "" && info.FolderID != "" {
			owner, found, err := loadACPConversationOwner(ctx, tx, info.FolderID, info.Harness, info.AgentSessionID)
			if err != nil {
				return err
			}
			if found {
				if info.StartedBy != nil && !info.StartedBy.canWriteSession(SessionInfo{StartedBy: &owner}) {
					return errACPResumeDenied
				}
				if info.StartedBy == nil {
					info.StartedBy = &owner
				}
			}
		}
		args, err := sessionValues(info)
		if err != nil {
			return err
		}
		args = append(args, time.Now().UnixNano())
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO sessions ("+sessionWriteColumns+",node_id,owner_email,ended_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,"+state.LocalNodeSQL+",?,?)", args...); err != nil {
			return err
		}
		var local bool
		if err := tx.QueryRowContext(ctx, "SELECT node_id="+state.LocalNodeSQL+" FROM sessions WHERE id=?", info.ID).Scan(&local); err != nil {
			return err
		}
		if !local {
			return errors.New("legacy session ID belongs to another node")
		}
		insert, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO conversation_events(session_id,seq,body) VALUES (?,?,?)")
		if err != nil {
			return err
		}
		defer insert.Close()
		var lastSeq int64
		lastEvent := sessionTimestamp(info.LastUsed)
		truncated := false
		for _, event := range events {
			if event.Seq <= 0 || event.Seq < lastSeq {
				return errors.New("invalid legacy conversation sequence")
			}
			truncated = truncated || event.Seq != lastSeq+1
			lastSeq = event.Seq
			if stamp := time.UnixMilli(event.TS).UnixNano(); stamp > lastEvent {
				lastEvent = stamp
			}
			body, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if _, err := insert.ExecContext(ctx, info.ID, event.Seq, string(body)); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET last_seq=max(last_seq,?),last_event_at=max(last_event_at,?),
			conversation_truncated=max(conversation_truncated,?),ended_at=CASE WHEN ended_at=0 THEN ? ELSE ended_at END WHERE id=?`,
			lastSeq, lastEvent, truncated, time.Now().UnixNano(), info.ID); err != nil {
			return err
		}
		for _, file := range files {
			if file != nil {
				if err := file.unchanged(); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil {
		g.removeImportedFiles(ctx, files...)
	}
	return err
}

func (g *Gateway) importLegacyRecent(ctx context.Context) error {
	file, err := readLegacySessionFile(filepath.Join(g.options.Home, "network", "recent.json"), 1<<20)
	if err != nil {
		return err
	}
	var recent []SessionInfo
	if err := json.Unmarshal(file.data, &recent); err != nil {
		return err
	}
	err = state.Transaction(ctx, g.store.DB, func(tx *sql.Tx) error {
		for _, info := range recent {
			if !filepath.IsAbs(info.Directory) {
				return errors.New("legacy recent directory must be absolute")
			}
			info.Directory = filepath.Clean(info.Directory)
			if info.LastUsed.IsZero() {
				info.LastUsed = file.info.ModTime().UTC()
			}
			key, err := json.Marshal(info)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(key)
			info.ID = fmt.Sprintf("session-legacy-recent-%x", digest)
			info.State = TerminalState{ExitCode: -1}
			args, err := sessionValues(info)
			if err != nil {
				return err
			}
			args = append(args, time.Now().UnixNano())
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO sessions ("+sessionWriteColumns+",node_id,owner_email,ended_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,"+state.LocalNodeSQL+",?,?)", args...); err != nil {
				return err
			}
		}
		return file.unchanged()
	})
	if err == nil {
		g.removeImportedFiles(ctx, file)
	}
	return err
}
