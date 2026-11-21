package network

import (
	"bufio"
	"bytes"

	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// Linked worktrees live under $AGENTPACK_HOME/worktrees/<project-hash>/<slug>, never inside the
// repository. go-git cannot add linked worktrees, so everything here runs the git executable.

// gitTimeout bounds one git command; worktree add and push take the longer gitSlowTimeout.
var (
	gitTimeout     = 15 * time.Second
	gitSlowTimeout = 2 * time.Minute
)

// worktreeIncludes are copied from the main folder when Git ignores them, besides .worktreeinclude.
var worktreeIncludes = []string{".env", ".env.*", "**/*.local.json", ".claude/settings.local.json"}

var errNotWorktree = errors.New("not a worktree of this project")

type gitError struct {
	args   string
	stderr string
	err    error
}

func (e *gitError) Error() string {
	if e.stderr != "" {
		return e.stderr
	}
	return fmt.Sprintf("git %s: %v", e.args, e.err)
}
func (e *gitError) Unwrap() error { return e.err }

// runGit runs git in dir with the machine's own credentials and never prompts. The error carries
// git's first stderr lines, which never include credentials: git redacts them from URLs.
func runGit(ctx context.Context, timeout time.Duration, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 600 {
			message = message[:600]
		}
		return string(out), &gitError{args: strings.Join(args, " "), stderr: message, err: err}
	}
	return string(out), nil
}

func gitLine(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := runGit(ctx, gitTimeout, dir, args...)
	return strings.TrimSpace(out), err
}

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func worktreesRoot(home string) string { return filepath.Join(home, "worktrees") }

// insideWorktrees reports whether dir is a worktree folder (or below one) under home.
func insideWorktrees(home, dir string) bool {
	rel, err := filepath.Rel(worktreesRoot(home), filepath.Clean(dir))
	return err == nil && filepath.IsAbs(dir) && rel != "." && !strings.HasPrefix(rel, "..") && len(strings.Split(rel, string(filepath.Separator))) >= 2
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// worktreeSlug makes a branch-safe name from the first words of text.
func worktreeSlug(text string) string {
	words := strings.Fields(strings.ToLower(text))
	if len(words) > 6 {
		words = words[:6]
	}
	slug := strings.Trim(slugUnsafe.ReplaceAllString(strings.Join(words, "-"), "-"), "-")
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	if slug == "" {
		return "session-" + randomToken()[:6]
	}
	return slug
}

func projectHash(top string) string {
	sum := sha256.Sum256([]byte(top))
	return hex.EncodeToString(sum[:8])
}

type createdWorktree struct {
	Path      string // the worktree's root
	Directory string // where the session runs: the main folder's position inside the worktree
	Branch    string
	Head      string
	Carried   bool
}

// createWorktree cuts a new branch from main's current HEAD into a fresh linked worktree. carry
// brings uncommitted tracked changes along without touching main or leaving a stash entry.
func createWorktree(ctx context.Context, home, main, slug string, carry bool) (createdWorktree, error) {
	top, err := gitLine(ctx, main, "rev-parse", "--show-toplevel")
	if err != nil {
		return createdWorktree{}, err
	}
	real, err := filepath.EvalSymlinks(main)
	if err != nil {
		return createdWorktree{}, err
	}
	rel, err := filepath.Rel(filepath.Clean(top), real)
	if err != nil || strings.HasPrefix(rel, "..") {
		return createdWorktree{}, errors.New("folder is outside its repository")
	}
	root := filepath.Join(worktreesRoot(home), projectHash(top))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return createdWorktree{}, err
	}
	head, err := gitLine(ctx, top, "rev-parse", "HEAD")
	if err != nil {
		return createdWorktree{}, errors.New("the repository has no commits yet")
	}
	var path, branch string
	for n := 1; n <= 50 && path == ""; n++ {
		name := slug
		if n > 1 {
			name = fmt.Sprintf("%s-%d", slug, n)
		}
		candidate := "ap/" + name
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			continue
		}
		if _, err := runGit(ctx, gitTimeout, top, "show-ref", "--verify", "--quiet", "refs/heads/"+candidate); err == nil {
			continue
		}
		path, branch = filepath.Join(root, name), candidate
	}
	if path == "" {
		return createdWorktree{}, errors.New("no free branch name for this worktree")
	}
	if _, err := runGit(ctx, gitSlowTimeout, top, "worktree", "add", "-b", branch, path, head); err != nil {
		return createdWorktree{}, err
	}
	created := createdWorktree{Path: path, Directory: filepath.Join(path, rel), Branch: branch, Head: head}
	undo := func(cause error) (createdWorktree, error) {
		_, _ = runGit(context.WithoutCancel(ctx), gitTimeout, top, "worktree", "remove", "--force", path)
		_, _ = runGit(context.WithoutCancel(ctx), gitTimeout, top, "branch", "-D", branch)
		return createdWorktree{}, cause
	}
	if carry {
		// stash create records the changes as a commit without touching main or the stash list.
		stash, err := gitLine(ctx, top, "stash", "create")
		if err != nil {
			return undo(err)
		}
		if stash != "" {
			if _, err := runGit(ctx, gitTimeout, path, "stash", "apply", stash); err != nil {
				return undo(err)
			}
			created.Carried = true
		}
	}
	if err := copyWorktreeIncludes(ctx, top, path); err != nil {
		return undo(err)
	}
	if _, err := os.Stat(filepath.Join(path, ".gitmodules")); err == nil {
		if _, err := runGit(ctx, gitSlowTimeout, path, "submodule", "update", "--init"); err != nil {
			return undo(err)
		}
	}
	return created, nil
}

// copyWorktreeIncludes copies ignored files that a checkout needs to run (.env, local settings,
// whatever .worktreeinclude names) from top into the new worktree.
func copyWorktreeIncludes(ctx context.Context, top, worktree string) error {
	lines := append([]string{}, worktreeIncludes...)
	if data, err := os.ReadFile(filepath.Join(top, ".worktreeinclude")); err == nil {
		lines = append(lines, strings.Split(string(data), "\n")...)
	}
	var patterns []gitignore.Pattern
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, gitignore.ParsePattern(line, nil))
	}
	matcher := gitignore.NewMatcher(patterns)
	// --directory collapses an ignored folder to one entry, so node_modules is never listed file by file.
	// shortcut: a pattern only matches files that are listed or under a listed folder it matches; upgrade
	// when a repository needs a file deep inside an otherwise ignored folder.
	out, err := runGit(ctx, gitTimeout, top, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(out, "\x00") {
		if entry == "" {
			continue
		}
		dir := strings.HasSuffix(entry, "/")
		entry = strings.TrimSuffix(entry, "/")
		if !matcher.Match(strings.Split(entry, "/"), dir) {
			continue
		}
		if err := copyIncluded(filepath.Join(top, filepath.FromSlash(entry)), filepath.Join(worktree, filepath.FromSlash(entry))); err != nil {
			return err
		}
	}
	return nil
}

// copyIncluded copies a file or folder. Symlinks are recreated only when they stay inside the
// copied tree; nothing is followed out of it.
func copyIncluded(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(source, path)
		target := filepath.Join(destination, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case entry.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			// A rooted link ("/etc/x" has no volume on Windows, so it is not IsAbs there) or one that
			// climbs out of the included folder is not copied.
			rooted := link == "" || filepath.IsAbs(link) || filepath.VolumeName(link) != "" || os.IsPathSeparator(link[0])
			if rooted || !filepath.IsLocal(filepath.Join(filepath.Dir(rel), link)) {
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			return os.Symlink(link, target)
		case !entry.Type().IsRegular():
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

type worktreeEntry struct {
	path   string
	branch string
}

func listWorktrees(ctx context.Context, main string) ([]worktreeEntry, error) {
	out, err := runGit(ctx, gitTimeout, main, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var entries []worktreeEntry
	for block := range strings.SplitSeq(strings.TrimSpace(out), "\n\n") {
		var entry worktreeEntry
		for line := range strings.SplitSeq(block, "\n") {
			if path, ok := strings.CutPrefix(line, "worktree "); ok {
				entry.path = filepath.Clean(path)
			} else if branch, ok := strings.CutPrefix(line, "branch "); ok {
				entry.branch = strings.TrimPrefix(branch, "refs/heads/")
			}
		}
		if entry.path != "" {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// isWorktreeOf is the trust check: dir must be a linked worktree (not the main checkout) that
// git itself lists for main's repository.
func isWorktreeOf(ctx context.Context, main, dir string) bool {
	entries, err := listWorktrees(ctx, main)
	if err != nil || len(entries) < 2 {
		return false
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries[1:] {
		if canonical, err := filepath.EvalSymlinks(entry.path); err == nil && canonical == real {
			return true
		}
	}
	return false
}

// worktreeMain resolves the main folder a session directory inside a worktree belongs to. dir
// must sit under home/worktrees and git must list its root as a worktree of that main checkout.
func worktreeMain(ctx context.Context, home, dir string) (main, root string, err error) {
	if !insideWorktrees(home, dir) || !gitAvailable() {
		return "", "", errNotWorktree
	}
	root, err = gitLine(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", errNotWorktree
	}
	common, err := gitLine(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || filepath.Base(common) != ".git" {
		return "", "", errNotWorktree
	}
	top := filepath.Dir(common)
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", "", errNotWorktree
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || strings.HasPrefix(rel, "..") || !isWorktreeOf(ctx, top, root) {
		return "", "", errNotWorktree
	}
	// git reports root with links resolved (and forward slashes on Windows); hand it back in dir's
	// spelling, so it stays comparable with paths built from home.
	root = filepath.Clean(dir)
	for up := rel; up != "."; up = filepath.Dir(up) {
		root = filepath.Dir(root)
	}
	return filepath.Join(top, rel), root, nil
}

// WorktreeMain is worktreeMain for the CLI's remote-start check.
func WorktreeMain(ctx context.Context, home, dir string) (string, error) {
	main, _, err := worktreeMain(ctx, home, dir)
	return main, err
}

type worktreeStatus struct {
	Branch    string `json:"branch"`
	Head      string `json:"head"`
	Upstream  string `json:"upstream,omitempty"`
	Ahead     int    `json:"ahead"`
	Behind    int    `json:"behind"`
	Dirty     int    `json:"dirty"`
	Untracked int    `json:"untracked"`
}

// readStatus parses git status --porcelain=v2 --branch. Ahead and behind are against the upstream.
func readStatus(ctx context.Context, dir string) (worktreeStatus, error) {
	out, err := runGit(ctx, gitTimeout, dir, "status", "--porcelain=v2", "--branch")
	if err != nil {
		return worktreeStatus{}, err
	}
	var status worktreeStatus
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			status.Head = strings.TrimPrefix(line, "# branch.oid ")
		case strings.HasPrefix(line, "# branch.head "):
			if head := strings.TrimPrefix(line, "# branch.head "); head != "(detached)" {
				status.Branch = head
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			status.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(fields) == 2 {
				status.Ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
				status.Behind, _ = strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
			}
		case strings.HasPrefix(line, "? "):
			status.Untracked++
		case line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "! "):
			status.Dirty++
		}
	}
	if status.Head == "(initial)" {
		status.Head = ""
	}
	return status, scanner.Err()
}

// aheadBehind counts commits of head missing from base and the reverse.
func aheadBehind(ctx context.Context, dir, base, head string) (ahead, behind int, err error) {
	out, err := gitLine(ctx, dir, "rev-list", "--left-right", "--count", base+"..."+head)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, errors.New("unexpected rev-list output")
	}
	behind, _ = strconv.Atoi(fields[0])
	ahead, _ = strconv.Atoi(fields[1])
	return ahead, behind, nil
}

// merged reports whether every commit on head is already in base.
func merged(ctx context.Context, dir, base, head string) bool {
	_, err := runGit(ctx, gitTimeout, dir, "merge-base", "--is-ancestor", head, base)
	return err == nil
}

// removeWorktree removes a linked worktree. Without discard git refuses a tree with changes.
// A worktree whose main checkout is gone is deleted and pruned from nothing.
func removeWorktree(ctx context.Context, home, root string, discard bool) error {
	if !insideWorktrees(home, root) || filepath.Dir(filepath.Dir(filepath.Clean(root))) != worktreesRoot(home) {
		return errNotWorktree
	}
	main, top, err := worktreeMain(ctx, home, root)
	if err != nil {
		// Orphaned: the main repository is gone, so git cannot open the worktree. One whose
		// repository still opens but failed the trust check is never deleted by hand.
		if _, gitErr := gitLine(ctx, root, "rev-parse", "--git-common-dir"); gitErr == nil {
			return err
		}
		return os.RemoveAll(root)
	}
	args := []string{"worktree", "remove"}
	if discard {
		args = append(args, "--force")
	}
	_, err = runGit(ctx, gitTimeout, main, append(args, top)...)
	return err
}

// dirSize sums regular file sizes below dir, cached for ten minutes because it walks the tree.
var (
	sizeMu    sync.Mutex
	sizeCache = map[string]struct {
		at   time.Time
		size int64
	}{}
)

func dirSize(dir string) int64 {
	sizeMu.Lock()
	cached, ok := sizeCache[dir]
	sizeMu.Unlock()
	if ok && time.Since(cached.at) < 10*time.Minute {
		return cached.size
	}
	var size int64
	_ = filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			if info, err := entry.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	sizeMu.Lock()
	sizeCache[dir] = struct {
		at   time.Time
		size int64
	}{time.Now(), size}
	sizeMu.Unlock()
	return size
}
