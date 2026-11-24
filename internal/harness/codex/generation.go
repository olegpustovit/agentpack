package codex

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/paths"
)

const generationPrefix = "g-"

func generations(projectRoot, modeName string) (paths.GenerationSet, error) {
	pointer, err := paths.ProjectStateFile(projectRoot, "codex-generations", paths.ModePathComponent(modeName)+".current")
	if err != nil {
		return paths.GenerationSet{}, err
	}
	directory, err := shortStagingCodexHome(projectRoot, modeName)
	if err != nil {
		return paths.GenerationSet{}, err
	}
	if runtime.GOOS == "windows" {
		directory, err = publishedWindowsDirectory(pointer, directory)
		if err != nil {
			return paths.GenerationSet{}, err
		}
	}
	return paths.GenerationSet{Directory: directory, Prefix: generationPrefix, Pointer: pointer, AbsolutePointer: true}, nil
}

// A Windows gateway and an interactive shell can inherit different temp directories.
// Keep the mode's established short directory once the first build publishes it.
func publishedWindowsDirectory(pointer, directory string) (string, error) {
	data, err := os.ReadFile(pointer)
	if os.IsNotExist(err) {
		return directory, nil
	}
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(data))
	parent := filepath.Dir(root)
	if parent == directory || !filepath.IsAbs(root) || root != filepath.Clean(root) || strings.ContainsRune(root, 0) ||
		filepath.Base(parent) != filepath.Base(directory) || filepath.Base(filepath.Dir(parent)) != "a" {
		return directory, nil // the generation set validates the pointer
	}
	suffix, ok := strings.CutPrefix(filepath.Base(root), generationPrefix)
	if _, err := strconv.ParseUint(suffix, 10, 32); !ok || err != nil {
		return directory, nil
	}
	if info, err := os.Lstat(root); err == nil && !info.IsDir() {
		return "", fmt.Errorf("generation must be a directory: %s", root)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if _, err := os.Lstat(parent); !os.IsNotExist(err) {
		if err := paths.EnsureOwnedPrivateDirectory(parent); err != nil {
			return "", err
		}
	}
	return parent, nil
}

func legacyHome(projectRoot, modeName string) (string, error) {
	return paths.StagingSubdir(projectRoot, modeName, "codex-home")
}

// CurrentHome reports the published home. Launchers lease it before use.
func CurrentHome(projectRoot, modeName string) (string, error) {
	set, err := generations(projectRoot, modeName)
	if err != nil {
		return "", err
	}
	home, err := set.Current()
	if home != "" || err != nil {
		return home, err
	}
	return legacyHome(projectRoot, modeName)
}

// LeaseHome pins the published home through the caller's final read or child exit.
func LeaseHome(projectRoot, modeName string) (string, func() error, error) {
	set, err := generations(projectRoot, modeName)
	if err != nil {
		return "", nil, err
	}
	home, lease, err := set.LeaseCurrent()
	if err != nil {
		return "", nil, err
	}
	if lease != nil {
		return home, lease.Unlock, nil
	}
	// The legacy home is part of the selected staging tree and shares its lease.
	home, err = legacyHome(projectRoot, modeName)
	return home, func() error { return nil }, err
}

type generationTransaction struct {
	*paths.Generation
	set paths.GenerationSet
}

func (transaction *generationTransaction) Commit() error {
	if err := transaction.Generation.Commit(); err != nil {
		return err
	}
	transaction.set.Reclaim(canRetireGeneration)
	return nil
}

func beginGeneration(ctx base.StageContext) (base.RebuildTransaction, error) {
	set, err := generations(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" {
		if err := paths.EnsureOwnedPrivateDirectory(set.Directory); err != nil {
			return nil, err
		}
	}
	generation, err := set.Begin()
	if err != nil {
		return nil, err
	}
	home := generation.Root()
	if needsShortHome(home) || windowsSocketPathTooLong(home) {
		return nil, errors.Join(fmt.Errorf("Codex generation path exceeds the local socket limit: %s", home), generation.Abort())
	}
	return &generationTransaction{Generation: generation, set: set}, nil
}

func windowsSocketPathTooLong(home string) bool {
	return runtime.GOOS == "windows" && len(home)+len(controlSocketSuffix) >= 108
}

// publishedGenerationHomes includes previous generations so copied MCP OAuth
// stores can be reconciled even after a process exits during a mode switch.
func publishedGenerationHomes(projectRoot string) ([]string, error) {
	state, err := paths.ProjectStateFile(projectRoot, "codex-generations")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(state)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var homes []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".current") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(state, entry.Name()))
		if err != nil {
			return nil, err
		}
		current := strings.TrimSpace(string(data))
		if !filepath.IsAbs(current) || !strings.HasPrefix(filepath.Base(current), generationPrefix) || strings.ContainsRune(current, 0) {
			return nil, fmt.Errorf("invalid Codex generation pointer for %s", entry.Name())
		}
		set := paths.GenerationSet{Directory: filepath.Dir(current), Prefix: generationPrefix}
		siblings, err := set.Roots()
		if err != nil {
			return nil, err
		}
		homes = append(homes, siblings...)
	}
	slices.Sort(homes)
	return slices.Compact(homes), nil
}

func canRetireGeneration(home string) bool {
	socket := filepath.Join(home, "app-server-control", "app-server-control.sock")
	if _, err := os.Stat(socket); os.IsNotExist(err) {
		return true
	}
	empty, err := daemonHasNoLoadedThreads(socket)
	if err != nil || !empty {
		return false
	}
	binary, err := base.ResolveBinary(base.Codex.Binary())
	if err != nil {
		return false
	}
	command := exec.Command(binary, "app-server", "daemon", "stop")
	command.Env = append(os.Environ(), "CODEX_HOME="+home)
	return command.Run() == nil
}
