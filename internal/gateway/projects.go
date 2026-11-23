package gateway

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/registry"
)

type ProjectLaunch struct {
	Harness  string    `json:"harness"`
	Mode     string    `json:"mode"`
	LastUsed time.Time `json:"last_used"`
}

// ProjectRow is one folder. Launches are unique per harness and mode, newest first.
type ProjectRow struct {
	Directory         string                   `json:"directory"`
	Name              string                   `json:"name"`
	FolderID          string                   `json:"folder_id"`
	Available         bool                     `json:"available"`
	UnavailableReason string                   `json:"unavailable_reason"`
	LastUsed          time.Time                `json:"last_used"`
	Launches          []ProjectLaunch          `json:"launches"`
	Packages          []network.ProjectPackage `json:"packages"`
	MCP               []network.ProjectMCP     `json:"mcp"`
	Modes             []network.ProjectMode    `json:"modes"`
	Error             string                   `json:"error"`
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
func withoutWorktrees(home string, folders []network.RecentFolder) []network.RecentFolder {
	return slices.DeleteFunc(folders, func(folder network.RecentFolder) bool { return insideWorktrees(home, folder.Directory) })
}

const projectReadConcurrency = 4

func (g *Gateway) projects(ctx context.Context) ([]ProjectRow, error) {
	recent, err := network.RecentIn(ctx, g.store.Reader)
	if err != nil {
		return nil, err
	}
	rows, err := projectRows(ctx, withoutWorktrees(g.options.Home, network.RecentFolders(recent)), g.options.Packages)
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

func projectRows(ctx context.Context, folders []network.RecentFolder, contents func(context.Context, string) (network.ProjectContents, error)) ([]ProjectRow, error) {
	slices.SortStableFunc(folders, func(a, b network.RecentFolder) int { return b.LastUsed.Compare(a.LastUsed) })
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
				Packages:          []network.ProjectPackage{},
				MCP:               []network.ProjectMCP{},
				Modes:             []network.ProjectMode{},
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
			row.Packages = append([]network.ProjectPackage{}, read.Packages...)
			row.MCP = append([]network.ProjectMCP{}, read.MCP...)
			row.Modes = append([]network.ProjectMode{}, read.Modes...)
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

type ProjectSettingsInput struct {
	Directory    string                     `json:"directory"`
	Add          []string                   `json:"add"`
	Remove       []string                   `json:"remove"`
	Servers      []registry.MCPServerInput  `json:"servers"`
	RemoveMCP    []string                   `json:"remove_mcp"`
	Logins       map[string]string          `json:"logins"`
	PluginLogins map[string]mcp.PluginLogin `json:"plugin_logins"`
}

var ErrProjectPluginServerChanged = errors.New("the plugin's MCP server changed; reopen Project Settings and choose its login again")

func (g *Gateway) saveProjectSettings(w http.ResponseWriter, r *http.Request) {
	var input ProjectSettingsInput
	if !network.ReadJSON(w, r, &input) {
		return
	}
	if !g.knownDirectory(r.Context(), input.Directory) {
		badRequest(w, "choose an available project folder")
		return
	}
	for _, server := range input.Servers {
		if strings.TrimSpace(server.Name) == "" || (strings.TrimSpace(server.Command) == "") == (strings.TrimSpace(server.URL) == "") {
			badRequest(w, "each MCP server needs a name and either a command or a URL")
			return
		}
	}
	if len(input.Logins)+len(input.PluginLogins) > 0 {
		_, account, ok := credentialAccount(r)
		if !ok {
			http.Error(w, "an enabled account is required for MCP credentials", http.StatusForbidden)
			return
		}
		logins := make(map[string]string, len(input.Logins))
		for rawURL, id := range input.Logins {
			url, ok := g.validateProjectLogin(w, r, account, rawURL, id)
			if !ok {
				return
			}
			if old, exists := logins[url]; exists && old != id {
				badRequest(w, "conflicting logins for the same MCP URL")
				return
			}
			logins[url] = id
		}
		input.Logins = logins
		for name, login := range input.PluginLogins {
			if name == "" || login.Module == "" {
				badRequest(w, "a plugin and its MCP server name are required")
				return
			}
			url, ok := g.validateProjectLogin(w, r, account, login.URL, login.Credential)
			if !ok {
				return
			}
			login.URL = url
			input.PluginLogins[name] = login
		}
	}
	if g.options.SaveProject == nil {
		http.Error(w, "project settings are unavailable on this machine", http.StatusServiceUnavailable)
		return
	}
	if err := g.options.SaveProject(r.Context(), input); err != nil {
		if errors.Is(err, ErrProjectPluginServerChanged) {
			http.Error(w, ErrProjectPluginServerChanged.Error(), http.StatusConflict)
			return
		}
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
