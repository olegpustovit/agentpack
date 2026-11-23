package codex

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/gofrs/flock"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/pelletier/go-toml/v2"
)

func New() base.Harness {
	return base.Harness{
		Target:      base.Codex,
		Root:        stagedRoot,
		Reset:       resetPaths,
		BeforeReset: preReset,
		Begin:       beginGeneration,
		Setup:       prepare,
		ProjectMCP:  planMCP,
		Guidance:    injectGuidance,
		Check:       verify,
		Launch:      launch,
		ACPAdapter:  codexACPPackage,
		LaunchACP:   acpLaunch,
		Capability:  Capability,
		Inheritance: base.Inheritance{
			Note:  "Codex keeps native history/auth continuity under CODEX_HOME bridging",
			Paths: func(string) []string { return base.HomeFiles(".codex/config.toml", ".codex/AGENTS.md") },
			Seed:  func() (string, string) { return base.HomeFile(".codex/config.toml") },
		},
	}
}

func launch(ctx base.LaunchContext) (*exec.Cmd, func() error, error) {
	arguments := append([]string(nil), ctx.Arguments...)
	if ctx.Yolo && !base.HasAny(arguments, "--dangerously-bypass-approvals-and-sandbox", "--yolo") {
		flag := "--dangerously-bypass-approvals-and-sandbox"
		if len(arguments) == 0 || strings.HasPrefix(arguments[0], "-") {
			arguments = append([]string{flag}, arguments...)
		} else {
			arguments = append(arguments[:1], append([]string{flag}, arguments[1:]...)...)
		}
	}
	binary, err := base.ResolveBinary(base.Codex.Binary())
	if err != nil {
		return nil, nil, err
	}
	home, lease, err := acquireGenerationLease(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return nil, nil, err
	}
	keepLease := false
	defer func() {
		if !keepLease {
			_ = lease.Unlock()
		}
	}()
	if needsShortHome(home) {
		if shortDir, err := shortStagingCodexHome(ctx.ProjectRoot, ctx.Mode.Name()); err == nil {
			home = shortDir
		}
	}
	if _, err := os.Lstat(filepath.Join(home, credentialsBaselineFile)); err == nil {
		if err := reconcileMCPAuthMode(ctx.ProjectRoot, ctx.Mode.Name()); err != nil {
			return nil, nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	var loginHome string
	if isCodexLogin(arguments) {
		if loginHome, err = prepareIsolatedLoginHome(home); err != nil {
			return nil, nil, err
		}
	}
	if ctx.SessionID != "" && !isCodexLogin(arguments) {
		// Per launch, never staged: the session ID would otherwise enter the shared generation.
		if executable, err := os.Executable(); err == nil {
			notify, err := sessionNotify(home, executable, ctx.SessionID)
			if err != nil {
				return nil, nil, err
			}
			arguments = append([]string{"-c", notify}, arguments...)
		}
	}
	command := exec.Command(binary, arguments...)
	command.Env = append(os.Environ(), "CODEX_HOME="+cmp.Or(loginHome, home))
	keepLease = true
	return command, func() error { return finishLaunch(ctx, home, loginHome, lease) }, nil
}

func isCodexLogin(arguments []string) bool {
	command, tail := codexCommand(arguments)
	if command != "login" {
		return false
	}
	subcommand, _ := codexCommand(tail)
	return subcommand != "status"
}

func isCodexLogout(arguments []string) bool {
	command, _ := codexCommand(arguments)
	return command == "logout"
}

func codexCommand(arguments []string) (string, []string) {
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			return "", nil
		}
		if codexOptionTakesValue(argument) {
			index++
			continue
		}
		if strings.HasPrefix(argument, "-") {
			continue
		}
		return argument, arguments[index+1:]
	}
	return "", nil
}

func codexOptionTakesValue(argument string) bool {
	switch argument {
	case "-c", "--config", "--enable", "--disable", "--remote", "--remote-auth-token-env", "-i", "--image", "-m", "--model", "--local-provider", "-p", "--profile", "-s", "--sandbox", "-C", "--cd", "--add-dir", "-a", "--ask-for-approval":
		return true
	}
	return false
}

func usesInteractiveCodex(arguments []string) bool {
	command, _ := codexCommand(arguments)
	switch command {
	case "agents", "exec", "e", "review", "login", "logout", "mcp", "plugin", "app-server", "remote-control", "completion", "update", "doctor", "sandbox", "debug", "apply", "a", "queue", "archive", "delete", "migrate-rollouts", "unarchive", "cloud", "exec-server", "features", "help", "tcp-tunnel":
		return false
	}
	return true
}

// finishLaunch runs after the agent exits: it installs a finished isolated login, shares
// refreshed credentials, releases the generation lease and retires unused generations.
func finishLaunch(ctx base.LaunchContext, home, loginHome string, lease *flock.Flock) error {
	var loginErr error
	if loginHome != "" {
		loginErr = finishIsolatedLoginHome(loginHome, home)
	}
	result := errors.Join(loginErr, finishAuthLaunch(home, ctx.Arguments), reconcileMCPAuthHome(ctx.ProjectRoot, home), lease.Unlock())
	// Busy generations retain their leases and loaded threads, so retirement can make progress on later launches.
	_ = retireGenerations(ctx.ProjectRoot, ctx.Mode.Name())
	return result
}

func stagedRoot(ctx base.StageContext) (string, error) {
	if home := ctx.StagedRoots[base.Codex]; home != "" {
		return home, nil
	}
	return CurrentHome(ctx.ProjectRoot, ctx.Mode.Name())
}

const controlSocketSuffix = "/app-server-control/app-server-control.sock"

func maxSunLen() int {
	switch runtime.GOOS {
	case "linux":
		return 108
	case "windows":
		return 0 // legacy staged homes are not shortened with symlinks on Windows
	default:
		return 104
	}
}

func isShortHomeSymlink(dir string) (string, bool) {
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", false
	}
	target, err := os.Readlink(dir)
	if err != nil {
		return "", false
	}
	return target, true
}

func needsShortHome(dir string) bool {
	limit := maxSunLen()
	if limit == 0 {
		return false
	}
	if _, ok := isShortHomeSymlink(dir); ok {
		return true
	}
	resolved := canonicalOrProjected(dir)
	return len(resolved)+len(controlSocketSuffix) >= limit
}

func canonicalOrProjected(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	curr := abs
	var suffix []string
	for {
		if fi, err := os.Stat(curr); err == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		suffix = append([]string{filepath.Base(curr)}, suffix...)
		curr = parent
	}
	if resolved, err := filepath.EvalSymlinks(curr); err == nil && resolved != "" {
		curr = resolved
	}
	return filepath.Join(append([]string{curr}, suffix...)...)
}

func shortStagingCodexHome(projectRoot, modeName string) (string, error) {
	hash, err := paths.ProjectPathHash(projectRoot)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		modeHash := sha256.Sum256([]byte(hash + "\x00" + modeName))
		return filepath.Join(os.TempDir(), "a", hex.EncodeToString(modeHash[:4])), nil
	}
	modeComp := paths.ModePathComponent(modeName)
	if len(modeComp) > 10 {
		sum := sha256.Sum256([]byte(modeName))
		modeComp = hex.EncodeToString(sum[:4])
	}
	return filepath.Join(HomesBase(), hash, modeComp), nil
}

// HomesBase is the directory that holds the Codex homes of every project.
//
// Codex binds a Unix socket at <home>/app-server-control/app-server-control.sock,
// and a socket path is limited to about 104 (macOS) or 108 (Linux) bytes, so a
// home must sit at a short path. With AGENTPACK_STAGING_ROOT set, the homes go
// under it (<root>/ap-c) whenever a generation there still leaves room for the
// socket. Otherwise, and when the configured root is too long, they go under
// /var/tmp/ap-c, the one place agentpack writes outside its configured roots.
func HomesBase() string {
	if root := os.Getenv("AGENTPACK_STAGING_ROOT"); root != "" {
		candidate := filepath.Join(canonicalOrProjected(root), "ap-c")
		// <base>/<16 hex project>/<mode, at most 10>/g-<10 digits> + socket
		longest := len(candidate) + 1 + 16 + 1 + 10 + 1 + 12 + len(controlSocketSuffix)
		if limit := maxSunLen(); limit == 0 || longest < limit {
			return candidate
		}
	}
	baseDir := "/var/tmp"
	if fi, err := os.Stat(baseDir); err != nil || !fi.IsDir() {
		baseDir = os.TempDir()
	}
	if resolved, err := filepath.EvalSymlinks(baseDir); err == nil && resolved != "" {
		baseDir = resolved
	}
	return filepath.Join(baseDir, "ap-c")
}

// ProjectHomes is the directory that holds one project's Codex homes, for
// every mode.
func ProjectHomes(projectRoot string) (string, error) {
	hash, err := paths.ProjectPathHash(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(HomesBase(), hash), nil
}

func resetPaths(ctx base.StageContext) ([]string, error) {
	if ctx.StagedRoots[base.Codex] != "" {
		return nil, nil // the new generation is private and already empty
	}
	root, err := stagedRoot(ctx)
	if err != nil {
		return nil, err
	}
	result := []string{root}
	if target, ok := isShortHomeSymlink(root); ok {
		result = append(result, target)
	}
	if needsShortHome(root) {
		if shortDir, err := shortStagingCodexHome(ctx.ProjectRoot, ctx.Mode.Name()); err == nil {
			result = append(result, shortDir)
		}
	}
	sort.Strings(result)
	return slices.Compact(result), nil
}

func preReset(ctx base.StageContext) error {
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	if err := recoverHistory(ctx.ProjectRoot, ctx.Mode.Name()); err != nil {
		return err
	}
	if err := preserveAuth(root); err != nil {
		return err
	}
	return recoverMCPAuth(ctx.ProjectRoot, ctx.Mode.Name())
}

func prepare(ctx base.StageContext) error {
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	targetDir := root
	if ctx.StagedRoots[base.Codex] != "" {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return err
		}
	} else if needsShortHome(root) {
		shortDir, err := shortStagingCodexHome(ctx.ProjectRoot, ctx.Mode.Name())
		if err != nil {
			return err
		}
		targetDir = shortDir
		if err := os.MkdirAll(shortDir, 0o755); err != nil {
			return err
		}
		if err := ensureSymlink(shortDir, root); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return err
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		native := filepath.Join(home, ".codex")
		if err := base.CopySelectedEntries(native, targetDir, []string{"config.toml", "hooks.json", "skills", "themes"}); err != nil {
			return err
		}
		if err := prepareAuth(native, targetDir); err != nil {
			return err
		}
	}
	if err := forceAuthFileStore(targetDir); err != nil {
		return err
	}
	if err := prepareMCPAuth(ctx.ProjectRoot, targetDir); err != nil {
		return err
	}
	if native, ok := nativeHome(); ok {
		if err := prepareHistory(targetDir, native); err != nil {
			return err
		}
	}
	if !keepAttribution() {
		if err := updateConfig(filepath.Join(targetDir, "config.toml"), func(config map[string]any) { delete(config, "commit_attribution") }); err != nil {
			return err
		}
	}
	return nil
}

func ensureSymlink(target, link string) error {
	if info, err := os.Lstat(link); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			curr, _ := os.Readlink(link)
			if curr == target {
				return nil
			}
			_ = os.Remove(link)
		} else {
			_ = os.RemoveAll(link)
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, link)
}

func planMCP(entries mcp.Entries, ctx base.StageContext) (base.MCPProjection, error) {
	root, err := stagedRoot(ctx)
	if err != nil {
		return base.MCPProjection{}, err
	}
	seedPath := filepath.Join(root, "config.toml")
	if ctx.StagedRoots[base.Codex] == "" {
		if home, err := os.UserHomeDir(); err == nil {
			seedPath = filepath.Join(home, ".codex", "config.toml")
		}
	}
	seed, err := base.ReadMCPSeed(seedPath)
	if err != nil {
		return base.MCPProjection{}, err
	}
	inputPath := seedPath
	if ctx.StagedRoots[base.Codex] != "" {
		if home, err := os.UserHomeDir(); err == nil {
			inputPath = filepath.Join(home, ".codex", "config.toml")
		}
	}
	input, err := base.ReadMCPInput(inputPath)
	if err != nil {
		return base.MCPProjection{}, err
	}
	data, warnings, shadowed, err := MergeMCPData(seed, entries)
	if err != nil {
		return base.MCPProjection{}, err
	}
	servers := base.ProjectedMCPServers(entries)
	for index := range servers {
		if slices.Contains(shadowed, servers[index].Name) {
			servers[index].Winner = "user"
		}
	}
	base.MarkMCPWarnings(servers, warnings)
	digest, err := base.MCPTOMLScopeDigest(data, "mcp_servers")
	if err != nil {
		return base.MCPProjection{}, err
	}
	return base.MCPProjection{Files: []base.MCPProjectedFile{{Path: filepath.Join(root, "config.toml"), Contents: data, Scope: "mcp_servers", ScopeDigest: digest, MergeOrder: []string{"user config (existing names win)", "plugin definitions", "agentpack.toml", ".agents/mcp.json"}}}, Servers: servers, Warnings: warnings, Inputs: []base.MCPProjectedInput{input}}, nil
}

func injectGuidance(blob string, ctx base.StageContext) error {
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	return base.WriteGuidance(filepath.Join(root, "AGENTS.md"), blob)
}

func verify(ctx base.StageContext) error {
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("codex home staging missing %s", root)
	}
	if native, ok := nativeHome(); ok {
		if err := verifyHistory(root, native); err != nil {
			return err
		}
	}
	if err := verifyAuth(root); err != nil {
		return err
	}
	return verifyMCPAuth(ctx.ProjectRoot, root)
}

func keepAttribution() bool {
	switch os.Getenv("AGENTPACK_KEEP_ATTRIBUTION") {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// sessionNotify is a -c override that makes a terminal Codex report each finished turn to the
// agentpack worker running it. Codex runs one notify program, so a notify the user configured is
// chained after the report and still receives Codex's payload.
func sessionNotify(home, executable, sessionID string) (string, error) {
	command := []string{executable, "hook-exec", "session-state", "--target", "codex", "--session", sessionID, "--state", "idle"}
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	var config map[string]any
	if len(data) > 0 {
		if err := toml.Unmarshal(data, &config); err != nil {
			return "", fmt.Errorf("parse staged Codex config: %w", err)
		}
	}
	// A notify Codex itself would reject is left alone rather than chained.
	if list, ok := config["notify"].([]any); ok && len(list) > 0 {
		user := make([]string, 0, len(list))
		for _, item := range list {
			if text, ok := item.(string); ok {
				user = append(user, text)
			}
		}
		if len(user) == len(list) {
			command = append(append(command, "--"), user...)
		}
	}
	// A JSON string array is also a TOML inline array.
	encoded, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	return "notify=" + string(encoded), nil
}
