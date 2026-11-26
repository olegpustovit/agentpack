package sync

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/olegpustovit/agentpack/internal/cache"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/cursor"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
)

const stagingLayoutVersion = "6"

type launchState struct {
	Digest string `json:"digest"`
	// Root is the staging generation a launch digest was computed for.
	Root string `json:"root,omitempty"`
}

// Inputs is what staging reads besides the package cache, gathered once per
// check. Every "did the inputs change?" identity is a digest of part of it:
//
//	launch        Lock, Manifest, DotAgents, Context, PerTarget[target], cache metadata, Binary
//	materialized  Lock, Manifest, DotAgents, Context, PerTarget[each target], ambient configuration
//	synced        materialized and Binary
//	environment   Content.Selected
//	preflight     Lock (lock digest), Content.Source (source identity), generation (generation ID)
//
// The file digests name host paths; Content does not, so the environment and
// preflight identities compare across checkouts and machines.
type Inputs struct {
	Lock, Manifest string // sha256 of the file bytes, or __missing__
	DotAgents      string
	// Context is the workspace, mode, staging generation, selected target, MCP credential index and the project's MCP login picks.
	Context string
	// PerTarget holds Harness.LaunchInputs for the selected harness, or every harness when none is.
	PerTarget map[base.Target]string
	Binary    string
	Content   Content
}

// lockDigest is the lockFile digest of the pack.lock the caller already read.
func readInputs(projectRoot, workspace string, effective mode.Effective, target *base.Target, lockDigest string) (Inputs, error) {
	in := Inputs{Lock: lockDigest, Manifest: fileDigestOrMissing(paths.ManifestPath(projectRoot)), PerTarget: map[base.Target]string{}, Binary: executableStamp()}
	dot, err := hashDotAgents(paths.ProjectDotAgentsDir(workspace))
	if err != nil {
		return Inputs{}, err
	}
	in.DotAgents = hex.EncodeToString(dot)
	stagingRoot, err := paths.StagingRootForMode(projectRoot, effective.Name())
	if err != nil {
		return Inputs{}, err
	}
	context := sha256.New()
	context.Write([]byte("staging_layout_version\x00" + stagingLayoutVersion))
	if index, err := paths.MCPCredentialIndexPath(); err == nil {
		base.DigestFile(context, "mcp_credentials", index)
	}
	if logins, err := paths.ProjectMCPLoginsPath(projectRoot); err == nil {
		base.DigestFile(context, "mcp_logins", logins)
	}
	if logins, err := paths.ProjectMCPPluginLoginsPath(projectRoot); err == nil {
		base.DigestFile(context, "mcp_plugin_logins", logins)
	}
	name := "__none__"
	if target != nil {
		name = string(*target)
	}
	fmt.Fprintf(context, "staging_root\x00%s\x00staging_generation\x00%s\x00mode\x00%s\x00target\x00%s\x00workspace\x00%s",
		os.Getenv("AGENTPACK_STAGING_ROOT"), stagingRoot, effective.FingerprintMaterial(), name, workspace)
	in.Context = hex.EncodeToString(context.Sum(nil))
	targets := base.AllTargets()
	if target != nil {
		targets = []base.Target{*target}
	}
	for _, each := range targets {
		entry, _ := registry.ByTarget(each)
		if entry.Harness.LaunchInputs == nil {
			continue
		}
		if in.PerTarget[each], err = entry.Harness.LaunchInputs(projectRoot, workspace); err != nil {
			return Inputs{}, err
		}
	}
	return in, nil
}

// staged digests what a rebuild stages from.
func (in Inputs) staged() string {
	hash := sha256.New()
	fmt.Fprintf(hash, "lock\x00%s\x00manifest\x00%s\x00dot_agents\x00%s\x00context\x00%s", in.Lock, in.Manifest, in.DotAgents, in.Context)
	targets := make([]string, 0, len(in.PerTarget))
	for target := range in.PerTarget {
		targets = append(targets, string(target))
	}
	sort.Strings(targets)
	for _, target := range targets {
		fmt.Fprintf(hash, "\x00%s\x00%s", target, in.PerTarget[base.Target(target)])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// launch extends the staged inputs with the cache metadata fingerprint, so a
// visible change to a verified cache entry ends the fast path and sends the
// launch through a full sync, which hashes the content, and with the binary,
// since a new agentpack may stage differently.
func (in Inputs) launch(lock lockfile.PackLock) (string, error) {
	fingerprint, err := cache.StatFingerprint(lock)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(in.staged() + "\x00cache\x00" + fingerprint + "\x00binary\x00" + in.Binary))
	return hex.EncodeToString(sum[:]), nil
}

// materialized extends the staged inputs with the user and workspace
// configuration the staged harnesses inherit.
func (in Inputs) materialized(workspace string, target *base.Target) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	roots := workspaceConfigInputs(workspace, home)
	targets := base.AllTargets()
	if target != nil {
		targets = []base.Target{*target}
	}
	for _, each := range targets {
		roots = append(roots, inheritedConfigPaths(&each, workspace)...)
	}
	ambient, err := base.FingerprintFiles(roots)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(in.staged() + "\x00ambient\x00" + ambient + "\x00attribution\x00" + os.Getenv("AGENTPACK_KEEP_ATTRIBUTION")))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func computeMaterializationDigest(definition, workspace string, effective mode.Effective, target *base.Target, lockDigest string) (string, error) {
	in, err := readInputs(definition, workspace, effective, target, lockDigest)
	if err != nil {
		return "", err
	}
	return in.materialized(workspace, target)
}

// synced is the materialized digest of a full sync with the binary that built it.
func (in Inputs) synced(materialized string) string {
	return materialized + "\x00" + in.Binary
}

const missingDigest = "__missing__"

func bytesDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func fileDigestOrMissing(path string) string {
	if data, err := os.ReadFile(path); err == nil {
		return bytesDigest(data)
	}
	return missingDigest
}

func hashDotAgents(root string) ([]byte, error) {
	info, err := os.Stat(root)
	if os.IsNotExist(err) || err == nil && !info.IsDir() {
		return []byte("__dot_agents_absent__"), nil
	}
	if err != nil {
		return nil, err
	}
	sizes := map[string]int64{}
	var files []string
	if err := walkDotAgents(root, func(_, path string, entry os.DirEntry) error {
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			files = append(files, path)
			sizes[path] = info.Size()
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return []byte("__dot_agents_absent__"), nil
	}
	sort.Strings(files)
	hash := sha256.New()
	for _, path := range files {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		hash.Write([]byte(relative))
		hash.Write([]byte{0})
		hash.Write(binary.LittleEndian.AppendUint64(nil, uint64(sizes[path])))
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		hash.Write(data)
	}
	return hash.Sum(nil), nil
}

func ReadLaunchDigest(projectRoot, modeName string, target base.Target) (string, bool, error) {
	path, err := paths.LaunchSyncStatePath(projectRoot, modeName, string(target))
	if err != nil {
		return "", false, err
	}
	return readDigestState(path)
}
func WriteLaunchDigest(projectRoot, modeName string, target base.Target, digest string) error {
	path, err := paths.LaunchSyncStatePath(projectRoot, modeName, string(target))
	if err != nil {
		return err
	}
	root, err := paths.StagingRootForMode(projectRoot, modeName)
	if err != nil {
		return err
	}
	return writeDigestState(path, digest, root)
}

// selectLaunchGeneration returns a harness to the generation its last launch
// built. A launch stages only its own harness, so another harness's rebuild
// publishes a generation without this one; following it would rebuild on every
// switch between agents. The digest still decides whether it may be reused.
//
// Overlay harnesses stay on the published path, and so do launches after
// Cursor: the rebuild is what keeps its workspace links current.
func selectLaunchGeneration(projectRoot, modeName string, target base.Target) error {
	if target.UsesWorkspaceOverlay() {
		return nil
	}
	manifest, err := cursor.OverlayManifestPath(projectRoot)
	if err != nil {
		return err
	}
	if info, err := os.Stat(manifest); err == nil && info.Size() != 0 {
		return nil
	}
	path, err := paths.LaunchSyncStatePath(projectRoot, modeName, string(target))
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state launchState
	if json.Unmarshal(data, &state) != nil || state.Root == "" {
		return nil
	}
	return paths.SelectStagingGeneration(projectRoot, modeName, state.Root)
}

// materializationStatePath is per target: a launch rebuilds one harness and
// must not claim that every other harness was built from current inputs.
func materializationStatePath(projectRoot, modeName string, target *base.Target) (string, error) {
	name := "materialized-" + paths.ModePathComponent(modeName)
	if target != nil {
		name += "-" + string(*target)
	}
	return paths.ProjectStateFile(projectRoot, name+".json")
}

// recordedGenerations lists the staging generations this mode's launch and
// sync records point at. Each harness returns to its own on the next launch,
// so reclaiming one would make switching agents rebuild.
func recordedGenerations(projectRoot, modeName string) []string {
	pattern, err := paths.ProjectStateFile(projectRoot, "launch-sync-"+paths.ModePathComponent(modeName)+".*.state")
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(pattern)
	if synced, err := syncedStatePath(projectRoot, modeName); err == nil {
		files = append(files, synced)
	}
	var roots []string
	for _, file := range files {
		var state launchState
		if data, err := os.ReadFile(file); err == nil && json.Unmarshal(data, &state) == nil && state.Root != "" {
			roots = append(roots, state.Root)
		}
	}
	return roots
}

func readMaterializationDigest(projectRoot, modeName string, target *base.Target) (string, bool, error) {
	path, err := materializationStatePath(projectRoot, modeName, target)
	if err != nil {
		return "", false, err
	}
	return readDigestState(path)
}
func writeMaterializationDigest(projectRoot, modeName string, target *base.Target, digest string) error {
	path, err := materializationStatePath(projectRoot, modeName, target)
	if err != nil {
		return err
	}
	return writeDigestState(path, digest, "")
}
func readDigestState(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var state launchState
	if err := json.Unmarshal(data, &state); err != nil {
		return "", false, fmt.Errorf("read sync input state %s: %w", path, err)
	}
	return state.Digest, true, nil
}
func writeDigestState(path, digest, root string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(launchState{Digest: digest, Root: root})
	if err != nil {
		return err
	}
	return paths.WriteAtomic(path, data, 0o600)
}

// A plain sync skips the rebuild when its inputs, the staged tree and the
// agentpack binary are all as the last full sync left them. Launch rebuilds
// stage one harness, so they never write this state.
func syncedStatePath(projectRoot, modeName string) (string, error) {
	return paths.ProjectStateFile(projectRoot, "synced-"+paths.ModePathComponent(modeName)+".json")
}

func writeSyncedDigest(projectRoot, modeName, digest string) error {
	path, err := syncedStatePath(projectRoot, modeName)
	if err != nil {
		return err
	}
	// The generation matters too: a launch publishes one with only its own harness.
	root, err := paths.StagingRootForMode(projectRoot, modeName)
	if err != nil {
		return err
	}
	return writeDigestState(path, digest, root)
}

// readSyncedDigest returns the last full sync's digest and the generation it built.
func readSyncedDigest(projectRoot, modeName string) (string, string, bool, error) {
	path, err := syncedStatePath(projectRoot, modeName)
	if err != nil {
		return "", "", false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	var state launchState
	if err := json.Unmarshal(data, &state); err != nil {
		return "", "", false, fmt.Errorf("read sync input state %s: %w", path, err)
	}
	return state.Digest, state.Root, true, nil
}

var executable = os.Executable

// executableStamp changes whenever the agentpack binary does, since a new
// version may stage differently.
func executableStamp() string {
	path, err := executable()
	if err != nil {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s\x00%d\x00%d", path, info.Size(), info.ModTime().UnixNano())
}
