package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/omp"
	"github.com/olegpustovit/agentpack/internal/harness/opencode"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
)

const stagingLayoutVersion = "5"

type launchState struct {
	Digest string `json:"digest"`
}

func ComputeLaunchDigest(projectRoot, workspace string, effective mode.Effective, target *base.Target) (string, error) {
	hash := sha256.New()
	hash.Write([]byte("staging_layout_version\x00" + stagingLayoutVersion))
	writeDigestFile(hash, "manifest", paths.ManifestPath(projectRoot))
	writeDigestFile(hash, "lock", paths.LockPath(projectRoot))
	hash.Write([]byte("dot_agents\x00"))
	dot, err := hashDotAgents(paths.ProjectDotAgentsDir(workspace))
	if err != nil {
		return "", err
	}
	hash.Write(dot)
	hash.Write([]byte("staging_root\x00" + os.Getenv("AGENTPACK_STAGING_ROOT")))
	stagingRoot, err := paths.StagingRootForMode(projectRoot, effective.Name())
	if err != nil {
		return "", err
	}
	hash.Write([]byte("staging_generation\x00" + stagingRoot))
	hash.Write([]byte("mode\x00" + effective.FingerprintMaterial()))
	name := "__none__"
	if target != nil {
		name = string(*target)
	}
	hash.Write([]byte("target\x00" + name))
	if target == nil || *target == base.OMP {
		inputs, err := fingerprintInputs(omp.ConfigInputs(workspace))
		if err != nil {
			return "", err
		}
		hash.Write([]byte("omp_native_inputs\x00" + inputs))
	}

	if target != nil && *target == base.Cursor {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		skills, err := fingerprintInputs([]string{filepath.Join(home, ".cursor", "skills")})
		if err != nil {
			return "", err
		}
		hash.Write([]byte("cursor_user_skills\x00" + skills))
	}
	if target == nil || *target == base.OpenCode {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		globalConfig := os.Getenv("XDG_CONFIG_HOME")
		if globalConfig == "" {
			globalConfig = filepath.Join(home, ".config")
		}
		globalConfig = filepath.Join(globalConfig, "opencode")
		configDir := os.Getenv("OPENCODE_CONFIG_DIR")
		if configDir == "" {
			configDir = globalConfig
		}
		configDir, err = filepath.Abs(configDir)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "opencode_config_dir\x00%s", configDir)
		entries, readErr := os.ReadDir(configDir)
		if readErr != nil && !os.IsNotExist(readErr) {
			return "", readErr
		}
		for _, entry := range entries {
			fmt.Fprintf(hash, "\x00custom_entry\x00%s\x00%s", entry.Name(), entry.Type())
		}
		for _, name := range []string{"config.json", "opencode.json", "opencode.jsonc"} {
			writeDigestFile(hash, "opencode_user_"+name, filepath.Join(configDir, name))
			writeDigestFile(hash, "opencode_global_"+name, filepath.Join(globalConfig, name))
			writeDigestFile(hash, "opencode_seed_"+name, filepath.Join(home, ".config", "opencode", name))
		}
		writeDigestFile(hash, "opencode_cli_config", filepath.Join(configDir, "cli.json"))
		for _, name := range []string{"opencode.json", "opencode.jsonc"} {
			writeDigestFile(hash, "opencode_home_"+name, filepath.Join(home, ".opencode", name))
		}
		if explicit := os.Getenv("OPENCODE_CONFIG"); explicit != "" {
			if !filepath.IsAbs(explicit) {
				explicit = filepath.Join(workspace, explicit)
			}
			writeDigestFile(hash, "opencode_explicit_config\x00"+explicit, explicit)
		}
		hash.Write([]byte("opencode_inline_config\x00" + os.Getenv("OPENCODE_CONFIG_CONTENT")))
		hash.Write([]byte("opencode_disable_project_config\x00" + os.Getenv("OPENCODE_DISABLE_PROJECT_CONFIG")))
		for directory := workspace; ; directory = filepath.Dir(directory) {
			for _, name := range []string{"opencode.json", "opencode.jsonc", ".opencode/opencode.json", ".opencode/opencode.jsonc"} {
				writeDigestFile(hash, "opencode_project\x00"+directory+"\x00"+name, filepath.Join(directory, filepath.FromSlash(name)))
			}
			if _, statErr := os.Stat(filepath.Join(directory, ".git")); statErr == nil || filepath.Dir(directory) == directory {
				break
			}
		}
		for _, name := range []string{"plugin", "plugins"} {
			info, statErr := os.Stat(filepath.Join(configDir, name))
			fmt.Fprintf(hash, "\x00%s\x00%t", name, statErr == nil && info.IsDir())
		}
		for _, name := range []string{"skill", "skills", "agent", "agents", "command", "commands", "mode", "modes"} {
			if err := writeAssetNames(hash, filepath.Join(configDir, name)); err != nil {
				return "", err
			}
		}
		if target != nil {
			nativeInputs, err := opencode.NativeConfigFingerprint(projectRoot, workspace)
			if err != nil {
				return "", err
			}
			hash.Write([]byte("opencode_native_inputs\x00" + nativeInputs))
		}
		binary, err := base.ResolveBinary("OPENCODE_PATH", "opencode")
		if err == nil {
			if resolved, resolveErr := filepath.EvalSymlinks(binary); resolveErr == nil {
				binary = resolved
			}
			if info, statErr := os.Stat(binary); statErr == nil {
				fmt.Fprintf(hash, "opencode_binary\x00%s\x00%d\x00%d", binary, info.Size(), info.ModTime().UnixNano())
			}
		}
	}
	hash.Write([]byte("workspace\x00"))
	hash.Write([]byte(workspace))
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeAssetNames(hash interface{ Write([]byte) (int, error) }, root string) error {
	resolved, err := filepath.EvalSymlinks(root)
	if os.IsNotExist(err) {
		fmt.Fprintf(hash, "custom_assets\x00%s\x00absent", root)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(hash, "custom_assets\x00%s\x00%s", root, resolved)
	return filepath.WalkDir(resolved, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(resolved, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "\x00%s\x00%s\x00%d\x00%d", relative, info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
}
func writeDigestFile(hash interface{ Write([]byte) (int, error) }, label, path string) {
	hash.Write([]byte(label + "\x00"))
	data, err := os.ReadFile(path)
	if err != nil {
		hash.Write([]byte("__missing__"))
		return
	}
	hash.Write(data)
}
func hashDotAgents(root string) ([]byte, error) {
	info, err := os.Stat(root)
	if os.IsNotExist(err) || err == nil && !info.IsDir() {
		return []byte("__dot_agents_absent__"), nil
	}
	if err != nil {
		return nil, err
	}
	// Antigravity's launch puts the staged bundle here (a link, or a copy where
	// links are refused). It is Agentpack's own output, not an input.
	overlay := filepath.Join(root, "plugins", paths.StagedAgentpackBundleName)
	var files []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == overlay {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err == nil && info.Mode().IsRegular() {
				files = append(files, path)
			}
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
		info, _ := os.Stat(path)
		size := uint64(info.Size())
		var encoded [8]byte
		for index := 0; index < 8; index++ {
			encoded[index] = byte(size >> (8 * index))
		}
		hash.Write(encoded[:])
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
	return writeDigestState(path, digest)
}
func readMaterializationDigest(projectRoot, modeName string) (string, bool, error) {
	path, err := paths.ProjectStateFile(projectRoot, "materialized-"+paths.ModePathComponent(modeName)+".json")
	if err != nil {
		return "", false, err
	}
	return readDigestState(path)
}
func writeMaterializationDigest(projectRoot, modeName, digest string) error {
	path, err := paths.ProjectStateFile(projectRoot, "materialized-"+paths.ModePathComponent(modeName)+".json")
	if err != nil {
		return err
	}
	return writeDigestState(path, digest)
}
func computeMaterializationDigest(definition, workspace string, effective mode.Effective) (string, error) {
	inputs, err := ComputeLaunchDigest(definition, workspace, effective, nil)
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	roots := workspaceConfigInputs(workspace, home)
	for _, target := range base.AllTargets() {
		roots = append(roots, inheritedConfigPaths(&target, workspace)...)
	}
	ambient, err := fingerprintInputs(roots)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(inputs + "\x00ambient\x00" + ambient + "\x00attribution\x00" + os.Getenv("AGENTPACK_KEEP_ATTRIBUTION")))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
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
func writeDigestState(path, digest string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(launchState{Digest: digest})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".sync-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
