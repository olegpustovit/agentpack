package registry

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/gofrs/flock"
	"github.com/olegpustovit/agentpack/internal/lockfile"
)

type index struct {
	Schema     int                  `json:"schema"`
	Agentpacks map[string]Agentpack `json:"agentpacks"`
	Packages   []string             `json:"packages"`
}

func (s *Store) objectPath(id string) (string, error) {
	if !validDigest(id) {
		return "", fmt.Errorf("invalid object digest")
	}
	return filepath.Join(s.root, objectsDir, id+".json"), nil
}

func (s *Store) writeObject(value any) (string, error) {
	data, err := marshalObject(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	path := filepath.Join(s.root, objectsDir, id+".json")
	if err := writeExclusiveOrVerify(path, data); err != nil {
		return "", err
	}
	return id, nil
}

func marshalObject(value any) ([]byte, error) {
	switch item := value.(type) {
	case Package:
		item.ID = ""
		return json.Marshal(item)
	case Revision:
		item.ID = ""
		return json.Marshal(item)
	case nameRevision:
		item.ID = ""
		return json.Marshal(item)
	default:
		return json.Marshal(value)
	}
}

func (s *Store) readObject(id string, target any) error {
	path, err := s.objectPath(id)
	if err != nil {
		return err
	}
	data, err := readRegular(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != id {
		return fmt.Errorf("registry object digest mismatch")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode registry object")
	}
	switch item := target.(type) {
	case *Package:
		item.ID = id
	case *Revision:
		item.ID = id
	case *nameRevision:
		item.ID = id
	}
	return nil
}

func (s *Store) readPackage(id string) (Package, error) {
	var p Package
	err := s.readObject(id, &p)
	if err == nil && (p.Schema != 0 && p.Schema != objectSchema || p.Module == "" || p.Commit == "" || (p.Kind != string(lockfile.PackageSkill) && p.Kind != string(lockfile.PackagePlugin)) || (p.ContentHash != "" && !lockfile.ValidContentHash(p.ContentHash))) {
		err = fmt.Errorf("invalid package registry object")
	}
	return p, err
}

func (s *Store) readRevision(id string) (Revision, error) {
	var r Revision
	if err := s.readObject(id, &r); err != nil {
		return r, err
	}
	if !validID(r.AgentpackID) {
		return r, fmt.Errorf("invalid agentpack revision object")
	}
	if r.Schema != 0 && r.Schema != objectSchema {
		return r, fmt.Errorf("unsupported agentpack revision schema")
	}
	manifestData, manifestSecrets, err := normalizeTOML([]byte(r.Manifest))
	if err != nil || !bytes.Equal(manifestData, []byte(r.Manifest)) {
		return r, fmt.Errorf("agentpack revision manifest is not normalized and redacted")
	}
	lockData, lockSecrets, err := normalizeTOML([]byte(r.Lock))
	if err != nil || !bytes.Equal(lockData, []byte(r.Lock)) {
		return r, fmt.Errorf("agentpack revision lock is not normalized and redacted")
	}
	if _, err := validateSnapshot(manifestData, lockData); err != nil {
		return r, fmt.Errorf("invalid agentpack revision snapshot")
	}
	if r.NeedsLocalConfiguration != (len(manifestSecrets)+len(lockSecrets) > 0) {
		return r, fmt.Errorf("agentpack revision configuration marker mismatch")
	}
	for _, packageID := range r.PackageIDs {
		if _, err := s.readPackage(packageID); err != nil {
			return r, err
		}
	}
	for _, parentID := range r.ParentIDs {
		var parent Revision
		if err := s.readObject(parentID, &parent); err != nil {
			return r, err
		}
		if parent.AgentpackID != r.AgentpackID {
			return r, fmt.Errorf("agentpack revision parent owner mismatch")
		}
	}
	return r, nil
}

func (s *Store) readIndex() (index, error) { return s.readIndexUnlocked() }

func (s *Store) readIndexUnlocked() (index, error) {
	path := filepath.Join(s.root, indexName)
	data, err := readRegular(path)
	if os.IsNotExist(err) {
		return s.rebuildIndex()
	}
	if err != nil {
		return index{}, err
	}
	var idx index
	if err := json.Unmarshal(data, &idx); err != nil {
		return index{}, fmt.Errorf("parse registry index")
	}
	if idx.Schema != schema {
		return index{}, fmt.Errorf("unsupported registry index schema")
	}
	if idx.Agentpacks == nil {
		idx.Agentpacks = map[string]Agentpack{}
	}
	for _, id := range idx.Packages {
		if _, err := s.readPackage(id); err != nil {
			return index{}, err
		}
	}
	for id, p := range idx.Agentpacks {
		if !validID(p.ID) || p.ID != id {
			return index{}, fmt.Errorf("invalid agentpack index entry")
		}
		for _, id := range p.Revisions {
			if _, err := s.readRevision(id); err != nil {
				return index{}, err
			}
		}
	}
	stale, err := s.indexStale(idx)
	if err != nil {
		return index{}, err
	}
	if stale {
		return s.rebuildIndex()
	}
	return idx, nil
}

func (s *Store) indexStale(idx index) (bool, error) {
	tombstoned := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(s.root, tombstonesDir))
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		tombstone, err := s.readTombstone(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return false, err
		}
		tombstoned[tombstone.Agentpack] = true
	}
	for id := range tombstoned {
		if _, ok := idx.Agentpacks[id]; ok {
			return true, nil
		}
	}
	entries, err = os.ReadDir(filepath.Join(s.root, objectsDir))
	if err != nil {
		return false, err
	}
	packages := map[string]bool{}
	for _, id := range idx.Packages {
		packages[id] = true
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validDigest(id) {
			continue
		}
		data, err := readRegular(filepath.Join(s.root, objectsDir, entry.Name()))
		if err != nil {
			return false, err
		}
		obj, err := decodeImported(data)
		if err != nil || obj.id != id {
			return false, fmt.Errorf("invalid registry public object")
		}
		if obj.name != nil {
			if tombstoned[obj.name.Agentpack] {
				continue
			}
			_, ok := idx.Agentpacks[obj.name.Agentpack]
			if !ok {
				return true, nil
			}
			continue
		}
		if obj.packageRecord != nil {
			if _, err := s.readPackage(id); err != nil {
				return false, err
			}
			if !packages[id] {
				return true, nil
			}
			continue
		}
		if obj.revision == nil {
			continue
		}
		revision := obj.revision
		if tombstoned[revision.AgentpackID] {
			continue
		}
		pack, ok := idx.Agentpacks[revision.AgentpackID]
		if !ok || !slices.Contains(pack.Revisions, id) {
			return true, nil
		}
		for _, packageID := range revision.PackageIDs {
			if !packages[packageID] {
				return true, nil
			}
		}
	}
	latestNames, err := s.nameObjects()
	if err != nil {
		return false, err
	}
	for owner, name := range latestNames {
		if tombstoned[owner] {
			continue
		}
		pack, ok := idx.Agentpacks[owner]
		if !ok || pack.Name != name.Name {
			return true, nil
		}
	}
	for id, pack := range idx.Agentpacks {
		if tombstoned[id] {
			return true, nil
		}
		computedHeads, err := s.revisionHeads(pack.Revisions)
		if err != nil {
			return false, err
		}
		if !slices.Equal(pack.Heads, computedHeads) {
			return true, nil
		}
		data, err := readRegular(filepath.Join(s.root, metadataDir, id+".json"))
		if os.IsNotExist(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		var metadata Agentpack
		_, hasName := latestNames[id]
		if err := json.Unmarshal(data, &metadata); err != nil || metadata.ID != id || !hasName && metadata.Name != pack.Name {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) rebuildIndex() (index, error) {
	result := index{Schema: schema, Agentpacks: map[string]Agentpack{}, Packages: []string{}}
	entries, err := os.ReadDir(filepath.Join(s.root, objectsDir))
	if err != nil {
		return index{}, err
	}
	revisions := map[string][]Revision{}
	names := map[string]nameRevision{}
	tombstoned := map[string]bool{}
	tombstoneEntries, err := os.ReadDir(filepath.Join(s.root, tombstonesDir))
	if err != nil {
		return index{}, err
	}
	for _, entry := range tombstoneEntries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		t, err := s.readTombstone(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return index{}, err
		}
		tombstoned[t.Agentpack] = true
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validDigest(id) {
			continue
		}
		if !entry.Type().IsRegular() {
			return index{}, fmt.Errorf("registry object must be a regular file")
		}
		path, _ := s.objectPath(id)
		data, err := readRegular(path)
		if err != nil {
			return index{}, err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != id {
			return index{}, fmt.Errorf("registry object digest mismatch")
		}
		obj, err := decodeImported(data)
		if err != nil || obj.id != id {
			return index{}, fmt.Errorf("decode registry object")
		}
		if obj.name != nil {
			if !tombstoned[obj.name.Agentpack] {
				if current, ok := names[obj.name.Agentpack]; !ok || nameNewer(*obj.name, current) {
					names[obj.name.Agentpack] = *obj.name
				}
			}
			continue
		}
		if obj.revision != nil {
			if tombstoned[obj.revision.AgentpackID] {
				continue
			}
			revision := *obj.revision
			revision.ID = id
			if !validID(revision.AgentpackID) {
				return index{}, fmt.Errorf("invalid agentpack revision owner")
			}
			revisions[revision.AgentpackID] = append(revisions[revision.AgentpackID], revision)
			for _, packageID := range revision.PackageIDs {
				if _, err := s.readPackage(packageID); err != nil {
					return index{}, err
				}
				result.Packages = appendUnique(result.Packages, packageID)
			}
			continue
		}
		if obj.packageRecord != nil {
			if _, err := s.readPackage(id); err != nil {
				return index{}, err
			}
			result.Packages = appendUnique(result.Packages, id)
			continue
		}
		return index{}, fmt.Errorf("decode registry object")
	}
	for id, packRevisions := range revisions {
		sort.Slice(packRevisions, func(i, j int) bool {
			if packRevisions[i].CreatedAt.Equal(packRevisions[j].CreatedAt) {
				return packRevisions[i].ID < packRevisions[j].ID
			}
			return packRevisions[i].CreatedAt.Before(packRevisions[j].CreatedAt)
		})
		name := ""
		if record, ok := names[id]; ok {
			name = record.Name
		} else if metadata, err := readRegular(filepath.Join(s.root, metadataDir, id+".json")); err == nil {
			var pack Agentpack
			if json.Unmarshal(metadata, &pack) == nil && pack.ID == id {
				name = pack.Name
			}
		} else if !os.IsNotExist(err) {
			return index{}, err
		}
		if name == "" {
			name = filepath.Base(packRevisions[0].Project)
		}
		if name == "" || name == "." {
			name = id
		}
		pack := Agentpack{ID: id, Name: name, Revisions: make([]string, 0, len(packRevisions))}
		for _, revision := range packRevisions {
			pack.Revisions = append(pack.Revisions, revision.ID)
		}
		pack.Heads, err = s.revisionHeads(pack.Revisions)
		if err != nil {
			return index{}, err
		}
		result.Agentpacks[id] = pack
	}
	sort.Strings(result.Packages)
	return result, nil
}

func (s *Store) writeMetadata(pack Agentpack) error {
	data, err := json.Marshal(pack)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.root, metadataDir, pack.ID+".json"), data, 0o600)
}

func (s *Store) writeIndexUnlocked(idx index) error {
	idx.Schema = schema
	sort.Strings(idx.Packages)
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.root, indexName), append(data, '\n'), 0o600)
}

func (s *Store) writeBindings(id string, b bindings) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.root, bindingsDir, id+".json"), data, 0o600)
}

func (s *Store) withLock(fn func() error) (err error) {
	guard := flock.New(filepath.Join(s.root, "registry.lock"))
	if err := guard.Lock(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, guard.Unlock()) }()
	return fn()
}

func writeExclusiveOrVerify(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if existing, err := readRegular(path); err == nil {
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("registry object collision")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".object-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := publishExclusive(name, path); err != nil {
		if os.IsExist(err) {
			existing, e := readRegular(path)
			if e == nil && bytes.Equal(existing, data) {
				return nil
			}
		}
		return err
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".registry-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := renameDurable(name, path, true); err != nil {
		return err
	}
	return nil
}

func removeDurable(path string) error {
	return removeFileDurable(path)
}

func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("registry file must be regular")
	}
	return os.ReadFile(path)
}

func appendUnique(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}

func sorted(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func validDigest(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

func validID(id string) bool {
	return len(id) >= 16 && len(id) <= 64 && !strings.ContainsAny(id, "/\\") && filepath.Base(id) == id
}

func randomID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}
