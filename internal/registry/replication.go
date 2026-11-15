package registry

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/obs"
)

type Tombstone struct {
	ID        string    `json:"id"`
	Schema    int       `json:"schema"`
	Agentpack string    `json:"agentpack_id"`
	Revisions []string  `json:"revisions"`
	CreatedAt time.Time `json:"created_at"`
}

type PeerReplica struct {
	Node string `json:"node"`
	Mesh bool   `json:"mesh"`
}

var peerName = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func ValidatePeer(node string) error {
	if !peerName.MatchString(node) || node == "." || node == ".." {
		return fmt.Errorf("invalid peer identity")
	}
	return nil
}

func (s *Store) EnrollPeer(node string) error {
	if err := ValidatePeer(node); err != nil {
		return err
	}
	_, err := s.sql.DB.Exec(`INSERT INTO registry_peers(node_id,mesh,removed) VALUES (?,1,0)
 ON CONFLICT(node_id) DO UPDATE SET mesh=1,removed=0`, node)
	return err
}

func (s *Store) EnrollOwnMachine(node string) error {
	if err := ValidatePeer(node); err != nil {
		return err
	}
	_, err := s.sql.DB.Exec("INSERT OR IGNORE INTO registry_peers(node_id) VALUES (?)", node)
	return err
}

func (s *Store) ListPeers() ([]PeerReplica, error) {
	rows, err := s.sql.Reader.Query("SELECT node_id,mesh FROM registry_peers WHERE removed=0 ORDER BY node_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	peers := []PeerReplica{}
	for rows.Next() {
		var p PeerReplica
		if err := rows.Scan(&p.Node, &p.Mesh); err != nil {
			return nil, err
		}
		peers = append(peers, p)
	}
	return peers, rows.Err()
}

func (s *Store) RemovePeer(node string) error {
	if err := ValidatePeer(node); err != nil {
		return err
	}
	_, err := s.sql.DB.Exec(`INSERT INTO registry_peers(node_id,removed) VALUES (?,1)
 ON CONFLICT(node_id) DO UPDATE SET removed=1,mesh=0`, node)
	return err
}

type importedObject struct {
	id            string
	data          []byte
	packageRecord *Package
	revision      *Revision
	name          *nameRevision
	tombstone     *Tombstone
}

func decodeImported(data []byte) (importedObject, error) {
	obj := importedObject{data: append([]byte(nil), data...)}
	sum := sha256.Sum256(data)
	obj.id = hex.EncodeToString(sum[:])
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(data, &shape); err != nil {
		return obj, fmt.Errorf("invalid registry object JSON")
	}
	if _, isName := shape["object_type"]; isName {
		var name nameRevision
		if err := json.Unmarshal(data, &name); err != nil || name.ObjectType != nameObjectType || name.Schema != objectSchema || !validID(name.Agentpack) || strings.TrimSpace(name.Name) == "" || obs.CredentialShaped(name.Name) || name.Node == "" || obs.CredentialShaped(name.Node) || name.CreatedAt.IsZero() || !canonicalObject(data, name) {
			return obj, fmt.Errorf("invalid registry agentpack name object")
		}
		name.ID = obj.id
		obj.name = &name
		return obj, nil
	}
	var revision Revision
	if _, isRevision := shape["manifest"]; isRevision {
		if err := json.Unmarshal(data, &revision); err != nil {
			return obj, fmt.Errorf("invalid registry revision JSON")
		}
		// Phase 3 objects had no schema field; zero denotes that known legacy form.
		if (revision.Schema != 0 && revision.Schema != objectSchema) || !validID(revision.AgentpackID) {
			return obj, fmt.Errorf("invalid registry revision schema or owner")
		}
		cleanManifest, manifestSecrets, err := normalizeTOML([]byte(revision.Manifest))
		if err != nil || !sameTOML(cleanManifest, []byte(revision.Manifest)) {
			return obj, fmt.Errorf("registry revision contains unredacted or invalid manifest data")
		}
		cleanLock, lockSecrets, err := normalizeTOML([]byte(revision.Lock))
		if err != nil || !sameTOML(cleanLock, []byte(revision.Lock)) {
			return obj, fmt.Errorf("registry revision contains unredacted or invalid lock data")
		}
		if revision.NeedsLocalConfiguration != (len(manifestSecrets)+len(lockSecrets) > 0) {
			return obj, fmt.Errorf("registry revision configuration marker mismatch")
		}
		if _, err := validateSnapshot(cleanManifest, cleanLock); err != nil {
			return obj, fmt.Errorf("invalid registry revision snapshot")
		}
		for _, value := range []string{revision.Project, revision.SourceNode, revision.Label} {
			if value != "<local>" && obs.CredentialShaped(value) {
				return obj, fmt.Errorf("registry revision contains credential-shaped metadata")
			}
		}
		if !canonicalObject(data, revision) {
			return obj, fmt.Errorf("registry revision is not canonical")
		}
		obj.revision = &revision
		return obj, nil
	}
	var pkg Package
	if _, isPackage := shape["module"]; isPackage {
		if err := json.Unmarshal(data, &pkg); err != nil {
			return obj, fmt.Errorf("invalid registry package JSON")
		}
		if (pkg.Schema != 0 && pkg.Schema != objectSchema) || pkg.Commit == "" || !validCacheKey(pkg.CacheKey) || (pkg.Kind != string(lockfile.PackageSkill) && pkg.Kind != string(lockfile.PackagePlugin)) || (pkg.ContentHash != "" && !lockfile.ValidContentHash(pkg.ContentHash)) {
			return obj, fmt.Errorf("invalid registry package object")
		}
		for _, value := range []string{pkg.Module, pkg.Commit, pkg.URL, pkg.Owner, pkg.Repo, pkg.Path, pkg.CacheKey, pkg.Name, pkg.SourceNode, pkg.Project} {
			if value != "<local>" && obs.CredentialShaped(value) {
				return obj, fmt.Errorf("registry package contains credential-shaped metadata")
			}
		}
		if !canonicalObject(data, pkg) {
			return obj, fmt.Errorf("registry package is not canonical")
		}
		obj.packageRecord = &pkg
		return obj, nil
	}
	var tombstone Tombstone
	if _, isTombstone := shape["revisions"]; isTombstone {
		if err := json.Unmarshal(data, &tombstone); err != nil {
			return obj, fmt.Errorf("invalid registry tombstone JSON")
		}
		if tombstone.Schema != objectSchema || !validID(tombstone.Agentpack) || tombstone.CreatedAt.IsZero() || !canonicalTombstone(data, tombstone) {
			return obj, fmt.Errorf("invalid registry tombstone")
		}
		for _, revisionID := range tombstone.Revisions {
			if !validDigest(revisionID) {
				return obj, fmt.Errorf("invalid tombstone revision digest")
			}
		}
		obj.tombstone = &tombstone
		return obj, nil
	}
	return obj, fmt.Errorf("unsupported registry object schema")
}

func canonicalObject(data []byte, value any) bool {
	canonical, err := marshalObject(value)
	return err == nil && bytes.Equal(data, canonical)
}

func canonicalTombstone(data []byte, tombstone Tombstone) bool {
	tombstone.ID = ""
	tombstone.Revisions = sorted(tombstone.Revisions)
	canonical, err := json.Marshal(tombstone)
	return err == nil && bytes.Equal(data, canonical)
}

func (s *Store) validateImportReferences(prepared map[string]importedObject) error {
	packages := map[string]Package{}
	revisions := map[string]Revision{}
	names := map[string]nameRevision{}
	tombstones := map[string]bool{}
	for id, obj := range prepared {
		if obj.packageRecord != nil {
			packages[id] = *obj.packageRecord
		}
		if obj.revision != nil {
			revisions[id] = *obj.revision
		}
		if obj.name != nil {
			names[id] = *obj.name
		}
		if obj.tombstone != nil {
			tombstones[obj.tombstone.Agentpack] = true
		}
	}
	getPackage := func(id string) (Package, error) {
		if p, ok := packages[id]; ok {
			return p, nil
		}
		return s.readPackage(id)
	}
	getRevision := func(id string) (Revision, error) {
		if r, ok := revisions[id]; ok {
			return r, nil
		}
		return s.readRevision(id)
	}
	for _, revision := range revisions {
		lock, err := validateSnapshot([]byte(revision.Manifest), []byte(revision.Lock))
		if err != nil || len(lock.Packages) != len(revision.PackageIDs) {
			return fmt.Errorf("registry revision package references do not match lock")
		}
		byModule := map[string]Package{}
		for _, id := range revision.PackageIDs {
			pkg, err := getPackage(id)
			if err != nil {
				return fmt.Errorf("registry revision references unavailable package object")
			}
			byModule[pkg.Module] = pkg
		}
		for _, pin := range lock.Packages {
			pkg, ok := byModule[pin.Module]
			if !ok || pkg.Commit != pin.Commit || pkg.ContentHash != pin.ContentHash || pkg.Kind != string(pin.Kind) || pkg.Direct != pin.Direct || pkg.URL != pin.URL || pkg.Owner != pin.Owner || pkg.Repo != pin.Repo || pkg.Path != pin.Path || pkg.CacheKey != pin.CacheKey || pkg.Name != pin.Name {
				return fmt.Errorf("registry revision package pin mismatch")
			}
		}
		for _, parentID := range revision.ParentIDs {
			parent, err := getRevision(parentID)
			if err != nil || parent.AgentpackID != revision.AgentpackID {
				return fmt.Errorf("registry revision parent is unavailable or has another owner")
			}
		}
	}
	for _, name := range names {
		if tombstones[name.Agentpack] {
			continue
		}
		if hasOwnerRevision(revisions, name.Agentpack) {
			continue
		}
		if _, _, err := s.Get(name.Agentpack); err != nil {
			return fmt.Errorf("registry name object has no agentpack revisions")
		}
	}
	return nil
}

func (s *Store) DeleteAgentpack(id string) (Tombstone, error) {
	var result Tombstone
	err := s.withLock(func() error {
		idx, err := s.readIndex()
		if err != nil {
			return err
		}
		pack, ok := idx.Agentpacks[id]
		if !ok {
			return ErrNotFound
		}
		if _, exists, err := s.tombstoneFor(id); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("agentpack is already deleted")
		}
		result = Tombstone{Schema: objectSchema, Agentpack: id, Revisions: sorted(pack.Revisions), CreatedAt: time.Now().UTC()}
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		result.ID = hex.EncodeToString(sum[:])
		if err := s.storeObject("registry_tombstones", result.ID, data); err != nil {
			return err
		}
		_, err = s.sql.DB.Exec("DELETE FROM registry_heads WHERE agentpack_id=?", id)
		return err
	})
	return result, err
}

func (s *Store) readTombstone(id string) (Tombstone, error) {
	var tombstone Tombstone
	if !validDigest(id) {
		return tombstone, fmt.Errorf("invalid tombstone digest")
	}
	var data []byte
	err := s.sql.Reader.QueryRow("SELECT body FROM registry_tombstones WHERE digest=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return tombstone, os.ErrNotExist
	}
	if err != nil {
		return tombstone, err
	}
	obj, err := decodeImported(data)
	if err != nil || obj.id != id || obj.tombstone == nil {
		return tombstone, fmt.Errorf("invalid registry tombstone object")
	}
	tombstone = *obj.tombstone
	tombstone.ID = id
	return tombstone, nil
}

func (s *Store) tombstoneFor(agentpackID string) (Tombstone, bool, error) {
	entries, err := s.storedIDs("registry_tombstones")
	if err != nil {
		return Tombstone{}, false, err
	}
	for _, id := range entries {
		tombstone, err := s.readTombstone(id)
		if err != nil {
			return Tombstone{}, false, err
		}
		if tombstone.Agentpack == agentpackID {
			return tombstone, true, nil
		}
	}
	return Tombstone{}, false, nil
}

func (s *Store) CollectGarbage() ([]string, error) {
	var removed []string
	err := s.withLock(func() error {
		var tombstones []Tombstone
		entries, err := s.storedIDs("registry_tombstones")
		if err != nil {
			return err
		}
		for _, id := range entries {
			t, err := s.readTombstone(id)
			if err != nil {
				return err
			}
			tombstones = append(tombstones, t)
		}
		for _, tombstone := range tombstones {
			var needed bool
			if err := s.sql.Reader.QueryRow(`SELECT
 EXISTS(SELECT 1 FROM registry_objects WHERE json_extract(body,'$.agentpack_id')=?)
 OR EXISTS(SELECT 1 FROM registry_names WHERE json_extract(body,'$.agentpack_id')=?)
 OR EXISTS(SELECT 1 FROM registry_heads WHERE agentpack_id=?)
 OR EXISTS(SELECT 1 FROM registry_bindings b,registry_tombstones t,json_each(t.body,'$.revisions') r
 WHERE t.digest=? AND b.revision_id=r.value)`, tombstone.Agentpack, tombstone.Agentpack, tombstone.Agentpack, tombstone.ID).Scan(&needed); err != nil {
				return err
			}
			if !needed {
				continue
			}
			// CRR retains deletion clocks, so an offline peer can learn collection without a separate receipt protocol.
			packageIDs := map[string]bool{}
			revisionIDs := map[string]bool{}
			nameIDs := map[string]bool{}
			for _, id := range tombstone.Revisions {
				revisionIDs[id] = true
			}
			objects, err := s.objectIDs()
			if err != nil {
				return err
			}
			for _, id := range objects {
				data, err := s.objectData(id)
				if err != nil {
					return err
				}
				obj, err := decodeImported(data)
				if err != nil || obj.id != id {
					return fmt.Errorf("invalid registry public object")
				}
				if obj.revision != nil && obj.revision.AgentpackID == tombstone.Agentpack {
					revisionIDs[id] = true
				}
				if obj.name != nil && obj.name.Agentpack == tombstone.Agentpack {
					nameIDs[id] = true
				}
			}
			for nameID := range nameIDs {
				if err := s.removeObject(nameID); err != nil {
					return err
				}
				removed = append(removed, nameID)
			}
			for revisionID := range revisionIDs {
				if revision, err := s.readRevision(revisionID); err == nil {
					for _, packageID := range revision.PackageIDs {
						packageIDs[packageID] = true
					}
				} else if !os.IsNotExist(err) {
					return err
				}
			}
			for revisionID := range revisionIDs {
				if err := s.removeObject(revisionID); err != nil {
					return err
				}
				removed = append(removed, revisionID)
				if _, err := s.sql.DB.Exec("DELETE FROM registry_bindings WHERE revision_id=?", revisionID); err != nil {
					return err
				}
			}
			for packageID := range packageIDs {
				referenced, err := s.packageReferencedOutside(tombstone.Agentpack, packageID)
				if err != nil {
					return err
				}
				if referenced {
					continue
				}
				if err := s.removeObject(packageID); err != nil {
					return err
				}
				removed = append(removed, packageID)
			}
			if _, err := s.sql.DB.Exec("DELETE FROM registry_heads WHERE agentpack_id=?", tombstone.Agentpack); err != nil {
				return err
			}
		}
		sort.Strings(removed)
		return nil
	})
	return removed, err
}

func (s *Store) packageReferencedOutside(deletedAgentpack, packageID string) (bool, error) {
	entries, err := s.objectIDs()
	if err != nil {
		return true, err
	}
	for _, id := range entries {
		var shape Revision
		if err := s.readObject(id, &shape); err != nil {
			return true, err
		}
		if shape.AgentpackID == "" || shape.AgentpackID == deletedAgentpack {
			continue
		}
		r, err := s.readRevision(id)
		if err != nil {
			return true, err
		}
		if slices.Contains(r.PackageIDs, packageID) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) revisionHeads(ids []string) ([]string, error) {
	parents := map[string]bool{}
	for _, id := range ids {
		r, err := s.readRevision(id)
		if err != nil {
			return nil, err
		}
		for _, parent := range r.ParentIDs {
			parents[parent] = true
		}
	}
	heads := make([]string, 0, len(ids))
	for _, id := range ids {
		if !parents[id] {
			heads = append(heads, id)
		}
	}
	sort.Strings(heads)
	return heads, nil
}

func (s *Store) Heads(id string) ([]string, error) {
	pack, _, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), pack.Heads...), nil
}
