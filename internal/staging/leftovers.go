package staging

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
	"github.com/olegpustovit/agentpack/internal/harness/codex"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// MarkProjectDirectories records the project's path in the directories its
// staging lives in outside $AGENTPACK_HOME: the staging tree and the Codex
// homes. Every rebuild does this, so that the directories can be recognised
// as left over once the project is deleted.
func MarkProjectDirectories(projectRoot string) {
	if staging, _, err := paths.StagingProjectDir(projectRoot); err == nil {
		_ = paths.MarkProjectDirectory(staging, projectRoot)
	}
	if homes, err := codex.ProjectHomes(projectRoot); err == nil {
		_ = paths.MarkProjectDirectory(homes, projectRoot)
	}
}

// RemoveOrphanedProjectDirectories removes what deleted projects left behind:
// /tmp/agentpack-<hash> staging trees and the Codex homes under /var/tmp/ap-c.
// Only a directory that agentpack marked, and whose project directory no
// longer exists, is removed; a Codex home that a running session still holds
// is left for a later run. Failures are ignored: tidying never fails a sync.
func RemoveOrphanedProjectDirectories(projectRoot string) {
	// The system temporary directory holds each project's staging
	// (agentpack-<hash>) when no staging root is configured.
	for _, orphan := range paths.OrphanedProjectDirectories(os.TempDir()) {
		if strings.HasPrefix(filepath.Base(orphan), "agentpack-") {
			_ = os.RemoveAll(orphan)
		}
	}
	homes, err := codex.ProjectHomes(projectRoot)
	if err != nil {
		return
	}
	for _, orphan := range paths.OrphanedProjectDirectories(filepath.Dir(homes)) {
		if codexHomesInUse(orphan) {
			continue
		}
		_ = os.RemoveAll(orphan)
	}
}

// codexHomesInUse reports whether a launch still holds a lease on one of the
// generations below a project's Codex homes directory.
func codexHomesInUse(directory string) bool {
	leases, _ := filepath.Glob(filepath.Join(directory, "*", "*.lease"))
	for _, path := range leases {
		lease := flock.New(path)
		free, err := lease.TryLock()
		if err != nil || !free {
			return true
		}
		_ = lease.Unlock()
	}
	return false
}
