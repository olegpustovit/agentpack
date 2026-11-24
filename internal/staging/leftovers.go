package staging

import (
	"os"
	"path/filepath"
	"strings"

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
// longer exists, is removed. A generation leased by a running session is left
// for a later run. Failures are ignored.
func RemoveOrphanedProjectDirectories(projectRoot string) {
	// The system temporary directory holds each project's staging
	// (agentpack-<hash>) when no staging root is configured.
	for _, orphan := range paths.OrphanedProjectDirectories(os.TempDir()) {
		if strings.HasPrefix(filepath.Base(orphan), "agentpack-") && !paths.GenerationLeasesInUse(orphan) {
			_ = os.RemoveAll(orphan)
		}
	}
	homes, err := codex.ProjectHomes(projectRoot)
	if err != nil {
		return
	}
	for _, orphan := range paths.OrphanedProjectDirectories(filepath.Dir(homes)) {
		if paths.GenerationLeasesInUse(orphan) {
			continue
		}
		_ = os.RemoveAll(orphan)
	}
}
