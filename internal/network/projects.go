package network

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// ProjectContents is one folder's pack.lock as a node reports it.
type ProjectContents struct {
	Packages []ProjectPackage `json:"packages"`
	MCP      []ProjectMCP     `json:"mcp"`
	Modes    []ProjectMode    `json:"modes"`
}

// ProjectMode names one mode and what it turns off; selectors themselves stay on the node.
type ProjectMode struct {
	Name            string   `json:"name"`
	OffPackages     []string `json:"off_packages"`
	PartialPackages []string `json:"partial_packages"`
	OffMCP          []string `json:"off_mcp"`
}

type ProjectPackage struct {
	Module      string `json:"module"`
	Commit      string `json:"commit"`
	ContentHash string `json:"content_hash"`
	CacheKey    string `json:"cache_key"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Direct      bool   `json:"direct"`
}

type ProjectMCP struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	Launcher  string `json:"launcher"`
	Status    string `json:"status"`
	Package   string `json:"package"`
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
	Host      string `json:"host"`
	Reason    string `json:"reason"`
	// URL is set for a remote server that a shared MCP login can serve (mcp.LoginURL).
	URL string `json:"url,omitempty"`
}

type ProjectLaunch struct {
	Harness  string    `json:"harness"`
	Mode     string    `json:"mode"`
	LastUsed time.Time `json:"last_used"`
}

// ProjectRow is one folder. Launches are unique per harness and mode, newest first.
type ProjectRow struct {
	Directory         string           `json:"directory"`
	Name              string           `json:"name"`
	FolderID          string           `json:"folder_id"`
	Available         bool             `json:"available"`
	UnavailableReason string           `json:"unavailable_reason"`
	LastUsed          time.Time        `json:"last_used"`
	Launches          []ProjectLaunch  `json:"launches"`
	Packages          []ProjectPackage `json:"packages"`
	MCP               []ProjectMCP     `json:"mcp"`
	Modes             []ProjectMode    `json:"modes"`
	Error             string           `json:"error"`
	// Git is the folder's checkout state; absent when it is not a repository or git is missing.
	Git *ProjectGit `json:"git,omitempty"`
}

type ProjectGit struct {
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head"`
	Dirty  int    `json:"dirty"`
}

// projectGit caches a folder’s checkout metadata for 30 seconds.
func projectGit(ctx context.Context, directory string, full bool) *ProjectGit {
	projectGitMu.Lock()
	cached, ok := projectGitCache[directory]
	projectGitMu.Unlock()
	if ok && time.Since(cached.at) < 30*time.Second && (cached.full || !full) {
		return cached.git
	}
	var git *ProjectGit
	if full {
		if status, err := readStatus(ctx, directory); err == nil && status.Head != "" {
			git = &ProjectGit{Branch: status.Branch, Head: status.Head, Dirty: status.Dirty}
		}
	} else {
		// Status can execute commands configured by a checkout. Browsing a folder
		// before it has been used only needs HEAD and the branch.
		branch, _ := gitLine(ctx, directory, "symbolic-ref", "--quiet", "--short", "HEAD")
		head, _ := gitLine(ctx, directory, "rev-parse", "--verify", "HEAD")
		if branch != "" || head != "" {
			git = &ProjectGit{Branch: branch, Head: head}
		}
	}
	if ctx.Err() != nil {
		return git
	}
	projectGitMu.Lock()
	if cached := projectGitCache[directory]; !full && cached.full && time.Since(cached.at) < 30*time.Second {
		projectGitMu.Unlock()
		return cached.git
	}
	projectGitCache[directory] = struct {
		at   time.Time
		git  *ProjectGit
		full bool
	}{time.Now(), git, full}
	projectGitMu.Unlock()
	return git
}

var (
	projectGitMu    sync.Mutex
	projectGitCache = map[string]struct {
		at   time.Time
		git  *ProjectGit
		full bool
	}{}
)

// withoutWorktrees drops agentpack's worktrees: they belong to their main folder's project.
func withoutWorktrees(home string, folders []RecentFolder) []RecentFolder {
	return slices.DeleteFunc(folders, func(folder RecentFolder) bool { return insideWorktrees(home, folder.Directory) })
}

const projectReadConcurrency = 4

// Projects groups recent launches by cleaned directory. Contents are read only for
// available folders, at most projectReadConcurrency at a time; a failed read sets
// Error on that row alone.
func Projects(ctx context.Context, home string, contents func(context.Context, string) (ProjectContents, error)) ([]ProjectRow, error) {
	folders, err := RecentFolders(home)
	if err != nil {
		return nil, err
	}
	return projectRows(ctx, withoutWorktrees(home, folders), contents)
}

func (g *Gateway) projects(ctx context.Context) ([]ProjectRow, error) {
	recent, err := recentIn(ctx, g.auth.store.Reader)
	if err != nil {
		return nil, err
	}
	rows, err := projectRows(ctx, withoutWorktrees(g.options.Home, recentFolders(recent)), g.options.Packages)
	if err != nil || !gitAvailable() {
		return rows, err
	}
	slots := make(chan struct{}, projectReadConcurrency)
	var wg sync.WaitGroup
	for i := range rows {
		if !rows[i].Available {
			continue
		}
		wg.Add(1)
		go func(row *ProjectRow) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			row.Git = projectGit(ctx, row.Directory, true)
		}(&rows[i])
	}
	wg.Wait()
	return rows, nil
}

func projectRows(ctx context.Context, folders []RecentFolder, contents func(context.Context, string) (ProjectContents, error)) ([]ProjectRow, error) {
	slices.SortStableFunc(folders, func(a, b RecentFolder) int { return b.LastUsed.Compare(a.LastUsed) })
	rows := []ProjectRow{}
	index := map[string]int{}
	for _, folder := range folders {
		directory := filepath.Clean(folder.Directory)
		i, ok := index[directory]
		if !ok {
			i = len(rows)
			index[directory] = i
			// The newest launch describes the folder's current identity and availability.
			rows = append(rows, ProjectRow{
				Directory:         directory,
				Name:              filepath.Base(directory),
				FolderID:          folder.FolderID,
				Available:         folder.Available,
				UnavailableReason: folder.UnavailableReason,
				LastUsed:          folder.LastUsed,
				Launches:          []ProjectLaunch{},
				Packages:          []ProjectPackage{},
				MCP:               []ProjectMCP{},
				Modes:             []ProjectMode{},
			})
		}
		row := &rows[i]
		if folder.Harness != "" && !slices.ContainsFunc(row.Launches, func(launch ProjectLaunch) bool {
			return launch.Harness == folder.Harness && launch.Mode == folder.Mode
		}) {
			row.Launches = append(row.Launches, ProjectLaunch{Harness: folder.Harness, Mode: folder.Mode, LastUsed: folder.LastUsed})
		}
	}

	slots := make(chan struct{}, projectReadConcurrency)
	var wg sync.WaitGroup
	for i := range rows {
		if !rows[i].Available || contents == nil {
			continue
		}
		wg.Add(1)
		go func(row *ProjectRow) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			read, err := contents(ctx, row.Directory)
			if err != nil {
				row.Error = err.Error()
				return
			}
			row.Packages = append([]ProjectPackage{}, read.Packages...)
			row.MCP = append([]ProjectMCP{}, read.MCP...)
			row.Modes = append([]ProjectMode{}, read.Modes...)
		}(&rows[i])
	}
	wg.Wait()

	slices.SortFunc(rows, func(a, b ProjectRow) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Directory, b.Directory)
	})
	return rows, nil
}
