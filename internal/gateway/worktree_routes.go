package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

func (g *Gateway) registerWorktrees(mux routeRegistrar) {
	mux.HandleFunc("GET /api/worktrees", g.serveWorktrees)
	mux.HandleFunc("POST /api/worktrees/remove", g.serveWorktreeRemove)
	mux.HandleFunc("POST /api/worktrees/push", g.serveWorktreePush)
	mux.HandleFunc("GET /api/sessions/{id}/git", g.serveSessionGit)
	mux.HandleFunc("POST /api/sessions/{id}/git/commit", g.serveSessionCommit)
	mux.HandleFunc("POST /api/sessions/{id}/git/pr", g.serveSessionPR)
	mux.HandleFunc("POST /api/sessions/{id}/git/merge", g.serveSessionMerge)
}

func gitFailure(w http.ResponseWriter, r *http.Request, status int, err error, extra map[string]any) {
	if status >= 500 {
		obs.From(r.Context()).Fail(err)
	}
	body := map[string]any{"error": obs.Redact(err.Error())}
	for key, value := range extra {
		body[key] = value
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// newSessionWorktree resolves POST /api/sessions' worktree request for a startable main folder:
// "new" creates one, anything else must be an existing worktree git lists for that folder.
func (g *Gateway) newSessionWorktree(ctx context.Context, actor session.Actor, main, worktree, name string, carry bool) (string, error) {
	if !g.startableDirectory(ctx, actor, main) {
		return "", errors.New("choose a recent folder from this node")
	}
	if !gitAvailable() {
		return "", errors.New("git is not installed on this machine")
	}
	if worktree == "new" {
		ctx, event := obs.Start(ctx, "gateway", "worktree_create")
		defer event.End()
		created, err := createWorktree(ctx, g.options.Home, main, worktreeSlug(name), carry)
		if err != nil {
			event.Fail(err)
			return "", err
		}
		return created.Directory, nil
	}
	// An existing worktree: git must list it for the same repository as main.
	worktreeTop, root, err := worktreeMain(ctx, g.options.Home, filepath.Clean(worktree))
	if err != nil {
		return "", err
	}
	top, err := gitLine(ctx, main, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(top) != filepath.Clean(worktreeTop) {
		return "", errNotWorktree
	}
	real, err := filepath.EvalSymlinks(main)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(top, real)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errNotWorktree
	}
	return filepath.Join(root, rel), nil
}

// definitionRootFor is what a session in a worktree of main uses for packages and modes: the main
// folder's own definition root, so staging, logins and MCP sign-ins stay the project's.
func (g *Gateway) definitionRootFor(ctx context.Context, main string) string {
	recent, err := network.RecentIn(ctx, g.store.Reader)
	if err == nil {
		for _, entry := range recent {
			if filepath.Clean(entry.Directory) == filepath.Clean(main) && entry.DefinitionRoot != "" {
				if identity, err := network.FolderIdentity(main); err == nil && network.SameFolder(entry.FolderID, identity) {
					return entry.DefinitionRoot
				}
				break
			}
		}
	}
	return main
}

// knownWorktree accepts a session directory inside a worktree whose main folder is known here.
func (g *Gateway) knownWorktree(ctx context.Context, dir string) bool {
	main, _, err := worktreeMain(ctx, g.options.Home, dir)
	return err == nil && g.knownDirectory(ctx, main)
}

type worktreeRow struct {
	worktreeStatus
	Path     string   `json:"path"`
	Main     string   `json:"main,omitempty"`
	Base     string   `json:"base,omitempty"`
	Unpushed int      `json:"unpushed"`
	Merged   bool     `json:"merged"`
	Orphaned bool     `json:"orphaned"`
	Sessions []string `json:"sessions"`
	Running  int      `json:"running"`
	Size     int64    `json:"size"`
	Error    string   `json:"error,omitempty"`
}

func (g *Gateway) worktreeSessions(ctx context.Context) ([]session.SessionInfo, error) {
	return session.QuerySessions(ctx, g.store.Reader, "SELECT "+session.SessionColumns+" FROM sessions WHERE node_id="+state.LocalNodeSQL+" AND hidden=0 AND directory<>''")
}

func within(root, dir string) bool {
	rel, err := filepath.Rel(root, filepath.Clean(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// serveWorktrees lists every worktree agentpack made on this machine, with what is still in it.
func (g *Gateway) serveWorktrees(w http.ResponseWriter, r *http.Request) {
	rows := []worktreeRow{}
	if !gitAvailable() {
		network.WriteJSON(w, map[string]any{"git": false, "worktrees": rows})
		return
	}
	sessions, err := g.worktreeSessions(r.Context())
	if err != nil {
		gitFailure(w, r, http.StatusInternalServerError, err, nil)
		return
	}
	var directories []string
	if directory := r.URL.Query().Get("directory"); directory != "" {
		if !filepath.IsAbs(directory) {
			badRequest(w, errFolderPath.Error())
			return
		}
		actor, _ := session.ActorFrom(r.Context())
		if !g.startableDirectory(r.Context(), actor, directory) {
			badRequest(w, "choose a project folder from this node")
			return
		}
		entries, _ := listWorktrees(r.Context(), directory)
		for _, entry := range entries {
			if insideWorktrees(g.options.Home, entry.path) {
				directories = append(directories, entry.path)
			}
		}
	} else if root := worktreesRoot(g.options.Home); !paths.RequiresUserAccess(root) {
		projects, _ := os.ReadDir(root)
		for _, project := range projects {
			parent := filepath.Join(root, project.Name())
			if !project.IsDir() || paths.RequiresUserAccess(parent) {
				continue
			}
			entries, _ := os.ReadDir(parent)
			for _, entry := range entries {
				path := filepath.Join(parent, entry.Name())
				if entry.IsDir() && !backgroundGitDeferred(path) {
					directories = append(directories, path)
				}
			}
		}
	}
	rows = make([]worktreeRow, len(directories))
	var group errgroup.Group
	group.SetLimit(8)
	for i, path := range directories {
		group.Go(func() error {
			rows[i] = g.worktreeRow(r.Context(), path, sessions)
			return nil
		})
	}
	_ = group.Wait()
	network.WriteJSON(w, map[string]any{"git": true, "worktrees": rows})
}

func (g *Gateway) worktreeRow(ctx context.Context, path string, sessions []session.SessionInfo) worktreeRow {
	row := worktreeRow{Path: path, Sessions: []string{}, Size: dirSize(path)}
	for _, session := range sessions {
		if within(path, session.Directory) {
			row.Sessions = append(row.Sessions, session.ID)
			if session.EndedAt.IsZero() {
				row.Running++
			}
		}
	}
	main, _, err := worktreeMain(ctx, g.options.Home, path)
	if err != nil {
		row.Orphaned = true
		return row
	}
	row.Main = main
	if row.worktreeStatus, err = readStatus(ctx, path); err != nil {
		row.Error = err.Error()
		return row
	}
	row.Base, _ = gitLine(ctx, main, "symbolic-ref", "--short", "-q", "HEAD")
	if mainHead, err := gitLine(ctx, main, "rev-parse", "HEAD"); err == nil && row.Head != "" {
		row.Merged, row.Unpushed = g.worktreeAncestry(ctx, path, mainHead, row.worktreeStatus)
		row.Merged = row.Merged || pullRequestMerged(ctx, path, row.worktreeStatus)
	}
	return row
}

// worktreeDivergence caches commit history for a pair of immutable tips; the
// working tree and upstream counts still come from the current git status.
type worktreeDivergence struct {
	base, head string
	at         time.Time
	ahead      int
}

func (g *Gateway) worktreeAncestry(ctx context.Context, path, base string, status worktreeStatus) (bool, int) {
	if status.Upstream != "" {
		return merged(ctx, path, base, status.Head), status.Ahead
	}
	g.worktreeHistoryMu.Lock()
	cached := g.worktreeHistory[path]
	g.worktreeHistoryMu.Unlock()
	if cached.base == base && cached.head == status.Head && time.Since(cached.at) < 30*time.Second {
		return cached.ahead == 0, cached.ahead
	}
	ahead, _, err := aheadBehind(ctx, path, base, status.Head)
	if err != nil {
		return false, 0
	}
	g.worktreeHistoryMu.Lock()
	if g.worktreeHistory == nil || len(g.worktreeHistory) >= 128 {
		g.worktreeHistory = make(map[string]worktreeDivergence)
	}
	g.worktreeHistory[path] = worktreeDivergence{base: base, head: status.Head, at: time.Now(), ahead: ahead}
	g.worktreeHistoryMu.Unlock()
	return ahead == 0, ahead
}

// worktreeRoot validates a client-named worktree: a folder directly under home/worktrees/<hash>.
func (g *Gateway) worktreeRoot(path string) (string, bool) {
	path = filepath.Clean(path)
	return path, filepath.IsAbs(path) && filepath.Dir(filepath.Dir(path)) == worktreesRoot(g.options.Home)
}

func (g *Gateway) serveWorktreeRemove(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path    string `json:"path"`
		Discard bool   `json:"discard"`
	}
	if !network.ReadJSON(w, r, &input) {
		return
	}
	path, ok := g.worktreeRoot(input.Path)
	if !ok {
		http.Error(w, "choose a worktree from this machine", http.StatusBadRequest)
		return
	}
	sessions, err := g.worktreeSessions(r.Context())
	if err != nil {
		gitFailure(w, r, http.StatusInternalServerError, err, nil)
		return
	}
	for _, session := range sessions {
		if within(path, session.Directory) && session.EndedAt.IsZero() {
			gitFailure(w, r, http.StatusConflict, errors.New("Stop the sessions running in this worktree first."), nil)
			return
		}
	}
	if err := removeWorktree(r.Context(), g.options.Home, path, input.Discard); err != nil {
		gitFailure(w, r, http.StatusConflict, err, nil)
		return
	}
	network.WriteJSON(w, map[string]bool{"removed": true})
}

func (g *Gateway) serveWorktreePush(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path string `json:"path"`
	}
	if !network.ReadJSON(w, r, &input) {
		return
	}
	path, ok := g.worktreeRoot(input.Path)
	if !ok {
		http.Error(w, "choose a worktree from this machine", http.StatusBadRequest)
		return
	}
	if _, _, err := worktreeMain(r.Context(), g.options.Home, path); err != nil {
		gitFailure(w, r, http.StatusBadRequest, err, nil)
		return
	}
	branch, err := gitLine(r.Context(), path, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		gitFailure(w, r, http.StatusConflict, errors.New("The worktree is not on a branch."), nil)
		return
	}
	if _, err := runGit(r.Context(), gitSlowTimeout, path, "push", "-u", "origin", branch); err != nil {
		gitFailure(w, r, http.StatusBadGateway, err, nil)
		return
	}
	network.WriteJSON(w, map[string]bool{"pushed": true})
}

type checkSummary struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
}

type pullRequest struct {
	Number int          `json:"number"`
	URL    string       `json:"url"`
	State  string       `json:"state"`
	Checks checkSummary `json:"checks"`
	head   string
}

type sessionGit struct {
	Available bool `json:"available"`
	Deferred  bool `json:"deferred,omitempty"`
	// Removed is a worktree session whose worktree no longer exists.
	Removed bool `json:"removed,omitempty"`
	worktreeStatus
	Base     string       `json:"base,omitempty"`
	Worktree bool         `json:"worktree"`
	Path     string       `json:"path,omitempty"`
	Main     string       `json:"main,omitempty"`
	Merged   bool         `json:"merged"`
	Added    int          `json:"added"`
	Deleted  int          `json:"deleted"`
	GH       bool         `json:"gh"`
	PR       *pullRequest `json:"pr,omitempty"`
}

// localSession reads a session this machine runs; a peer's session is reached with ?node=.
func (g *Gateway) localSession(w http.ResponseWriter, r *http.Request) (session.SessionInfo, bool) {
	info, err := session.StoredSession(r.Context(), g.store.Reader, r.PathValue("id"))
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return info, false
	}
	local, err := g.store.NodeID(r.Context())
	if err != nil || info.NodeID != local || info.Directory == "" {
		http.Error(w, "session not found", http.StatusNotFound)
		return info, false
	}
	return info, true
}

func (g *Gateway) readSessionGit(ctx context.Context, info session.SessionInfo) sessionGit {
	result := sessionGit{}
	if !gitAvailable() {
		return result
	}
	var main, root string
	var worktreeErr error
	var checks sync.WaitGroup
	checks.Go(func() { main, root, worktreeErr = worktreeMain(ctx, g.options.Home, info.Directory) })
	status, err := readStatus(ctx, info.Directory)
	checks.Wait()
	if err != nil {
		result.Removed = insideWorktrees(g.options.Home, info.Directory)
		result.Worktree = result.Removed
		return result
	}
	result.Available, result.worktreeStatus = true, status
	// Lines changed: a worktree against where it left the main folder, a shared folder uncommitted only.
	since := "HEAD"
	if worktreeErr == nil {
		result.Worktree, result.Path, result.Main = true, root, main
		var mainHead string
		checks.Go(func() { result.Base, _ = gitLine(ctx, main, "symbolic-ref", "--short", "-q", "HEAD") })
		checks.Go(func() { mainHead, _ = gitLine(ctx, main, "rev-parse", "HEAD") })
		checks.Wait()
		if mainHead != "" && status.Head != "" {
			graph := worktreeCommitGraph(ctx, root, mainHead, status.Head)
			result.Ahead, result.Behind, result.Merged = graph.ahead, graph.behind, graph.fork == status.Head
			if graph.fork != "" {
				since = graph.fork
			}
		}
	} else if status.Upstream != "" {
		result.Base = status.Upstream
	}
	if status.Head != "" {
		result.Added, result.Deleted = sessionDiffstat(ctx, info.Directory, since, status)
	}
	if _, err := paths.LookPathForBackground("gh"); err == nil {
		result.GH = true
		if status.Branch != "" {
			result.PR = g.backgroundPullRequest(ctx, info.Directory, status.Branch)
		}
	}
	return result
}

type diffstatKey struct {
	directory, base, head string
	indexTime             time.Time
	indexSize             int64
}

type cachedDiffstat struct {
	index          os.FileInfo
	added, deleted int
}

var sessionDiffstats struct {
	sync.Mutex
	values map[diffstatKey]cachedDiffstat
}

func sessionDiffstat(ctx context.Context, dir, since string, status worktreeStatus) (int, int) {
	args := []string{"diff", "--shortstat", since}
	key := diffstatKey{directory: dir, base: since, head: status.Head}
	cacheable := !status.unstaged
	var index os.FileInfo
	var indexPath string
	if cacheable && status.Dirty != 0 {
		indexPath, _ = gitLine(ctx, dir, "rev-parse", "--path-format=absolute", "--git-path", "index")
		index, _ = os.Stat(indexPath)
		cacheable = index != nil && index.Mode().IsRegular()
		if cacheable {
			key.indexTime, key.indexSize = index.ModTime(), index.Size()
			args = []string{"diff", "--cached", "--shortstat", since}
		}
	} else if cacheable {
		if since == "HEAD" {
			return 0, 0
		}
		args = []string{"diff", "--shortstat", since, status.Head}
	}
	if cacheable {
		sessionDiffstats.Lock()
		cached, ok := sessionDiffstats.values[key]
		sessionDiffstats.Unlock()
		if ok && (index == nil || cached.index != nil && os.SameFile(index, cached.index)) {
			return cached.added, cached.deleted
		}
	}
	stat, err := gitLine(ctx, dir, args...)
	if err != nil {
		return 0, 0
	}
	added, deleted := shortstat(stat)
	if index != nil && cacheable {
		current, err := os.Stat(indexPath)
		cacheable = err == nil && os.SameFile(index, current) && current.ModTime() == key.indexTime && current.Size() == key.indexSize
	}
	if cacheable {
		sessionDiffstats.Lock()
		if len(sessionDiffstats.values) >= 128 || sessionDiffstats.values == nil {
			sessionDiffstats.values = make(map[diffstatKey]cachedDiffstat)
		}
		sessionDiffstats.values[key] = cachedDiffstat{index, added, deleted}
		sessionDiffstats.Unlock()
	}
	return added, deleted
}

type commitGraph struct {
	ahead, behind int
	fork          string
}

var commitGraphs struct {
	sync.Mutex
	byHead map[[2]string]commitGraph
}

func worktreeCommitGraph(ctx context.Context, dir, main, head string) commitGraph {
	key := [2]string{main, head}
	commitGraphs.Lock()
	cached, ok := commitGraphs.byHead[key]
	commitGraphs.Unlock()
	if ok {
		return cached
	}
	var graph commitGraph
	var countErr, forkErr error
	var checks sync.WaitGroup
	checks.Go(func() { graph.ahead, graph.behind, countErr = aheadBehind(ctx, dir, main, head) })
	checks.Go(func() { graph.fork, forkErr = gitLine(ctx, dir, "merge-base", main, head) })
	checks.Wait()
	if countErr == nil && forkErr == nil {
		commitGraphs.Lock()
		if len(commitGraphs.byHead) >= 128 || commitGraphs.byHead == nil {
			commitGraphs.byHead = make(map[[2]string]commitGraph)
		}
		commitGraphs.byHead[key] = graph
		commitGraphs.Unlock()
	}
	return graph
}

var shortstatNumber = regexp.MustCompile(`(\d+) (insertion|deletion)`)

// shortstat reads "3 files changed, 10 insertions(+), 2 deletions(-)".
func shortstat(line string) (added, deleted int) {
	for _, match := range shortstatNumber.FindAllStringSubmatch(line, -1) {
		n, _ := strconv.Atoi(match[1])
		if match[2] == "insertion" {
			added = n
		} else {
			deleted = n
		}
	}
	return added, deleted
}

func (g *Gateway) serveSessionGit(w http.ResponseWriter, r *http.Request) {
	info, ok := g.localSession(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("inspect") != "1" && (paths.RequiresUserAccess(info.WorktreeOf) || backgroundGitDeferred(info.Directory)) {
		network.WriteJSON(w, sessionGit{Deferred: true})
		return
	}
	network.WriteJSON(w, g.readSessionGit(r.Context(), info))
}

var (
	prMu      sync.Mutex
	prRefresh singleflight.Group
	prCache   = map[string]struct {
		at time.Time
		pr *pullRequest
	}{}
	// ghTimeout bounds one gh call; prCacheTTL is how long a PR read is reused.
	ghTimeout  = 5 * time.Second
	prCacheTTL = 30 * time.Second
)

// Git status is visible immediately; a changed PR refreshes the active inspector through /api/events.
func (g *Gateway) backgroundPullRequest(ctx context.Context, dir, branch string) *pullRequest {
	key := dir + "\x00" + branch
	prMu.Lock()
	cached, found := prCache[key]
	prMu.Unlock()
	if !found || time.Since(cached.at) >= prCacheTTL {
		_ = prRefresh.DoChan(key, func() (any, error) {
			next := cachedPullRequest(context.WithoutCancel(ctx), dir, branch)
			if cached.pr == nil && next != nil || cached.pr != nil && (next == nil || *cached.pr != *next) {
				g.emitEvent(eventMessage{Topic: "session-git"})
			}
			return nil, nil
		})
	}
	return cached.pr
}

// cachedPullRequest asks gh for the branch's pull request, at most every 30 s per branch.
// A failure (no PR, no login, no network) reads as no pull request.
func cachedPullRequest(ctx context.Context, dir, branch string) *pullRequest {
	key := dir + "\x00" + branch
	prMu.Lock()
	cached, ok := prCache[key]
	prMu.Unlock()
	if ok && time.Since(cached.at) < prCacheTTL {
		return cached.pr
	}
	pr := viewPullRequest(ctx, dir, branch)
	prMu.Lock()
	prCache[key] = struct {
		at time.Time
		pr *pullRequest
	}{time.Now(), pr}
	prMu.Unlock()
	return pr
}

// pullRequestMerged is how a squash or rebase merge shows, which leaves no merge in the local
// branch: the branch was pushed, gh reports its pull request merged, and the commit checked out
// is one the pull request contained. Without gh it is false.
func pullRequestMerged(ctx context.Context, dir string, status worktreeStatus) bool {
	if status.Branch == "" || status.Upstream == "" || status.Head == "" {
		return false
	}
	if _, err := paths.LookPathForBackground("gh"); err != nil {
		return false
	}
	pr := cachedPullRequest(ctx, dir, status.Branch)
	return pr != nil && pr.State == "MERGED" && pr.head != "" && merged(ctx, dir, pr.head, status.Head)
}

func forgetPullRequest(dir, branch string) {
	prMu.Lock()
	delete(prCache, dir+"\x00"+branch)
	prMu.Unlock()
}

func runGH(ctx context.Context, timeout time.Duration, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	exe, err := paths.LookPathForBackground("gh")
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, exe, args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	out, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return string(out), errors.New(strings.TrimSpace(string(exit.Stderr)))
		}
	}
	return string(out), err
}

func viewPullRequest(ctx context.Context, dir, branch string) *pullRequest {
	out, err := runGH(ctx, ghTimeout, dir, "pr", "view", branch, "--json", "number,url,state,headRefOid,statusCheckRollup")
	if err != nil {
		return nil
	}
	var body struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
		State  string `json:"state"`
		Head   string `json:"headRefOid"`
		Checks []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"`
		} `json:"statusCheckRollup"`
	}
	if json.Unmarshal([]byte(out), &body) != nil || body.Number == 0 {
		return nil
	}
	pr := &pullRequest{Number: body.Number, URL: body.URL, State: body.State, head: body.Head}
	for _, check := range body.Checks {
		pr.Checks.Total++
		// Check runs report status and conclusion; commit statuses report state.
		switch outcome := strings.ToUpper(network.CmpOr(check.Conclusion, check.State)); {
		case outcome == "SUCCESS" || outcome == "NEUTRAL" || outcome == "SKIPPED":
			pr.Checks.Passed++
		case outcome == "" || outcome == "PENDING" || outcome == "EXPECTED" || check.Status != "" && check.Status != "COMPLETED":
			pr.Checks.Pending++
		default:
			pr.Checks.Failed++
		}
	}
	return pr
}

// worktreeSession is the shared guard for commit, PR and merge: the caller may write to the
// session and it runs in a worktree. In a shared folder, other agents' edits would be swept in.
func (g *Gateway) worktreeSession(w http.ResponseWriter, r *http.Request) (info session.SessionInfo, main, root string, ok bool) {
	info, ok = g.localSession(w, r)
	if !ok || !session.AuthorizeWrite(w, r, info) {
		return info, "", "", false
	}
	main, root, err := worktreeMain(r.Context(), g.options.Home, info.Directory)
	if err != nil {
		gitFailure(w, r, http.StatusConflict, errors.New("Only sessions in their own worktree can commit, open pull requests or merge."), nil)
		return info, "", "", false
	}
	return info, main, root, true
}

func (g *Gateway) serveSessionCommit(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Message string `json:"message"`
	}
	if !network.ReadJSON(w, r, &input) {
		return
	}
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" {
		http.Error(w, "a commit message is required", http.StatusBadRequest)
		return
	}
	_, _, root, ok := g.worktreeSession(w, r)
	if !ok {
		return
	}
	if _, err := runGit(r.Context(), gitTimeout, root, "add", "-A"); err != nil {
		gitFailure(w, r, http.StatusConflict, err, nil)
		return
	}
	if status, err := readStatus(r.Context(), root); err == nil && status.Dirty == 0 {
		gitFailure(w, r, http.StatusConflict, errors.New("Nothing to commit."), nil)
		return
	}
	if _, err := runGit(r.Context(), gitTimeout, root, "commit", "-q", "-m", input.Message); err != nil {
		gitFailure(w, r, http.StatusConflict, err, nil)
		return
	}
	head, _ := gitLine(r.Context(), root, "rev-parse", "HEAD")
	network.WriteJSON(w, map[string]string{"commit": head})
}

var githubRemote = regexp.MustCompile(`^(?:https://(?:[^@/]+@)?github\.com/|git@github\.com:|ssh://git@github\.com/)([^/]+)/([^/]+?)(?:\.git)?/?$`)

// compareURL is GitHub's page for opening a pull request by hand, or "" for another host.
func compareURL(remote, base, branch string) string {
	match := githubRemote.FindStringSubmatch(strings.TrimSpace(remote))
	if match == nil {
		return ""
	}
	return "https://github.com/" + match[1] + "/" + match[2] + "/compare/" + url.PathEscape(base) + "..." + url.PathEscape(branch) + "?expand=1"
}

// serveSessionPR pushes the worktree's branch, then opens a pull request with gh, or returns
// GitHub's compare page when gh is not installed.
func (g *Gateway) serveSessionPR(w http.ResponseWriter, r *http.Request) {
	_, main, root, ok := g.worktreeSession(w, r)
	if !ok {
		return
	}
	branch, err := gitLine(r.Context(), root, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		gitFailure(w, r, http.StatusConflict, errors.New("The worktree is not on a branch."), nil)
		return
	}
	if _, err := runGit(r.Context(), gitSlowTimeout, root, "push", "-u", "origin", branch); err != nil {
		gitFailure(w, r, http.StatusBadGateway, err, nil)
		return
	}
	defer forgetPullRequest(root, branch)
	if _, err := paths.LookPathForBackground("gh"); err == nil {
		if pr := viewPullRequest(r.Context(), root, branch); pr != nil && pr.State == "OPEN" {
			network.WriteJSON(w, map[string]any{"pushed": true, "created": false, "url": pr.URL})
			return
		}
		args := []string{"pr", "create", "--fill", "--head", branch}
		if base, err := gitLine(r.Context(), main, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && base != "" {
			args = append(args, "--base", base)
		}
		out, err := runGH(r.Context(), gitSlowTimeout, root, args...)
		if err != nil {
			gitFailure(w, r, http.StatusBadGateway, err, nil)
			return
		}
		lines := strings.Fields(out)
		created := ""
		if len(lines) > 0 {
			created = lines[len(lines)-1]
		}
		network.WriteJSON(w, map[string]any{"pushed": true, "created": true, "url": created})
		return
	}
	remote, _ := gitLine(r.Context(), root, "remote", "get-url", "origin")
	base, _ := gitLine(r.Context(), main, "symbolic-ref", "--short", "-q", "HEAD")
	network.WriteJSON(w, map[string]any{"pushed": true, "created": false, "url": compareURL(remote, network.CmpOr(base, "main"), branch)})
}

// serveSessionMerge merges the worktree's branch into the main folder with --no-ff, only when the
// main folder is on a branch and has no uncommitted changes. A conflict is aborted and answers 409.
func (g *Gateway) serveSessionMerge(w http.ResponseWriter, r *http.Request) {
	_, main, root, ok := g.worktreeSession(w, r)
	if !ok {
		return
	}
	branch, err := gitLine(r.Context(), root, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		gitFailure(w, r, http.StatusConflict, errors.New("The worktree is not on a branch."), nil)
		return
	}
	if status, err := readStatus(r.Context(), root); err != nil || status.Dirty > 0 {
		gitFailure(w, r, http.StatusConflict, errors.New("Commit the worktree's changes before merging."), nil)
		return
	}
	if _, err := gitLine(r.Context(), main, "symbolic-ref", "-q", "HEAD"); err != nil {
		gitFailure(w, r, http.StatusConflict, errors.New("The main folder is not on a branch."), nil)
		return
	}
	if out, err := gitLine(r.Context(), main, "status", "--porcelain", "--untracked-files=no"); err != nil || out != "" {
		gitFailure(w, r, http.StatusConflict, errors.New("The main folder has uncommitted changes."), nil)
		return
	}
	if _, err := runGit(r.Context(), gitSlowTimeout, main, "merge", "--no-ff", "--no-edit", branch); err != nil {
		_, _ = runGit(context.WithoutCancel(r.Context()), gitTimeout, main, "merge", "--abort")
		gitFailure(w, r, http.StatusConflict, errors.New("The branch conflicts with the main folder. Nothing was merged."), map[string]any{"conflict": true})
		return
	}
	network.WriteJSON(w, map[string]bool{"merged": true})
}

// removeArchivedWorktree runs after a session is archived: its worktree goes when no other
// session uses it, it has no changes and its commits are on the main folder's branch or in a
// merged pull request.
func (g *Gateway) removeArchivedWorktree(ctx context.Context, info session.SessionInfo) {
	if !insideWorktrees(g.options.Home, info.Directory) || paths.RequiresUserAccess(info.WorktreeOf) || backgroundGitDeferred(info.Directory) {
		return
	}
	ctx, event := obs.Start(ctx, "gateway", "worktree_auto_remove")
	defer event.End()
	event.Set("session_id", info.ID)
	main, root, err := worktreeMain(ctx, g.options.Home, info.Directory)
	if err != nil {
		return
	}
	sessions, err := g.worktreeSessions(ctx)
	if err != nil {
		event.Fail(err)
		return
	}
	for _, session := range sessions {
		if session.ID != info.ID && within(root, session.Directory) {
			return
		}
	}
	status, err := readStatus(ctx, root)
	if err != nil || status.Dirty+status.Untracked > 0 {
		return
	}
	mainHead, err := gitLine(ctx, main, "rev-parse", "HEAD")
	if (err != nil || !merged(ctx, root, mainHead, "HEAD")) && !pullRequestMerged(ctx, root, status) {
		return
	}
	if _, err := runGit(ctx, gitTimeout, main, "worktree", "remove", root); err != nil {
		event.Fail(err)
		return
	}
	event.Set("removed", true)
}
