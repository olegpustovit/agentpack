package registry

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofrs/flock"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/paths"
)

type index struct {
	Agentpacks map[string]Agentpack `json:"agentpacks"`
	Packages   []string             `json:"packages"`
}

func (s *Store) writeObject(value any) (string, error) {
	data, err := marshalObject(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	table := "registry_objects"
	if _, ok := value.(nameRevision); ok {
		table = "registry_names"
	}
	if err := s.storeObject(table, id, data); err != nil {
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
	data, err := s.objectData(id)
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

type objectStamp struct {
	kind, id, site string
	version, cl    int64
}

// Immutable objects are revalidated when their replicated conflict position changes.
func memoized[T any](s *Store, kind, id string, load func() (T, error)) (T, error) {
	stamp := objectStamp{kind: kind, id: id}
	var site []byte
	err := s.sql.Reader.QueryRow(`SELECT col_version,cl,site_id FROM crsql_changes
 WHERE "table" IN ('registry_objects','registry_names') AND pk=crsql_pack_columns(?) AND cid='body'`, id).Scan(&stamp.version, &stamp.cl, &site)
	if err != nil {
		return load()
	}
	stamp.site = string(site)
	if value, ok := s.memo.Load(stamp); ok {
		return value.(T), nil
	}
	value, err := load()
	if err == nil {
		s.memo.Store(stamp, value)
	}
	return value, err
}

func (s *Store) readPackage(id string) (Package, error) {
	return memoized(s, "package", id, func() (Package, error) { return s.loadPackage(id) })
}

func (s *Store) readRevision(id string) (Revision, error) {
	return memoized(s, "revision", id, func() (Revision, error) { return s.loadRevision(id) })
}

func (s *Store) loadPackage(id string) (Package, error) {
	var p Package
	err := s.readObject(id, &p)
	if err == nil && (p.Schema != 0 && p.Schema != objectSchema || p.Module == "" || p.Commit == "" || (p.Kind != string(lockfile.PackageSkill) && p.Kind != string(lockfile.PackagePlugin)) || (p.ContentHash != "" && !lockfile.ValidContentHash(p.ContentHash))) {
		err = fmt.Errorf("invalid package registry object")
	}
	return p, err
}

func (s *Store) loadRevision(id string) (Revision, error) {
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
	if err != nil || !sameTOML(manifestData, []byte(r.Manifest)) {
		return r, fmt.Errorf("agentpack revision manifest is not normalized and redacted")
	}
	lockData, lockSecrets, err := normalizeTOML([]byte(r.Lock))
	if err != nil || !sameTOML(lockData, []byte(r.Lock)) {
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

func (s *Store) readIndex() (index, error) {
	result := index{Agentpacks: map[string]Agentpack{}, Packages: []string{}}
	entries, err := s.objectIDs()
	if err != nil {
		return index{}, err
	}
	revisions := map[string][]Revision{}
	names := map[string]nameRevision{}
	tombstoned := map[string]bool{}
	tombstoneEntries, err := s.storedIDs("registry_tombstones")
	if err != nil {
		return index{}, err
	}
	for _, id := range tombstoneEntries {
		t, err := s.readTombstone(id)
		if err != nil {
			return index{}, err
		}
		tombstoned[t.Agentpack] = true
	}
	for _, id := range entries {
		obj, err := memoized(s, "index", id, func() (importedObject, error) {
			data, err := s.objectData(id)
			if err != nil {
				return importedObject{}, err
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != id {
				return importedObject{}, fmt.Errorf("registry object digest mismatch")
			}
			obj, err := decodeImported(data)
			if err != nil {
				return importedObject{}, err
			}
			if obj.id != id {
				return importedObject{}, fmt.Errorf("registry object digest mismatch")
			}
			return obj, nil
		})
		if err != nil {
			return index{}, err
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
		} else if metadata, err := s.metadata(id); err == nil {
			name = metadata.Name
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
	_, err = s.sql.DB.Exec(`INSERT INTO registry_heads(agentpack_id,body) VALUES (?,?)
 ON CONFLICT(agentpack_id) DO UPDATE SET body=excluded.body WHERE body<>excluded.body`, pack.ID, string(data))
	return err
}

func (s *Store) writeBindings(id string, b bindings) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = s.sql.DB.Exec(`INSERT INTO registry_bindings(revision_id,body) VALUES (?,?)
 ON CONFLICT(revision_id) DO UPDATE SET body=excluded.body WHERE body<>excluded.body`, id, string(data))
	return err
}

func (s *Store) withLock(fn func() error) (err error) {
	guard := flock.New(filepath.Join(s.root, "registry.lock"))
	if err := guard.Lock(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, guard.Unlock()) }()
	return fn()
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
	if err := paths.RenameDurable(name, path, true); err != nil {
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
