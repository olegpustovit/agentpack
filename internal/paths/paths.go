package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/olegpustovit/agentpack/internal/slug"
)

const (
	LockfileName              = "pack.lock"
	ManifestName              = "agentpack.toml"
	StagedAgentpackBundleName = "agentpack-bundle"
	DotAgentsDir              = ".agents"
)

var ErrProjectNotFound = errors.New("no agentpack.toml or pack.lock found")

func ProjectDotAgentsDir(projectRoot string) string { return filepath.Join(projectRoot, DotAgentsDir) }
func ManifestPath(projectRoot string) string        { return filepath.Join(projectRoot, ManifestName) }
func LockPath(projectRoot string) string            { return filepath.Join(projectRoot, LockfileName) }

// UserAgentpackHome implements the documented AGENTPACK_HOME/XDG/LOCALAPPDATA
// precedence without depending on a platform directory package.
func UserAgentpackHome() (string, error) {
	if value := strings.TrimSpace(os.Getenv("AGENTPACK_HOME")); value != "" {
		return value, nil
	}
	if runtime.GOOS == "windows" {
		if value := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); value != "" {
			return filepath.Join(value, "agentpack"), nil
		}
		// LOCALAPPDATA can be missing (minimal shells, services); derive it.
		if value := strings.TrimSpace(os.Getenv("USERPROFILE")); value != "" {
			return filepath.Join(value, "AppData", "Local", "agentpack"), nil
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "AppData", "Local", "agentpack"), nil
		}
		return "", errors.New("resolve agentpack home: LOCALAPPDATA and USERPROFILE are empty")
	}
	if value := os.Getenv("XDG_DATA_HOME"); value != "" {
		return filepath.Join(value, "agentpack"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve agentpack home: %w", err)
	}
	return filepath.Join(home, ".local", "share", "agentpack"), nil
}

func EnsureUserAgentpackLayout() (string, error) {
	root, err := UserAgentpackHome()
	if err != nil {
		return "", err
	}
	for _, name := range []string{"cache", "local", "projects", "environments"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", fmt.Errorf("create %s: %w", path, err)
		}
	}
	return root, nil
}

// EnvironmentsRoot holds portable external definition directories.
func EnvironmentsRoot() (string, error) { return AgentpackHomePath("environments") }

// AgentpackHomePath resolves path components under the user's agentpack home.
func AgentpackHomePath(parts ...string) (string, error) {
	root, err := UserAgentpackHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{root}, parts...)...), nil
}

func CacheDir() (string, error)          { return AgentpackHomePath("cache") }
func CacheDBPath() (string, error)       { return AgentpackHomePath("cache", "index.db") }
func LocalRegistryRoot() (string, error) { return AgentpackHomePath("local") }

func LocalMirrorPathFromShorthand(spec string) (string, error) {
	return AgentpackHomePath("local", filepath.FromSlash(spec))
}

func ProjectStateDir(projectRoot string) (string, error) {
	hash, err := ProjectPathHash(projectRoot)
	if err != nil {
		return "", err
	}
	return AgentpackHomePath("projects", hash)
}

// ProjectStateFile resolves a path under the project's durable state directory.
func ProjectStateFile(projectRoot string, parts ...string) (string, error) {
	root, err := ProjectStateDir(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{root}, parts...)...), nil
}

func ProxyLogDir(projectRoot string) (string, error) {
	if value := strings.TrimSpace(os.Getenv("AGENTPACK_PROXY_LOG_DIR")); value != "" {
		return value, nil
	}
	return ProjectStateFile(projectRoot, "proxy-logs")
}

func FindProjectRoot(start string) (string, error) {
	directory, err := canonical(start)
	if err != nil {
		return "", err
	}
	for {
		if regularFile(filepath.Join(directory, ManifestName)) || regularFile(filepath.Join(directory, LockfileName)) {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", fmt.Errorf("%w from %s", ErrProjectNotFound, start)
		}
		directory = parent
	}
}

func ResolveProjectRoot(explicit string) (string, error) {
	if explicit == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
		return FindProjectRoot(cwd)
	}
	root, err := canonical(explicit)
	if err != nil {
		return "", err
	}
	if !regularFile(filepath.Join(root, ManifestName)) && !regularFile(filepath.Join(root, LockfileName)) {
		return "", fmt.Errorf("%w from %s", ErrProjectNotFound, root)
	}
	return root, nil
}

func ResolveProjectRootOrCWD(explicit string) (string, error) {
	if explicit != "" {
		return canonical(explicit)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get current directory: %w", err)
	}
	root, err := FindProjectRoot(cwd)
	if err == nil {
		return root, nil
	}
	if !errors.Is(err, ErrProjectNotFound) {
		return "", err
	}
	return canonical(cwd)
}

func ProjectPathHash(projectRoot string) (string, error) {
	root, err := canonical(projectRoot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:8]), nil
}

func ModePathComponent(modeName string) string {
	if modeName == "default" {
		return "default"
	}
	name := slug.DashedLower(modeName)
	if name == "" {
		name = "mode"
	}
	sum := sha256.Sum256([]byte(modeName))
	return name + "-" + hex.EncodeToString(sum[:4])
}

// StagedManifestPath records staged content per generation, with the legacy
// project-state location used only for staging built by older releases.
func StagedManifestPath(projectRoot, modeName string) (string, error) {
	legacy, err := stagingModePath(projectRoot, modeName, true)
	if err != nil {
		return "", err
	}
	root, err := selectStagingRoot(legacy)
	if err != nil {
		return "", err
	}
	if root != legacy {
		return filepath.Join(root, ".staged.json"), nil
	}
	return ProjectStateFile(projectRoot, "staged-"+ModePathComponent(modeName)+".json")
}

func LaunchSyncStatePath(projectRoot, modeName, key string) (string, error) {
	return ProjectStateFile(projectRoot, "launch-sync-"+ModePathComponent(modeName)+"."+ModePathComponent(key)+".state")
}

func stagingRootBase(projectRoot string) (string, error) {
	if value, ok := os.LookupEnv("AGENTPACK_STAGING_ROOT"); ok {
		hash, err := ProjectPathHash(projectRoot)
		if err != nil {
			return "", err
		}
		return filepath.Join(value, "projects", hash), nil
	}
	hash, err := ProjectPathHash(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(os.TempDir(), "agentpack-"+hash), nil
}

func StagingSubdir(projectRoot, modeName, segment string) (string, error) {
	root, err := StagingRootForMode(projectRoot, modeName)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, segment), nil
}

func StagingPluginsDirForMode(root, mode string) (string, error) {
	return StagingSubdir(root, mode, "plugins")
}

// StagingPluginRootsDirForMode holds whole copies of the plugins whose MCP
// servers run from their own directory, one per package, shared by every
// harness of the mode.
func StagingPluginRootsDirForMode(root, mode string) (string, error) {
	return StagingSubdir(root, mode, "plugin-roots")
}

// PluginDataDir is the durable directory a plugin's ${CLAUDE_PLUGIN_DATA}
// names for a project; it survives staging rebuilds and package updates.
func PluginDataDir(projectRoot, module string) (string, error) {
	state, err := ProjectStateDir(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "plugin-data", module), nil
}

func StagingBundleDir(projectRoot, modeName, segment string) (string, error) {
	root, err := StagingSubdir(projectRoot, modeName, segment)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, StagedAgentpackBundleName), nil
}

// MCPCredentialIndexName lists the gateway's stored MCP credentials (no secrets) for staging.
const MCPCredentialIndexName = "mcp-credentials.json"

func MCPCredentialIndexPath() (string, error) { return AgentpackHomePath(MCPCredentialIndexName) }

// ProjectMCPLoginsPath holds the login each remote MCP server uses in this project.
func ProjectMCPLoginsPath(projectRoot string) (string, error) {
	return ProjectStateFile(projectRoot, "mcp-logins.json")
}

// ProjectMCPPluginLoginsPath holds this project's permissions for plugin MCP logins.
func ProjectMCPPluginLoginsPath(projectRoot string) (string, error) {
	return ProjectStateFile(projectRoot, "mcp-plugin-logins.json")
}

func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	return filepath.Clean(resolved), nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// projectMarker is written into each directory agentpack creates for one
// project outside $AGENTPACK_HOME. It holds the project's path, so that the
// directory can be recognised as left over once the project is gone.
const projectMarker = ".agentpack-project"

// StagingProjectDir is the directory that holds a project's staging for every
// mode. The second result is the directory such directories are created in
// when no AGENTPACK_STAGING_ROOT is set, and empty when one is.
func StagingProjectDir(projectRoot string) (string, string, error) {
	directory, err := stagingRootBase(projectRoot)
	if err != nil {
		return "", "", err
	}
	if _, configured := os.LookupEnv("AGENTPACK_STAGING_ROOT"); configured {
		return directory, "", nil
	}
	return directory, filepath.Dir(directory), nil
}

// MarkProjectDirectory records which project a directory belongs to.
func MarkProjectDirectory(directory, projectRoot string) error {
	root, err := canonical(projectRoot)
	if err != nil {
		return err
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return nil
	}
	marker := filepath.Join(directory, projectMarker)
	if data, err := os.ReadFile(marker); err == nil && string(data) == root+"\n" {
		return nil
	}
	return os.WriteFile(marker, []byte(root+"\n"), 0o644)
}

// OrphanedProjectDirectories lists the directories directly under parent that
// carry a project marker for a project directory that no longer exists. A
// directory without a marker is never listed.
func OrphanedProjectDirectories(parent string) []string {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	var orphans []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		directory := filepath.Join(parent, entry.Name())
		data, err := os.ReadFile(filepath.Join(directory, projectMarker))
		if err != nil {
			continue
		}
		root := strings.TrimSpace(string(data))
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		if _, err := os.Stat(root); os.IsNotExist(err) {
			orphans = append(orphans, directory)
		}
	}
	return orphans
}

// WriteAtomic replaces path with data in one rename, so a reader or a crash
// sees either the old file or the new one, never a truncated mix.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	_, err = temporary.Write(data)
	if err = errors.Join(err, temporary.Chmod(mode), temporary.Sync(), temporary.Close()); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
