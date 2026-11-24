package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

const generationLeaseSuffix = ".lease"

// GenerationSet stores directories with unique names and publishes one through
// Pointer. The pointer guard covers selection through lease acquisition.
type GenerationSet struct {
	Directory, Prefix, Pointer string
	AbsolutePointer            bool
}

func (set GenerationSet) guard(options ...flock.Option) *flock.Flock {
	return flock.New(set.Pointer+".lock", options...)
}

func (set GenerationSet) contains(root string) bool {
	return root == filepath.Clean(root) && filepath.Dir(root) == set.Directory && set.matches(filepath.Base(root))
}

func (set GenerationSet) matches(name string) bool {
	suffix, ok := strings.CutPrefix(name, set.Prefix)
	// MkdirTemp appends a random number. A hyphen can mark a longer mode's
	// prefix, whose generations are protected by a different pointer guard.
	return ok && suffix != "" && !strings.Contains(suffix, "-")
}

// Current reads the published path. Call LeaseCurrent before using a home
// beyond the lifetime of the pointer read.
func (set GenerationSet) Current() (string, error) {
	data, err := os.ReadFile(set.Pointer)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(data))
	if strings.ContainsRune(name, 0) || (set.AbsolutePointer && !filepath.IsAbs(name)) ||
		(!set.AbsolutePointer && filepath.Base(name) != name) {
		return "", fmt.Errorf("invalid generation pointer %s", set.Pointer)
	}
	root := filepath.Clean(name)
	if !set.AbsolutePointer {
		root = filepath.Join(set.Directory, name)
	}
	if !set.contains(root) {
		return "", fmt.Errorf("generation pointer %s points outside its mode", set.Pointer)
	}
	return root, nil
}

// LeaseCurrent returns the published root and a shared lease, or an empty root
// if the pointer or its directory is absent. The caller releases the lease.
func (set GenerationSet) LeaseCurrent() (string, *flock.Flock, error) {
	return set.lease(set.Current)
}

// Lease pins a previously published root when it still exists and no reclaimer
// owns its lease. A nil lease means the caller should use the current root.
func (set GenerationSet) Lease(root string) (*flock.Flock, error) {
	if !set.contains(root) {
		return nil, nil
	}
	_, lease, err := set.lease(func() (string, error) { return root, nil })
	return lease, err
}

func (set GenerationSet) lease(read func() (string, error)) (string, *flock.Flock, error) {
	if err := os.MkdirAll(filepath.Dir(set.Pointer), 0o700); err != nil {
		return "", nil, err
	}
	guard := set.guard()
	if err := guard.RLock(); err != nil {
		return "", nil, err
	}
	defer guard.Unlock()

	root, err := read()
	if err != nil || root == "" {
		return "", nil, err
	}
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("generation must be a directory: %s", root)
	}
	lease := flock.New(root + generationLeaseSuffix)
	if locked, err := lease.TryRLock(); err != nil || !locked {
		return "", nil, err
	}
	return root, lease, nil
}

// Generation is a private build with a lease held from creation through
// publication or rollback.
type Generation struct {
	set       GenerationSet
	root      string
	lease     *flock.Flock
	published bool
}

func (set GenerationSet) Begin() (*Generation, error) {
	if err := os.MkdirAll(filepath.Dir(set.Pointer), 0o700); err != nil {
		return nil, err
	}
	guard := set.guard()
	if err := guard.RLock(); err != nil {
		return nil, err
	}
	defer guard.Unlock()
	if err := os.MkdirAll(set.Directory, 0o700); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(set.Directory, set.Prefix)
	if err != nil {
		return nil, err
	}
	lease := flock.New(root + generationLeaseSuffix)
	locked, err := lease.TryRLock()
	if err == nil && !locked {
		err = fmt.Errorf("generation lease still in use: %s", root)
	}
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(root))
	}
	return &Generation{set: set, root: root, lease: lease}, nil
}

func (generation *Generation) Root() string { return generation.root }

func (generation *Generation) Commit() error {
	guard := generation.set.guard()
	if err := guard.Lock(); err != nil {
		return err
	}
	defer guard.Unlock()
	name := generation.root
	if !generation.set.AbsolutePointer {
		name = filepath.Base(name)
	}
	if err := WriteAtomic(generation.set.Pointer, []byte(name+"\n"), 0o600); err != nil {
		return err
	}
	generation.published = true
	return nil
}

// Abort releases the build lease and discards an unpublished root.
func (generation *Generation) Abort() error {
	if generation.published {
		return generation.lease.Unlock()
	}
	return generation.set.remove(generation.lease, generation.root)
}

// Roots lists generation directories, including older unpublished ones.
func (set GenerationSet) Roots() ([]string, error) {
	entries, err := os.ReadDir(set.Directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var roots []string
	for _, entry := range entries {
		if entry.IsDir() && set.matches(entry.Name()) {
			roots = append(roots, filepath.Join(set.Directory, entry.Name()))
		}
	}
	return roots, nil
}

// Reclaim removes unused generations. canRetire runs with the candidate's
// exclusive lease after checking the current pointer, so it can inspect live
// records or do slow cleanup without holding up other generations.
func (set GenerationSet) Reclaim(canRetire func(string) bool) {
	roots, err := set.Roots()
	if err != nil {
		return
	}
	for _, root := range roots {
		lease := set.claimUnused(root)
		if lease == nil {
			continue
		}
		if canRetire != nil && !canRetire(root) {
			_ = lease.Unlock()
			continue
		}
		_ = set.remove(lease, root)
	}
}

func (set GenerationSet) claimUnused(root string) *flock.Flock {
	// Cleanup can race removal of the whole project. Never recreate its guard.
	guard := set.guard(flock.SetFlag(os.O_RDWR))
	if locked, err := guard.TryLock(); err != nil || !locked {
		return nil
	}
	defer guard.Unlock()
	current, err := set.Current()
	if err != nil || current == root {
		return nil
	}
	// A concurrent reclaimer can have removed this root since Roots.
	if _, err := os.Lstat(root); err != nil {
		return nil
	}
	lease := flock.New(root + generationLeaseSuffix)
	if locked, err := lease.TryLock(); err != nil || !locked {
		return nil
	}
	return lease
}

func (set GenerationSet) remove(lease *flock.Flock, root string) error {
	if err := os.RemoveAll(root); err != nil {
		return errors.Join(err, lease.Unlock())
	}
	// Unlink only after closing the handle (needed on Windows). The guard
	// prevents another reader from opening the file between close and unlink.
	guard := set.guard(flock.SetFlag(os.O_RDWR))
	if err := guard.Lock(); err != nil {
		return errors.Join(err, lease.Unlock())
	}
	defer guard.Unlock()
	if err := lease.Unlock(); err != nil {
		return err
	}
	return os.Remove(lease.Path())
}

// GenerationLeasesInUse checks for running launches before removing an orphaned
// project's generation trees. Lease files live one directory below the project.
func GenerationLeasesInUse(projectDir string) bool {
	leases, err := filepath.Glob(filepath.Join(projectDir, "*", "*"+generationLeaseSuffix))
	if err != nil {
		return true
	}
	for _, path := range leases {
		lease := flock.New(path, flock.SetFlag(os.O_RDWR))
		free, err := lease.TryLock()
		if err != nil || !free {
			return true
		}
		_ = lease.Unlock()
	}
	return false
}
