package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/lockfile"
)

type Inventory struct {
	Schema     int      `json:"schema"`
	Objects    []string `json:"objects"`
	Tombstones []string `json:"tombstones"`
}

type InventoryDelta struct {
	Objects    []string `json:"objects"`
	Tombstones []string `json:"tombstones"`
}

type Tombstone struct {
	ID        string    `json:"id"`
	Schema    int       `json:"schema"`
	Agentpack string    `json:"agentpack_id"`
	Revisions []string  `json:"revisions"`
	CreatedAt time.Time `json:"created_at"`
}

type peerState struct {
	Peers    []string            `json:"peers"`
	Receipts map[string][]string `json:"receipts"`
}

type PeerReplica struct {
	Node         string   `json:"node"`
	ReceiptCount int      `json:"receipt_count"`
	Digests      []string `json:"digests"`
}

var peerName = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func (s *Store) EnrollPeer(node string) error {
	if !peerName.MatchString(node) || node == "." || node == ".." {
		return fmt.Errorf("invalid peer identity")
	}
	return s.withLock(func() error {
		state, err := s.readPeerState()
		if err != nil {
			return err
		}
		state.Peers = appendUnique(state.Peers, node)
		sort.Strings(state.Peers)
		return s.writePeerState(state)
	})
}

func (s *Store) ListPeers() ([]PeerReplica, error) {
	state, err := s.readPeerState()
	if err != nil {
		return nil, err
	}
	peers := make([]PeerReplica, 0, len(state.Peers))
	for _, node := range state.Peers {
		peers = append(peers, PeerReplica{Node: node, ReceiptCount: len(state.Receipts[node]), Digests: slices.Clone(state.Receipts[node])})
	}
	return peers, nil
}

func (s *Store) RemovePeer(node string) error {
	if !peerName.MatchString(node) || node == "." || node == ".." {
		return fmt.Errorf("invalid peer identity")
	}
	return s.withLock(func() error {
		state, err := s.readPeerState()
		if err != nil {
			return err
		}
		state.Peers = slices.DeleteFunc(state.Peers, func(peer string) bool { return peer == node })
		delete(state.Receipts, node)
		return s.writePeerState(state)
	})
}

func (s *Store) Inventory() (Inventory, error) {
	var inventory Inventory
	objects, err := os.ReadDir(filepath.Join(s.root, objectsDir))
	if err != nil {
		return inventory, err
	}
	inventory = Inventory{Schema: objectSchema, Objects: []string{}, Tombstones: []string{}}
	tombstoned := map[string]bool{}
	tombstoneEntries, err := os.ReadDir(filepath.Join(s.root, tombstonesDir))
	if err != nil {
		return Inventory{}, err
	}
	for _, entry := range tombstoneEntries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		tombstone, err := s.readTombstone(id)
		if err != nil {
			return Inventory{}, err
		}
		tombstoned[tombstone.Agentpack] = true
		inventory.Tombstones = append(inventory.Tombstones, id)
	}
	activeRevisions := map[string]bool{}
	activePackages := map[string]bool{}
	referencedPackages := map[string]bool{}
	activeAgentpacks := map[string]bool{}
	publicObjects := map[string]importedObject{}
	for _, entry := range objects {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		data, err := s.readPublicObject(id)
		if err != nil {
			return Inventory{}, err
		}
		obj, err := decodeImported(data)
		if err != nil {
			return Inventory{}, err
		}
		publicObjects[id] = obj
		if obj.revision != nil {
			for _, packageID := range obj.revision.PackageIDs {
				referencedPackages[packageID] = true
			}
		}
		if obj.revision != nil && !tombstoned[obj.revision.AgentpackID] {
			activeRevisions[id] = true
			activeAgentpacks[obj.revision.AgentpackID] = true
			for _, packageID := range obj.revision.PackageIDs {
				activePackages[packageID] = true
			}
		}
	}
	for id, obj := range publicObjects {
		if obj.packageRecord != nil && !referencedPackages[id] {
			activePackages[id] = true
		}
	}
	for id, obj := range publicObjects {
		if obj.revision != nil && activeRevisions[id] || obj.packageRecord != nil && activePackages[id] || obj.name != nil && activeAgentpacks[obj.name.Agentpack] {
			inventory.Objects = append(inventory.Objects, id)
		}
	}
	sort.Strings(inventory.Objects)
	sort.Strings(inventory.Tombstones)
	return inventory, nil
}

func (s *Store) MissingFrom(remote Inventory) (InventoryDelta, error) {
	if remote.Schema != objectSchema {
		return InventoryDelta{}, fmt.Errorf("unsupported registry inventory schema")
	}
	local, err := s.Inventory()
	if err != nil {
		return InventoryDelta{}, err
	}
	knownObjects, knownTombstones := map[string]bool{}, map[string]bool{}
	for _, id := range local.Objects {
		knownObjects[id] = true
	}
	for _, id := range local.Tombstones {
		knownTombstones[id] = true
	}
	delta := InventoryDelta{Objects: []string{}, Tombstones: []string{}}
	for _, id := range remote.Objects {
		if !validDigest(id) {
			return InventoryDelta{}, fmt.Errorf("invalid registry inventory digest")
		}
		if !knownObjects[id] {
			delta.Objects = append(delta.Objects, id)
		}
	}
	for _, id := range remote.Tombstones {
		if !validDigest(id) {
			return InventoryDelta{}, fmt.Errorf("invalid registry inventory tombstone digest")
		}
		if !knownTombstones[id] {
			delta.Tombstones = append(delta.Tombstones, id)
		}
	}
	delta.Objects = uniqueDigests(delta.Objects)
	delta.Tombstones = uniqueDigests(delta.Tombstones)
	return delta, nil
}

func (s *Store) ExportObject(id string) ([]byte, error) {
	path, err := s.objectPath(id)
	if err != nil {
		return nil, err
	}
	data, err := readRegular(path)
	if os.IsNotExist(err) {
		path = filepath.Join(s.root, tombstonesDir, id+".json")
		data, err = readRegular(path)
		if err == nil {
			if _, err = s.readTombstone(id); err != nil {
				return nil, err
			}
		}
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return data, err
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.readPublicObject(id); err != nil {
		return nil, err
	}
	return data, nil
}

// ImportObjects validates the complete batch before publishing any object.
func (s *Store) ImportObjects(documents map[string][]byte) ([]string, error) {
	prepared := make(map[string]importedObject, len(documents))
	ids := make([]string, 0, len(documents))
	for expectedID, data := range documents {
		obj, err := decodeImported(data)
		if err != nil {
			return nil, err
		}
		if !validDigest(expectedID) || obj.id != expectedID {
			return nil, fmt.Errorf("registry object digest mismatch")
		}
		if _, exists := prepared[obj.id]; exists {
			continue
		}
		prepared[obj.id] = obj
		ids = append(ids, obj.id)
	}
	sort.Strings(ids)
	return ids, s.withLock(func() error {
		for _, id := range ids {
			obj := prepared[id]
			if obj.tombstone != nil {
				continue
			}
			if obj.revision != nil && obj.revision.Schema != 0 && obj.revision.Schema != objectSchema {
				return fmt.Errorf("unsupported registry object schema")
			}
			if obj.packageRecord != nil && obj.packageRecord.Schema != 0 && obj.packageRecord.Schema != objectSchema {
				return fmt.Errorf("unsupported registry object schema")
			}
		}
		if err := s.validateImportReferences(prepared); err != nil {
			return err
		}
		// Tombstones keep stale replicas hidden; accepting their bytes lets peers converge and acknowledge deletion.
		for _, id := range ids {
			obj := prepared[id]
			var path string
			if obj.tombstone != nil {
				path = filepath.Join(s.root, tombstonesDir, id+".json")
			} else {
				path = filepath.Join(s.root, objectsDir, id+".json")
			}
			if err := writeExclusiveOrVerify(path, obj.data); err != nil {
				return err
			}
		}
		if len(ids) == 0 {
			return nil
		}
		idx, err := s.rebuildIndex()
		if err != nil {
			return err
		}
		for _, pack := range idx.Agentpacks {
			if err := s.writeMetadata(pack); err != nil {
				return err
			}
		}
		return s.writeIndexUnlocked(idx)
	})
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
		if err := json.Unmarshal(data, &name); err != nil || name.ObjectType != nameObjectType || name.Schema != objectSchema || !validID(name.Agentpack) || strings.TrimSpace(name.Name) == "" || credentialShaped(name.Name) || name.Node == "" || credentialShaped(name.Node) || name.CreatedAt.IsZero() || !canonicalObject(data, name) {
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
		if err != nil || !bytes.Equal(cleanManifest, []byte(revision.Manifest)) {
			return obj, fmt.Errorf("registry revision contains unredacted or invalid manifest data")
		}
		cleanLock, lockSecrets, err := normalizeTOML([]byte(revision.Lock))
		if err != nil || !bytes.Equal(cleanLock, []byte(revision.Lock)) {
			return obj, fmt.Errorf("registry revision contains unredacted or invalid lock data")
		}
		if revision.NeedsLocalConfiguration != (len(manifestSecrets)+len(lockSecrets) > 0) {
			return obj, fmt.Errorf("registry revision configuration marker mismatch")
		}
		if _, err := validateSnapshot(cleanManifest, cleanLock); err != nil {
			return obj, fmt.Errorf("invalid registry revision snapshot")
		}
		for _, value := range []string{revision.Project, revision.SourceNode, revision.Label} {
			if value != "<local>" && credentialShaped(value) {
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
			if value != "<local>" && credentialShaped(value) {
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
			return fmt.Errorf("registry name object belongs to a deleted agentpack")
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

func (s *Store) Acknowledge(peer string, digests []string) error {
	if !peerName.MatchString(peer) || peer == "." || peer == ".." {
		return fmt.Errorf("invalid peer identity")
	}
	return s.withLock(func() error {
		state, err := s.readPeerState()
		if err != nil {
			return err
		}
		if !slices.Contains(state.Peers, peer) {
			return fmt.Errorf("peer is not enrolled")
		}
		for _, id := range digests {
			if _, err := s.ExportObject(id); err != nil {
				return err
			}
			state.Receipts[peer] = appendUnique(state.Receipts[peer], id)
		}
		sort.Strings(state.Receipts[peer])
		return s.writePeerState(state)
	})
}

func (s *Store) readPeerState() (peerState, error) {
	state := peerState{Peers: []string{}, Receipts: map[string][]string{}}
	data, err := readRegular(filepath.Join(s.root, peersDir, "state.json"))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil || state.Receipts == nil {
		return peerState{}, fmt.Errorf("invalid registry peer state")
	}
	for _, peer := range state.Peers {
		if !peerName.MatchString(peer) || peer == "." || peer == ".." {
			return peerState{}, fmt.Errorf("invalid registry peer state")
		}
	}
	for peer, receipts := range state.Receipts {
		if !slices.Contains(state.Peers, peer) {
			return peerState{}, fmt.Errorf("registry receipts exist for an unenrolled peer")
		}
		for _, id := range receipts {
			if !validDigest(id) {
				return peerState{}, fmt.Errorf("invalid registry receipt digest")
			}
		}
	}
	return state, nil
}

func (s *Store) writePeerState(state peerState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.root, peersDir, "state.json"), data, 0o600)
}

func (s *Store) DeleteAgentpack(id string) (Tombstone, error) {
	var result Tombstone
	err := s.withLock(func() error {
		idx, err := s.readIndexUnlocked()
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
		path := filepath.Join(s.root, tombstonesDir, result.ID+".json")
		if err := writeExclusiveOrVerify(path, data); err != nil {
			return err
		}
		delete(idx.Agentpacks, id)
		if err := s.writeIndexUnlocked(idx); err != nil {
			return err
		}
		_ = os.Remove(filepath.Join(s.root, metadataDir, id+".json"))
		return nil
	})
	return result, err
}

func (s *Store) readTombstone(id string) (Tombstone, error) {
	var tombstone Tombstone
	if !validDigest(id) {
		return tombstone, fmt.Errorf("invalid tombstone digest")
	}
	data, err := readRegular(filepath.Join(s.root, tombstonesDir, id+".json"))
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
	entries, err := os.ReadDir(filepath.Join(s.root, tombstonesDir))
	if err != nil {
		return Tombstone{}, false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
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
		state, err := s.readPeerState()
		if err != nil {
			return err
		}
		var tombstones []Tombstone
		entries, err := os.ReadDir(filepath.Join(s.root, tombstonesDir))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			t, err := s.readTombstone(strings.TrimSuffix(entry.Name(), ".json"))
			if err != nil {
				return err
			}
			tombstones = append(tombstones, t)
		}
		for _, tombstone := range tombstones {
			ready := true
			for _, peer := range state.Peers {
				if !slices.Contains(state.Receipts[peer], tombstone.ID) {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			packageIDs := map[string]bool{}
			revisionIDs := map[string]bool{}
			nameIDs := map[string]bool{}
			for _, id := range tombstone.Revisions {
				revisionIDs[id] = true
			}
			objects, err := os.ReadDir(filepath.Join(s.root, objectsDir))
			if err != nil {
				return err
			}
			for _, entry := range objects {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				id := strings.TrimSuffix(entry.Name(), ".json")
				data, err := readRegular(filepath.Join(s.root, objectsDir, entry.Name()))
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
				if err := removeDurable(filepath.Join(s.root, objectsDir, nameID+".json")); err != nil {
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
				if err := removeDurable(filepath.Join(s.root, objectsDir, revisionID+".json")); err != nil {
					return err
				}
				removed = append(removed, revisionID)
				if err := os.Remove(filepath.Join(s.root, bindingsDir, revisionID+".json")); err != nil && !os.IsNotExist(err) {
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
				if err := removeDurable(filepath.Join(s.root, objectsDir, packageID+".json")); err != nil {
					return err
				}
				removed = append(removed, packageID)
			}
			if err := os.Remove(filepath.Join(s.root, metadataDir, tombstone.Agentpack+".json")); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		idx, err := s.rebuildIndex()
		if err != nil {
			return err
		}
		if err := s.writeIndexUnlocked(idx); err != nil {
			return err
		}
		sort.Strings(removed)
		return nil
	})
	return removed, err
}

func (s *Store) packageReferencedOutside(deletedAgentpack, packageID string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, objectsDir))
	if err != nil {
		return true, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
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

func (s *Store) readPublicObject(id string) ([]byte, error) {
	path, err := s.objectPath(id)
	if err != nil {
		return nil, err
	}
	data, err := readRegular(path)
	if err != nil {
		return nil, err
	}
	obj, err := decodeImported(data)
	if err != nil || obj.id != id || obj.tombstone != nil {
		return nil, fmt.Errorf("invalid registry public object")
	}
	if obj.revision != nil {
		if _, err := s.readRevision(id); err != nil {
			return nil, err
		}
	} else if obj.name != nil {
		if _, err := s.readName(id); err != nil {
			return nil, err
		}
	} else if _, err := s.readPackage(id); err != nil {
		return nil, err
	}
	return data, nil
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

func uniqueDigests(values []string) []string {
	values = slices.Clone(values)
	sort.Strings(values)
	return slices.Compact(values)
}
