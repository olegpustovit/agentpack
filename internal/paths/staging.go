package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// A command keeps its selected paths through child exit and credential recovery,
// even when another process publishes a replacement generation.
var stagingSelections sync.Map

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

// StagingGeneration builds privately and publishes only after verification.
// Old generations are retained so running sessions and their support files survive.
// shortcut: generations accumulate, add lease-aware reclamation when disk use requires it.
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
	stagingSelections.Store(legacy, root)
	return &StagingGeneration{Root: root, previous: previous, legacy: legacy}, nil
}

func (generation *StagingGeneration) Publish() error {
	file, err := os.CreateTemp(filepath.Dir(generation.legacy), ".staging-current-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(filepath.Base(generation.Root) + "\n")
	if err := file.Close(); writeErr == nil {
		writeErr = err
	}
	if writeErr != nil {
		return writeErr
	}
	if err := os.Rename(file.Name(), generation.legacy+".current"); err != nil {
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
