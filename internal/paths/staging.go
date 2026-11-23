package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/gofrs/flock"
)

// A command keeps its selected paths through child exit and credential recovery,
// even when another process publishes a replacement generation.
var stagingSelections sync.Map

// stagingLeases holds a shared lock on every generation this process has
// selected, until it exits: launchers and session workers use its files for
// their whole life. ReclaimStagingGenerations removes only unleased ones.
var stagingLeases sync.Map

func leaseStagingGeneration(root string) {
	if _, held := stagingLeases.Load(root); held {
		return
	}
	lease := flock.New(root + ".lease")
	// A lease that cannot be taken also cannot be taken by the reclaimer, which
	// then keeps the generation; nothing is lost by going on without it.
	if lease.RLock() != nil {
		return
	}
	if _, loaded := stagingLeases.LoadOrStore(root, lease); loaded {
		_ = lease.Unlock()
	}
}

func stagingModePath(projectRoot, modeName string) (string, error) {
	root, err := stagingRootBase(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(root, "modes", ModePathComponent(modeName)))
}

func selectedStagingRoot(projectRoot, modeName string) (string, error) {
	legacy, err := stagingModePath(projectRoot, modeName)
	if err != nil {
		return "", err
	}
	if selected, ok := stagingSelections.Load(legacy); ok {
		return selected.(string), nil
	}
	selected, err := readStagingSelection(legacy)
	if err != nil {
		return "", err
	}
	actual, _ := stagingSelections.LoadOrStore(legacy, selected)
	leaseStagingGeneration(actual.(string))
	return actual.(string), nil
}

func readStagingSelection(legacy string) (string, error) {
	data, err := os.ReadFile(legacy + ".current")
	if os.IsNotExist(err) {
		return legacy, nil
	}
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(data))
	if filepath.Base(name) != name || !strings.HasPrefix(name, filepath.Base(legacy)+"-g-") || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("invalid staging generation in %s.current", legacy)
	}
	return filepath.Join(filepath.Dir(legacy), name), nil
}

// SelectStagingGeneration makes this command use an existing generation of the
// mode. A generation that is gone or not of this mode is ignored: the caller
// rebuilds as it would for any stale staging.
func SelectStagingGeneration(projectRoot, modeName, root string) error {
	legacy, err := stagingModePath(projectRoot, modeName)
	if err != nil {
		return err
	}
	if filepath.Dir(root) != filepath.Dir(legacy) || !strings.HasPrefix(filepath.Base(root), filepath.Base(legacy)+"-g-") {
		return nil
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil
	}
	leaseStagingGeneration(root)
	stagingSelections.Store(legacy, root)
	return nil
}

// StagingGeneration builds privately and publishes only after verification.
// Old generations stay while a process holds their lease or a launch record
// names them; see ReclaimStagingGenerations.
type StagingGeneration struct {
	Root, previous, legacy string
	published              bool
}

func BeginStagingGeneration(projectRoot, modeName string) (*StagingGeneration, error) {
	legacy, err := stagingModePath(projectRoot, modeName)
	if err != nil {
		return nil, err
	}
	previous, err := selectedStagingRoot(projectRoot, modeName)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(filepath.Dir(legacy), filepath.Base(legacy)+"-g-")
	if err != nil {
		return nil, err
	}
	leaseStagingGeneration(root)
	stagingSelections.Store(legacy, root)
	return &StagingGeneration{Root: root, previous: previous, legacy: legacy}, nil
}

func (generation *StagingGeneration) Publish() error {
	if err := WriteAtomic(generation.legacy+".current", []byte(filepath.Base(generation.Root)+"\n"), 0o600); err != nil {
		return err
	}
	generation.published = true
	return nil
}

func (generation *StagingGeneration) Abort() {
	if !generation.published {
		stagingSelections.Store(generation.legacy, generation.previous)
		// Keep failed generations too: they may contain credentials or recovery files.
	}
}

// ReclaimStagingGenerations removes the mode's generations that are not
// published, not in keep (the roots launch records return to), were fully
// built (have a staged record), and that no running process holds a lease on.
// A generation that failed to build is kept: it may hold recovery files.
// Failures are ignored; reclaiming never fails a sync.
func ReclaimStagingGenerations(projectRoot, modeName string, keep []string) {
	legacy, err := stagingModePath(projectRoot, modeName)
	if err != nil {
		return
	}
	current, err := readStagingSelection(legacy)
	if err != nil {
		return
	}
	entries, _ := os.ReadDir(filepath.Dir(legacy))
	for _, entry := range entries {
		root := filepath.Join(filepath.Dir(legacy), entry.Name())
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), filepath.Base(legacy)+"-g-") || root == current || slices.Contains(keep, root) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, ".staged.json")); err != nil {
			continue
		}
		lease := flock.New(root + ".lease")
		if free, err := lease.TryLock(); err != nil || !free {
			continue
		}
		if os.RemoveAll(root) == nil {
			_ = os.Remove(root + ".lease")
		}
		_ = lease.Unlock()
	}
}
