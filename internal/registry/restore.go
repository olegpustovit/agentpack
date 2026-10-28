package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// Restore writes the selected definition, runs apply, and restores both original files on any failure.
func (s *Store) Restore(id, revisionID, directory string, apply func() error) error {
	if err := s.Recover(directory); err != nil {
		return err
	}
	_, revs, err := s.Get(id)
	if err != nil {
		return err
	}
	var target *Revision
	for i := range revs {
		if revs[i].ID == revisionID {
			target = &revs[i]
			break
		}
	}
	if target == nil {
		return ErrNotFound
	}
	manifestData, lockData, err := s.restoreBytes(*target)
	if err != nil {
		return err
	}
	root, err := canonicalDirectory(directory)
	if err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("restore destination must be a real directory")
	}
	manifestPath, lockPath := paths.ManifestPath(root), paths.LockPath(root)
	oldManifest, hadManifest, oldManifestMode, err := readRegularOptional(manifestPath)
	if err != nil {
		return err
	}
	oldLock, hadLock, oldLockMode, err := readRegularOptional(lockPath)
	if err != nil {
		return err
	}
	journal := restoreJournal{Project: root, Manifest: oldManifest, Lock: oldLock, HadManifest: hadManifest, HadLock: hadLock, ManifestMode: oldManifestMode, LockMode: oldLockMode}
	transactionPath, err := s.writeJournal(root, journal)
	if err != nil {
		return err
	}
	manifestMode, lockMode := oldManifestMode, oldLockMode
	if !hadManifest {
		manifestMode = 0o644
	}
	if !hadLock {
		lockMode = 0o644
	}
	rollback := func() error {
		var errs []error
		if err := restoreOne(manifestPath, oldManifest, hadManifest, oldManifestMode); err != nil {
			errs = append(errs, err)
		}
		if err := restoreOne(lockPath, oldLock, hadLock, oldLockMode); err != nil {
			errs = append(errs, err)
		}
		joined := errors.Join(errs...)
		if joined == nil {
			joined = removeDurable(transactionPath)
		}
		return joined
	}
	if err := writeAtomic(manifestPath, manifestData, manifestMode); err != nil {
		return errors.Join(err, rollback())
	}
	if err := writeAtomic(lockPath, lockData, lockMode); err != nil {
		return errors.Join(err, rollback())
	}
	if apply != nil {
		if err := apply(); err != nil {
			return errors.Join(err, rollback())
		}
	}
	if err := removeDurable(transactionPath); err != nil {
		return fmt.Errorf("finish registry restore transaction: %w", err)
	}
	return nil
}

func captureProject(directory string) (CaptureInput, error) {
	manifestData, err := readRequired(paths.ManifestPath(directory))
	if err != nil {
		return CaptureInput{}, err
	}
	lockData, err := readRequired(paths.LockPath(directory))
	if err != nil {
		return CaptureInput{}, err
	}
	return CaptureInput{Project: directory, Manifest: manifestData, Lock: lockData}, nil
}

func diffLocks(a, b lockfile.PackLock, modeName string) DiffResult {
	am := map[string]lockfile.Package{}
	bm := map[string]lockfile.Package{}
	for _, p := range a.Packages {
		am[p.Module] = p
	}
	for _, p := range b.Packages {
		bm[p.Module] = p
	}
	r := DiffResult{Add: []string{}, Update: []string{}, Remove: []string{}, Mode: modeName}
	for m, pkg := range bm {
		old, ok := am[m]
		if !ok {
			r.Add = append(r.Add, m)
		} else if old != pkg {
			r.Update = append(r.Update, m)
		}
	}
	for m := range am {
		if _, ok := bm[m]; !ok {
			r.Remove = append(r.Remove, m)
		}
	}
	sort.Strings(r.Add)
	sort.Strings(r.Update)
	sort.Strings(r.Remove)
	return r
}

func (s *Store) restoreBytes(revision Revision) ([]byte, []byte, error) {
	var b bindings
	path := filepath.Join(s.root, bindingsDir, revision.ID+".json")
	data, exists, _, err := readRegularOptional(path)
	if err != nil {
		return nil, nil, err
	}
	if exists {
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, nil, fmt.Errorf("parse local registry bindings")
		}
	}
	if b.Values == nil {
		b.Values = map[string]string{}
	}
	manifestData := []byte(revision.Manifest)
	lockData := []byte(revision.Lock)
	_, manifestSecrets, err := normalizeTOML(manifestData)
	if err != nil {
		return nil, nil, fmt.Errorf("validate redacted revision manifest")
	}
	_, lockSecrets, err := normalizeTOML(lockData)
	if err != nil {
		return nil, nil, fmt.Errorf("validate redacted revision lock")
	}
	expected := mergeBindings(manifestSecrets, lockSecrets, "manifest:", "lock:")
	if revision.NeedsLocalConfiguration != (len(expected) != 0) || len(expected) != len(b.Values) {
		return nil, nil, fmt.Errorf("revision needs local configuration on this machine")
	}
	for path := range expected {
		if _, ok := b.Values[path]; !ok {
			return nil, nil, fmt.Errorf("revision needs local configuration on this machine")
		}
	}
	for path := range b.Values {
		if _, ok := expected[path]; !ok {
			return nil, nil, fmt.Errorf("local registry binding does not match revision")
		}
	}
	if revision.NeedsLocalConfiguration && len(b.Values) == 0 {
		return nil, nil, fmt.Errorf("revision needs local configuration on this machine")
	}
	manifestData, err = applyBindings(manifestData, b.Values, "manifest:")
	if err != nil {
		return nil, nil, fmt.Errorf("restore local manifest bindings: %w", err)
	}
	lockData, err = applyBindings(lockData, b.Values, "lock:")
	if err != nil {
		return nil, nil, fmt.Errorf("restore local lock bindings: %w", err)
	}
	cleanManifest, _, err := normalizeTOML(manifestData)
	if err != nil || !bytes.Equal(cleanManifest, []byte(revision.Manifest)) {
		return nil, nil, fmt.Errorf("restored manifest does not match revision")
	}
	cleanLock, _, err := normalizeTOML(lockData)
	if err != nil || !bytes.Equal(cleanLock, []byte(revision.Lock)) {
		return nil, nil, fmt.Errorf("restored lock does not match revision")
	}
	return manifestData, lockData, nil
}

type optionalFile struct {
	data   []byte
	exists bool
}

type restoreJournal struct {
	Project      string      `json:"project"`
	Manifest     []byte      `json:"manifest"`
	Lock         []byte      `json:"lock"`
	HadManifest  bool        `json:"had_manifest"`
	HadLock      bool        `json:"had_lock"`
	ManifestMode os.FileMode `json:"manifest_mode"`
	LockMode     os.FileMode `json:"lock_mode"`
}

func (s *Store) writeJournal(project string, journal restoreJournal) (string, error) {
	hash, err := paths.ProjectPathHash(project)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.root, transactionsDir, hash+".json")
	data, err := json.Marshal(journal)
	if err != nil {
		return "", err
	}
	if err := writeAtomic(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func validateJournal(data []byte, journal restoreJournal, project string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("parse interrupted restore journal")
	}
	for _, field := range []string{"project", "manifest", "lock", "had_manifest", "had_lock", "manifest_mode", "lock_mode"} {
		if _, ok := fields[field]; !ok {
			return fmt.Errorf("incomplete interrupted restore journal")
		}
	}
	root, err := canonicalDirectory(journal.Project)
	if err != nil || root != project {
		return fmt.Errorf("interrupted restore journal project mismatch")
	}
	if journal.HadManifest && journal.Manifest == nil || journal.HadLock && journal.Lock == nil {
		return fmt.Errorf("incomplete interrupted restore journal")
	}
	if !journal.HadManifest && (len(journal.Manifest) != 0 || journal.ManifestMode != 0) || !journal.HadLock && (len(journal.Lock) != 0 || journal.LockMode != 0) {
		return fmt.Errorf("invalid interrupted restore journal")
	}
	if journal.ManifestMode&^0o777 != 0 || journal.LockMode&^0o777 != 0 {
		return fmt.Errorf("invalid interrupted restore journal modes")
	}
	return nil
}

// Recover rolls back an interrupted restore for one definition directory.
func (s *Store) Recover(directory string) error {
	root, err := canonicalDirectory(directory)
	if err != nil {
		return err
	}
	hash, err := paths.ProjectPathHash(root)
	if err != nil {
		return err
	}
	path := filepath.Join(s.root, transactionsDir, hash+".json")
	data, err := readRegular(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal restoreJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("parse interrupted restore journal")
	}
	if err := validateJournal(data, journal, root); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("restore destination must be a real directory")
	}
	if err := restoreOne(paths.ManifestPath(root), journal.Manifest, journal.HadManifest, journal.ManifestMode); err != nil {
		return err
	}
	if err := restoreOne(paths.LockPath(root), journal.Lock, journal.HadLock, journal.LockMode); err != nil {
		return err
	}
	return removeDurable(path)
}

func canonicalDirectory(directory string) (string, error) {
	root, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}

// RecoverDefinition is a no-op without an interrupted restore and does not create registry storage.
func RecoverDefinition(home, directory string) error {
	root, err := filepath.Abs(filepath.Join(home, "registry"))
	if err != nil {
		return err
	}
	hash, err := paths.ProjectPathHash(directory)
	if err != nil {
		return err
	}
	path := filepath.Join(root, transactionsDir, hash+".json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	store, err := New(home)
	if err != nil {
		return err
	}
	return store.Recover(directory)
}

// PendingRestore reports an interrupted restore without changing project or registry files.
func PendingRestore(home, directory string) (bool, error) {
	root, err := filepath.Abs(filepath.Join(home, "registry"))
	if err != nil {
		return false, err
	}
	project, err := canonicalDirectory(directory)
	if err != nil {
		return false, err
	}
	hash, err := paths.ProjectPathHash(project)
	if err != nil {
		return false, err
	}
	data, err := readRegular(filepath.Join(root, transactionsDir, hash+".json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var journal restoreJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return false, fmt.Errorf("parse interrupted restore journal")
	}
	if err := validateJournal(data, journal, project); err != nil {
		return false, err
	}
	return true, nil
}

func readRegularOptional(path string) ([]byte, bool, os.FileMode, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, 0, nil
	}
	if err != nil {
		return nil, false, 0, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, 0, fmt.Errorf("registry file must be regular")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, 0, nil
	}
	return data, err == nil, info.Mode().Perm(), err
}

func readRequired(path string) ([]byte, error) {
	data, err := readRegular(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func restoreOne(path string, data []byte, exists bool, mode os.FileMode) error {
	if !exists {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return writeAtomic(path, data, mode)
}
