package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gofrs/flock"
)

// A command keeps its staging selection and shared leases through child
// exit and credential recovery, even after another process publishes.
var stagingSelections, stagingLeases sync.Map

func retainStagingLease(root string, lease *flock.Flock) {
	if lease != nil {
		if _, loaded := stagingLeases.LoadOrStore(root, lease); loaded {
			_ = lease.Unlock()
		}
	}
}

func stagingModePath(projectRoot, modeName string, create bool) (string, error) {
	root, err := stagingRootBase(projectRoot)
	if err != nil {
		return "", err
	}
	if create {
		err = EnsureOwnedPrivateDirectory(root)
	} else {
		err = checkStagingRoot(root, true)
	}
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(root, "modes", ModePathComponent(modeName)))
}

// EnsureOwnedPrivateDirectory refuses an unsafe existing directory before using it.
func EnsureOwnedPrivateDirectory(root string) error {
	err := checkStagingRoot(root, true)
	if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if err := checkStagingRoot(root, false); err != nil {
		return err
	}
	return EnsurePrivateDirectory(root)
}

func checkStagingRoot(root string, existing bool) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("staging root must be a directory: %s", root)
	}
	return checkStagingOwnership(root, info, existing)
}

func stagingGenerations(legacy string) GenerationSet {
	return GenerationSet{
		Directory: filepath.Dir(legacy),
		Prefix:    filepath.Base(legacy) + "-g-",
		Pointer:   legacy + ".current",
	}
}

func StagingRootForMode(projectRoot, modeName string) (string, error) {
	legacy, err := stagingModePath(projectRoot, modeName, true)
	if err != nil {
		return "", err
	}
	return selectStagingRoot(legacy)
}

func selectStagingRoot(legacy string) (string, error) {
	if selected, ok := stagingSelections.Load(legacy); ok {
		return selected.(string), nil
	}
	root, lease, err := stagingGenerations(legacy).LeaseCurrent()
	if err != nil {
		return "", err
	}
	if root == "" {
		root = legacy
	}
	retainStagingLease(root, lease)
	selected, _ := stagingSelections.LoadOrStore(legacy, root)
	return selected.(string), nil
}

// StagingGenerationName identifies the selected generation for durable
// companions that share its lease. Legacy mode directories have no suffix.
func StagingGenerationName(projectRoot, modeName string) (string, error) {
	legacy, err := stagingModePath(projectRoot, modeName, true)
	if err != nil {
		return "", err
	}
	root, err := selectStagingRoot(legacy)
	if err != nil || root == legacy {
		return "", err
	}
	return filepath.Base(root), nil
}

// SelectStagingGeneration makes this command reuse a remembered root if it
// can still be leased. A retired or unrelated root is ignored.
func SelectStagingGeneration(projectRoot, modeName, root string) error {
	legacy, err := stagingModePath(projectRoot, modeName, true)
	if err != nil {
		return err
	}
	if already, ok := stagingSelections.Load(legacy); ok && already == root {
		return nil
	}
	lease, err := stagingGenerations(legacy).Lease(root)
	if err != nil || lease == nil {
		return err
	}
	retainStagingLease(root, lease)
	stagingSelections.Store(legacy, root)
	return nil
}

// StagingGeneration holds the prior process selection for rollback.
type StagingGeneration struct {
	*Generation
	previous, legacy string
}

func BeginStagingGeneration(projectRoot, modeName string) (*StagingGeneration, error) {
	legacy, err := stagingModePath(projectRoot, modeName, true)
	if err != nil {
		return nil, err
	}
	previous, err := selectStagingRoot(legacy)
	if err != nil {
		return nil, err
	}
	generation, err := stagingGenerations(legacy).Begin()
	if err != nil {
		return nil, err
	}
	retainStagingLease(generation.root, generation.lease)
	stagingSelections.Store(legacy, generation.root)
	return &StagingGeneration{Generation: generation, previous: previous, legacy: legacy}, nil
}

func (generation *StagingGeneration) Abort() {
	if !generation.published {
		stagingSelections.CompareAndSwap(generation.legacy, generation.root, generation.previous)
		// Failed trees can contain credentials or recovery files.
	}
}

// ReclaimStagingGenerations considers only fully built, unleased trees that
// are no longer current. Reclaiming is best effort.
func ReclaimStagingGenerations(projectRoot, modeName string, canRetire func(string) bool) {
	legacy, err := stagingModePath(projectRoot, modeName, false)
	if err != nil {
		return
	}
	stagingGenerations(legacy).Reclaim(func(root string) bool {
		_, err := os.Lstat(filepath.Join(root, ".staged.json"))
		return err == nil && (canRetire == nil || canRetire(root))
	})
}
