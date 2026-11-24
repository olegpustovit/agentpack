package gateway

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

type workerEndpointFile struct {
	path string
	info os.FileInfo
	data []byte
}

func workerEndpointDirectories(path string) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("worker endpoint source must be a directory, not a link or file")
	}
	return os.ReadDir(path)
}

func readWorkerEndpointFile(path string, limit int64) (*workerEndpointFile, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("worker endpoint file is not regular or exceeds its byte limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.Join(err, errors.New("worker endpoint file changed while opening"))
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("worker endpoint file exceeds its byte limit")
	}
	return &workerEndpointFile{path: path, info: info, data: data}, nil
}

func (file *workerEndpointFile) unchanged() error {
	current, err := os.Lstat(file.path)
	if err != nil {
		return err
	}
	if !os.SameFile(file.info, current) || current.Size() != file.info.Size() || !current.ModTime().Equal(file.info.ModTime()) {
		return errors.New("worker endpoint file changed during recovery; left in place")
	}
	return nil
}

func (g *Gateway) reportEndpointRecovery(ctx context.Context, path string, err error) {
	_, event := obs.Start(ctx, "state", "reap_worker_endpoint")
	event.Set("source", filepath.Base(path))
	event.Fail(err)
	event.End()
	log.Printf("worker endpoint recovery left %s in place: %s", filepath.Base(path), obs.Redact(err.Error()))
}

func (g *Gateway) removeDeadEndpoints(ctx context.Context, files ...*workerEndpointFile) {
	for _, file := range files {
		if file == nil {
			continue
		}
		err := file.unchanged()
		if err == nil {
			err = os.Remove(file.path)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			g.reportEndpointRecovery(ctx, file.path, err)
		}
	}
}

func (g *Gateway) reapWorkerEndpoints(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	root := filepath.Join(g.options.Home, "network", "sessions")
	directories, err := workerEndpointDirectories(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		g.reportEndpointRecovery(ctx, root, err)
	}
	for _, directory := range directories {
		if ctx.Err() != nil {
			return
		}
		if !directory.IsDir() || !strings.HasPrefix(directory.Name(), "session-") {
			continue
		}
		path := filepath.Join(root, directory.Name(), "worker.json")
		file, err := readWorkerEndpointFile(path, 1<<20)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var record network.WorkerRecord
		if err == nil {
			record, err = network.ParseWorkerRecord(file.data, directory.Name())
		}
		if err != nil {
			g.reportEndpointRecovery(ctx, path, err)
			continue
		}
		possiblyAlive := true
		if record.PID > 0 {
			possiblyAlive, err = workerProcessAlive(record.PID)
		}
		if err != nil {
			g.reportEndpointRecovery(ctx, path, err)
			continue
		}
		if possiblyAlive {
			// A PID can belong to another process (reused after a reboot), and a new endpoint can
			// precede its server. An endpoint that answered once is its own worker until it changes.
			if verified, ok := g.verifiedEndpoints.Load(path); ok && verified == file.info.ModTime() {
				continue
			}
			_, probeErr := network.SessionState(ctx, record)
			if probeErr == nil {
				g.verifiedEndpoints.Store(path, file.info.ModTime())
			}
			// A slow answer is a busy worker, not a dead one: only a refusal or a wrong answer settles it.
			var netErr net.Error
			if probeErr == nil || ctx.Err() != nil || errors.As(probeErr, &netErr) && netErr.Timeout() ||
				record.PID > 0 && time.Since(file.info.ModTime()) < interruptedStartGrace {
				continue
			}
		}
		if err := g.settleDeadWorker(ctx, file, record.ID); err != nil {
			g.reportEndpointRecovery(ctx, path, err)
		}
	}
}

// Current endpoints have no session metadata. The existing row owns their history and state.
func (g *Gateway) settleDeadWorker(ctx context.Context, file *workerEndpointFile, id string) error {
	if err := file.unchanged(); err != nil {
		return err
	}
	_, err := g.store.DB.ExecContext(ctx, "UPDATE sessions SET ended_at=? WHERE id=? AND ended_at=0 AND node_id="+state.LocalNodeSQL, time.Now().UnixNano(), id)
	if err == nil {
		g.verifiedEndpoints.Delete(file.path)
		g.removeDeadEndpoints(ctx, file)
	}
	return err
}

func (g *Gateway) reapUnpublishedWorker(ctx context.Context, info session.SessionInfo, now time.Time) error {
	if !network.ValidSessionID(info.ID) {
		return nil
	}
	_, err := os.Lstat(filepath.Join(g.options.Home, "network", "sessions", info.ID, "worker.json"))
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A newer launch can register the row between the query and the endpoint check.
	_, err = g.store.DB.ExecContext(ctx, `UPDATE sessions SET ended_at=? WHERE id=? AND ended_at=0 AND archived_at=0
 AND node_id=`+state.LocalNodeSQL+` AND transport<>'acp' AND last_used=?`, now.UnixNano(), info.ID, session.SessionTimestamp(info.LastUsed))
	return err
}
