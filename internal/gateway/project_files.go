package gateway

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
)

type projectFiles struct {
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated"`
}

func validTrackedPath(path string) bool {
	return path != "." && fs.ValidPath(path) && !strings.ContainsRune(path, '\\') && !strings.ContainsFunc(path, unicode.IsControl)
}

func (g *Gateway) serveProjectFiles(w http.ResponseWriter, r *http.Request) {
	directory := r.URL.Query().Get("directory")
	if !filepath.IsAbs(directory) || strings.ContainsRune(directory, 0) ||
		slices.Contains(strings.Split(filepath.ToSlash(directory), "/"), "..") {
		badRequest(w, errFolderPath.Error())
		return
	}
	directory = filepath.Clean(directory)
	actor, _ := session.ActorFrom(r.Context())
	readFolders(w, r, folderReadTimeout, folderReadSlots, func(ctx context.Context) (projectFiles, error) {
		if !g.startableDirectory(ctx, actor, directory) {
			return projectFiles{}, errUnknownFolder
		}
		// shortcut: only the first 2,000 paths are searchable, add server-side filtering for larger repositories.
		return listTrackedProjectFiles(ctx, directory, 512<<10, 2000)
	})
}

// Git's index is enough to suggest paths without opening files or scanning the working tree.
func listTrackedProjectFiles(ctx context.Context, directory string, byteLimit, pathLimit int) (projectFiles, error) {
	out := projectFiles{Files: []string{}}
	exe, err := paths.LookPathForBackground("git")
	if err != nil {
		return out, nil
	}
	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(cmdCtx, exe, "-C", directory, "-c", "core.fsmonitor=false", "ls-files", "-z", "--cached", "--", ".")
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_PREFIX":
			return true
		}
		return false
	})
	command.Env = append(env, "GIT_TERMINAL_PROMPT=0")
	command.WaitDelay = 500 * time.Millisecond
	stdout, err := command.StdoutPipe()
	if err != nil {
		return out, err
	}
	if err := command.Start(); err != nil {
		return out, err
	}

	data, readErr := io.ReadAll(io.LimitReader(stdout, int64(byteLimit)+1))
	out.Truncated = len(data) > byteLimit
	if readErr != nil || out.Truncated {
		cancel()
		_ = stdout.Close()
	}
	waitErr := command.Wait()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if readErr != nil {
		return out, readErr
	}
	if out.Truncated {
		data = data[:bytes.LastIndexByte(data[:byteLimit], 0)+1]
	} else if waitErr != nil {
		return out, nil // the selected directory may be outside a Git repository
	}
	for name := range strings.SplitSeq(string(data), "\x00") {
		if !validTrackedPath(name) || len(out.Files) > 0 && out.Files[len(out.Files)-1] == name {
			continue
		}
		if len(out.Files) == pathLimit {
			out.Truncated = true
			break
		}
		out.Files = append(out.Files, name)
	}
	return out, nil
}
