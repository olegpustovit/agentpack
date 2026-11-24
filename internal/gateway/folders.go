package gateway

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/olegpustovit/agentpack/internal/environment"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
)

const folderReadTimeout = 2 * time.Second

var errFolderPath = errors.New("folder path must be absolute")

type folderEntry struct {
	Name   string `json:"name"`
	Git    string `json:"git,omitempty"`
	Pack   bool   `json:"pack"`
	Hidden bool   `json:"hidden"`
}

type folderListing struct {
	Path      string        `json:"path"`
	Parent    string        `json:"parent"`
	Row       *ProjectRow   `json:"row"`
	Entries   []folderEntry `json:"entries"`
	Truncated bool          `json:"truncated"`
}

type folderDiscovery struct {
	mu   sync.Mutex
	at   time.Time
	rows []ProjectRow
}

// The OS cannot cancel a directory read on a slow mount. Keep those reads off the
// handler and bound the number that can outlive their requests.
var folderReadSlots = make(chan struct{}, 4)
var folderScanSlots = make(chan struct{}, 1)

func readFolders[T any](w http.ResponseWriter, r *http.Request, timeout time.Duration, slots chan struct{}, read func(context.Context) (T, error)) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	select {
	case slots <- struct{}{}:
		obs.Go("gateway.folders", func() {
			defer func() { <-slots }()
			value, err := read(ctx)
			done <- result{value, err}
		})
	case <-ctx.Done():
		folderFailure(w, r, ctx.Err())
		return
	}
	select {
	case out := <-done:
		if out.err != nil {
			folderFailure(w, r, out.err)
		} else {
			network.WriteJSON(w, out.value)
		}
	case <-ctx.Done():
		folderFailure(w, r, ctx.Err())
	}
}

func folderFailure(w http.ResponseWriter, r *http.Request, err error) {
	status, message := http.StatusInternalServerError, "Couldn't read folders."
	switch {
	case errors.Is(err, errFolderPath):
		status, message = http.StatusBadRequest, errFolderPath.Error()
	case errors.Is(err, fs.ErrNotExist):
		status, message = http.StatusNotFound, "Folder not found."
	case errors.Is(err, fs.ErrPermission):
		status, message = http.StatusForbidden, "Folder is inaccessible."
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status, message = http.StatusGatewayTimeout, "Folder read timed out."
	}
	folderProblem(w, r, status, message)
}

func folderProblem(w http.ResponseWriter, r *http.Request, status int, message string) {
	obs.From(r.Context()).Fail(errors.New(message))
	http.Error(w, message, status)
}

func (g *Gateway) serveFolders(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	readFolders(w, r, folderReadTimeout, folderReadSlots, func(ctx context.Context) (folderListing, error) {
		return g.listFolders(ctx, path)
	})
}

func serveCreateFolder(w http.ResponseWriter, r *http.Request) {
	actor, _ := session.ActorFrom(r.Context())
	if !actor.CanStartNewFolder() {
		folderProblem(w, r, http.StatusForbidden, "A browser account is required to create folders.")
		return
	}
	var input struct {
		Parent string `json:"parent"`
		Name   string `json:"name"`
	}
	if !network.ReadJSON(w, r, &input) {
		return
	}
	if !filepath.IsAbs(input.Parent) || strings.ContainsRune(input.Parent, 0) {
		folderProblem(w, r, http.StatusBadRequest, errFolderPath.Error())
		return
	}
	name := input.Name
	invalidWindowsName := runtime.GOOS == "windows" && (strings.TrimRight(name, ". ") != name || strings.ContainsAny(name, `<>"|?*`))
	if strings.TrimSpace(name) == "" || name == "." || !filepath.IsLocal(name) || strings.ContainsAny(name, "/\\\x00") || invalidWindowsName {
		folderProblem(w, r, http.StatusBadRequest, "Enter a single folder name.")
		return
	}
	path := filepath.Join(input.Parent, name)
	err := os.Mkdir(path, 0o700)
	switch {
	case err == nil:
		network.WriteJSON(w, struct {
			Path string `json:"path"`
		}{path})
	case errors.Is(err, fs.ErrExist):
		folderProblem(w, r, http.StatusConflict, "That name is already in use.")
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		folderProblem(w, r, http.StatusNotFound, "Parent folder not found.")
	case errors.Is(err, fs.ErrPermission):
		folderProblem(w, r, http.StatusForbidden, "Parent folder is inaccessible.")
	default:
		folderProblem(w, r, http.StatusInternalServerError, "Couldn't create the folder.")
	}
}

func (g *Gateway) listFolders(ctx context.Context, path string) (folderListing, error) {
	out := folderListing{Entries: []folderEntry{}}
	if path == "" && runtime.GOOS == "windows" {
		err := listFolderDrives(&out)
		return out, err
	}
	if path == "~" || strings.HasPrefix(path, "~/") || runtime.GOOS == "windows" && strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return out, err
		}
		path = filepath.Join(home, path[1:])
	}
	if !filepath.IsAbs(path) {
		return out, errFolderPath
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return out, err
	}
	if !info.IsDir() {
		return out, fs.ErrNotExist
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return out, err
	}
	out.Path = path
	if parent := filepath.Dir(path); parent != path {
		out.Parent = parent
	}
	folder, err := browsableFolder(path)
	if err != nil {
		return out, err
	}
	recent, err := network.RecentIn(ctx, g.store.Reader)
	if err != nil {
		return out, err
	}
	folders := []network.RecentFolder{}
	for _, entry := range recent {
		if filepath.Clean(entry.Directory) == path && network.SameFolder(entry.FolderID, folder.FolderID) {
			folders = append(folders, network.RecentFolder{SessionInfo: entry, Available: true})
		}
	}
	if len(folders) == 0 {
		folders = append(folders, folder)
	}
	rows := g.browsableRows(ctx, folders, true)
	out.Row = &rows[0]
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		child := filepath.Join(path, entry.Name())
		deferred := paths.RequiresUserAccess(child)
		if !entry.IsDir() && !deferred {
			if target, err := os.Stat(child); err != nil || !target.IsDir() {
				continue
			}
		}
		if len(out.Entries) == 1000 {
			out.Truncated = true
			break
		}
		entry := folderEntry{Name: entry.Name(), Hidden: strings.HasPrefix(entry.Name(), ".")}
		if !deferred {
			entry.Pack = backgroundFolderMarker(child, paths.ManifestName)
			if backgroundFolderMarker(child, ".git") && !backgroundGitDeferred(child) && gitAvailable() {
				if git := projectGit(ctx, child, false); git != nil {
					entry.Git = git.Branch
				}
			}
		}
		out.Entries = append(out.Entries, entry)
	}
	return out, nil
}

func folderMarker(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func backgroundFolderMarker(dir, name string) bool {
	return !paths.RequiresUserAccess(filepath.Join(dir, name)) && folderMarker(dir, name)
}

func browsableFolder(dir string) (network.RecentFolder, error) {
	identity, err := network.FolderIdentity(dir)
	return network.RecentFolder{SessionInfo: session.SessionInfo{Directory: dir, FolderID: identity}, Available: err == nil}, err
}

func (g *Gateway) browsableRows(ctx context.Context, folders []network.RecentFolder, explicit bool) []ProjectRow {
	for i := range folders {
		dir := folders[i].Directory
		// Browsing a protected folder is explicit. Its parent's listing only sees its name.
		if !explicit || !paths.RequiresUserAccess(dir) {
			folders[i].DetailsDeferred = environment.ProjectRequiresUserAccess(dir, "") || backgroundGitDeferred(dir)
		}
	}
	rows, _ := projectRows(ctx, folders, g.options.Packages)
	for i := range rows {
		row := &rows[i]
		if row.Error != "" {
			obs.From(ctx).Fail(errors.New("couldn't read folder package contents"))
			row.Error = "Folder package contents are unavailable."
		}
		if !row.DetailsDeferred && folderMarker(row.Directory, ".git") && gitAvailable() {
			row.Git = projectGit(ctx, row.Directory, false)
		}
	}
	return rows
}

func (g *Gateway) serveFoundFolders(w http.ResponseWriter, r *http.Request) {
	readFolders(w, r, 30*time.Second, folderScanSlots, g.foundFolders)
}

func (g *Gateway) foundFolders(ctx context.Context) ([]ProjectRow, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	recent, err := network.RecentIn(ctx, g.store.Reader)
	if err != nil {
		return nil, err
	}
	g.found.mu.Lock()
	defer g.found.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if time.Since(g.found.at) >= 10*time.Minute {
		rows := g.browsableRows(ctx, discoverFolders(ctx, home, g.options.Home), false)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		g.found.at, g.found.rows = time.Now(), rows
	}
	used := make(map[string]bool, len(recent))
	for _, entry := range recent {
		used[filepath.Clean(entry.Directory)] = true
	}
	rows := slices.Clone(g.found.rows)
	return slices.DeleteFunc(rows, func(row ProjectRow) bool { return used[row.Directory] }), nil
}

func discoverFolders(ctx context.Context, home, dataHome string) []network.RecentFolder {
	const budget = 5000
	found := []network.RecentFolder{}
	visited := map[string]int{}
	var scan func(string, int)
	scan = func(dir string, remaining int) {
		if len(visited) >= budget || ctx.Err() != nil || insideWorktrees(dataHome, dir) || paths.RequiresUserAccess(dir) {
			return
		}
		prev, seen := visited[dir]
		if seen && prev >= remaining {
			return
		}
		visited[dir] = remaining
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			return
		}
		repo := backgroundFolderMarker(dir, ".git")
		if repo || backgroundFolderMarker(dir, paths.ManifestName) {
			if !seen {
				if folder, err := browsableFolder(dir); err == nil {
					found = append(found, folder)
				}
			}
		}
		if repo || remaining == 0 {
			return
		}
		file, err := os.Open(dir)
		if err != nil {
			return
		}
		defer file.Close()
		for len(visited) < budget && ctx.Err() == nil {
			children, err := file.ReadDir(128)
			for _, child := range children {
				name := child.Name()
				if !child.IsDir() || strings.HasPrefix(name, ".") {
					continue
				}
				switch name {
				case "node_modules", "vendor", "Library", "AppData":
					continue
				}
				scan(filepath.Join(dir, name), remaining-1)
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					obs.From(ctx).Fail(errors.New("couldn't scan folder entries"))
				}
				break
			}
		}
	}
	if paths.RequiresUserAccess(home) {
		return found
	}
	if !backgroundFolderMarker(home, ".git") {
		for _, root := range []string{"projects", "src", "code", "dev", "work", "repos", "git", "Developer"} {
			scan(filepath.Join(home, root), 3)
		}
		goHome := filepath.Join(home, "go")
		if !paths.RequiresUserAccess(goHome) {
			if info, err := os.Lstat(goHome); err == nil && info.IsDir() && !backgroundFolderMarker(goHome, ".git") {
				scan(filepath.Join(goHome, "src"), 3)
			}
		}
	}
	scan(home, 3)
	return found
}

func (g *Gateway) startableDirectory(ctx context.Context, actor session.Actor, dir string) bool {
	if g.knownDirectory(ctx, dir) {
		return true
	}
	if !actor.CanStartNewFolder() || !filepath.IsAbs(dir) {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}
