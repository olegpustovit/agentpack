package registry

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

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
	id, site    string
	version, cl int64
}

type memoKey struct{ kind, id string }

type memoEntry struct {
	stamp objectStamp
	value any
}

var memoKinds = []string{"package", "revision", "index"}

// Immutable objects are revalidated when their replicated conflict position changes.
// One entry per digest keeps the memo bounded by the live objects; removeObject drops it.
func memoized[T any](s *Store, kind, id string, load func() (T, error)) (T, error) {
	stamps, err := s.bodyStamps()
	stamp, ok := stamps[id]
	if err != nil || !ok {
		return load()
	}
	key := memoKey{kind, id}
	if entry, ok := s.memo.Load(key); ok && entry.(memoEntry).stamp == stamp {
		return entry.(memoEntry).value.(T), nil
	}
	value, err := load()
	if err == nil {
		s.memo.Store(key, memoEntry{stamp, value})
	}
	return value, err
}

// stampCache holds every object and name body's conflict position as of one database version.
type indexCache struct {
	mu      sync.Mutex
	ok      bool
	version int64
	value   index
}

type stampCache struct {
	mu      sync.Mutex
	version int64
	byID    map[string]objectStamp
}

// bodyStamps answers every lookup from one crsql_changes scan until the database version moves:
// a per-object query scans the whole change table each time, which made Library reads O(objects × changes).
func (s *Store) bodyStamps() (map[string]objectStamp, error) {
	version, err := s.dbVersion()
	if err != nil {
		return nil, err
	}
	s.stamps.mu.Lock()
	defer s.stamps.mu.Unlock()
	if s.stamps.byID != nil && s.stamps.version == version {
		return s.stamps.byID, nil
	}
	digests := map[string]string{}
	rows, err := s.sql.Reader.Query(`SELECT digest,crsql_pack_columns(digest) FROM registry_objects
 UNION ALL SELECT digest,crsql_pack_columns(digest) FROM registry_names`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var digest string
		var pk []byte
		if err := rows.Scan(&digest, &pk); err != nil {
			rows.Close()
			return nil, err
		}
		digests[string(pk)] = digest
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	byID := make(map[string]objectStamp, len(digests))
	rows, err = s.sql.Reader.Query(`SELECT pk,col_version,cl,site_id FROM crsql_changes
 WHERE "table" IN ('registry_objects','registry_names') AND cid='body'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pk, site []byte
		var stamp objectStamp
		if err := rows.Scan(&pk, &stamp.version, &stamp.cl, &site); err != nil {
			return nil, err
		}
		if digest, ok := digests[string(pk)]; ok {
			stamp.id, stamp.site = digest, string(site)
			byID[digest] = stamp
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.stamps.version, s.stamps.byID = version, byID
	return byID, nil
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

// readIndex reuses the last index while the database version holds: every input is a replicated table.
func (s *Store) readIndex() (index, error) {
	version, versionErr := s.dbVersion()
	s.indexCache.mu.Lock()
	hit := versionErr == nil && s.indexCache.ok && s.indexCache.version == version
	cached := s.indexCache.value
	s.indexCache.mu.Unlock()
	if hit {
		return index{Agentpacks: maps.Clone(cached.Agentpacks), Packages: cached.Packages}, nil
	}
	idx, err := s.buildIndex()
	if err != nil || versionErr != nil {
		return idx, err
	}
	// Clipped slices make a caller's append copy instead of writing into the cached index.
	for id, pack := range idx.Agentpacks {
		pack.Revisions, pack.Heads = slices.Clip(pack.Revisions), slices.Clip(pack.Heads)
		idx.Agentpacks[id] = pack
	}
	idx.Packages = slices.Clip(idx.Packages)
	s.indexCache.mu.Lock()
	s.indexCache.ok, s.indexCache.version, s.indexCache.value = true, version, idx
	s.indexCache.mu.Unlock()
	return index{Agentpacks: maps.Clone(idx.Agentpacks), Packages: idx.Packages}, nil
}

// dbVersion is the newest change to a table the index reads. Each clock table's db_version index answers it
// directly; the database-wide version moves on every streamed chat flush and would rebuild the index constantly.
func (s *Store) dbVersion() (int64, error) {
	var version int64
	err := s.sql.Reader.QueryRow(`SELECT max(
 (SELECT coalesce(max(db_version),0) FROM registry_objects__crsql_clock),
 (SELECT coalesce(max(db_version),0) FROM registry_names__crsql_clock),
 (SELECT coalesce(max(db_version),0) FROM registry_heads__crsql_clock),
 (SELECT coalesce(max(db_version),0) FROM registry_tombstones__crsql_clock))`).Scan(&version)
	return version, err
}

func (s *Store) buildIndex() (index, error) {
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
