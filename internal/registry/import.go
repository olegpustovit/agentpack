package registry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

// Legacy files are removed only after the complete verified import is durable.
func (s *Store) importLegacy() error {
	return s.withLock(func() error {
		peerFile := filepath.Join(s.root, peersDir, "state.json")
		if info, err := os.Lstat(filepath.Dir(peerFile)); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("invalid legacy registry peer directory")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		var peers struct {
			Peers, Mesh, Left []string
			Receipts          map[string][]string
		}
		peerData, peerErr := readRegular(peerFile)
		if peerErr == nil {
			if json.Unmarshal(peerData, &peers) != nil || peers.Receipts == nil {
				return fmt.Errorf("invalid legacy registry peers")
			}
			for _, node := range append(slices.Clone(peers.Peers), peers.Left...) {
				if err := ValidatePeer(node); err != nil {
					return err
				}
			}
			for _, node := range peers.Mesh {
				if !slices.Contains(peers.Peers, node) {
					return fmt.Errorf("invalid legacy mesh approval")
				}
			}
			for node, receipts := range peers.Receipts {
				if !slices.Contains(peers.Peers, node) {
					return fmt.Errorf("invalid legacy registry receipts")
				}
				for _, id := range receipts {
					if !validDigest(id) {
						return fmt.Errorf("invalid legacy registry receipt")
					}
				}
			}
		} else if !os.IsNotExist(peerErr) {
			return peerErr
		}
		type source struct {
			path, id, kind string
			data           []byte
		}
		var sources []source
		prepared := map[string]importedObject{}
		for _, kind := range []string{objectsDir, tombstonesDir, metadataDir, bindingsDir, draftsDir} {
			dir := filepath.Join(s.root, kind)
			info, err := os.Lstat(dir)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("invalid legacy registry directory")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				path := filepath.Join(dir, entry.Name())
				data, err := readRegular(path)
				if err != nil {
					return err
				}
				id := strings.TrimSuffix(entry.Name(), ".json")
				switch kind {
				case objectsDir, tombstonesDir:
					obj, err := decodeImported(data)
					if err != nil {
						return err
					}
					if !validDigest(id) || obj.id != id {
						return fmt.Errorf("legacy registry object digest mismatch")
					}
					if (kind == tombstonesDir) != (obj.tombstone != nil) {
						return fmt.Errorf("legacy registry object type mismatch")
					}
					prepared[id] = obj
				case metadataDir:
					var pack Agentpack
					if json.Unmarshal(data, &pack) != nil || !validID(id) || pack.ID != id || obs.CredentialShaped(pack.Name) {
						return fmt.Errorf("invalid legacy registry metadata")
					}
				case bindingsDir:
					var b bindings
					if !validDigest(id) || json.Unmarshal(data, &b) != nil || b.Values == nil {
						return fmt.Errorf("invalid legacy registry bindings")
					}
				case draftsDir:
					if !validID(id) {
						return fmt.Errorf("invalid legacy draft id")
					}
					if _, err := decodeDraft(id, data); err != nil {
						return err
					}
				}
				sources = append(sources, source{path: path, id: id, kind: kind, data: data})
			}
		}
		if err := s.validateImportReferences(prepared); err != nil {
			return err
		}
		for _, src := range sources {
			switch src.kind {
			case objectsDir, tombstonesDir:
				obj := prepared[src.id]
				table := "registry_objects"
				if obj.name != nil {
					table = "registry_names"
				} else if obj.tombstone != nil {
					table = "registry_tombstones"
				}
				if err := s.storeObject(table, src.id, src.data); err != nil {
					return err
				}
			case metadataDir:
				var pack Agentpack
				if err := json.Unmarshal(src.data, &pack); err != nil {
					return err
				}
				if err := s.writeMetadata(pack); err != nil {
					return err
				}
			case bindingsDir:
				if _, err := s.sql.DB.Exec("INSERT OR IGNORE INTO registry_bindings(revision_id,body) VALUES (?,?)", src.id, string(src.data)); err != nil {
					return err
				}
				saved, _, err := s.readBindings(src.id)
				if err != nil {
					return err
				}
				if !bytes.Equal(saved, src.data) {
					return fmt.Errorf("legacy registry bindings conflict with the local database; source files were kept")
				}
			case draftsDir:
				if err := state.Transaction(context.Background(), s.sql.DB, func(tx *sql.Tx) error {
					draft, err := decodeDraft(src.id, src.data)
					if err != nil {
						return err
					}
					data := src.data
					if draft.Node == "local" {
						if err := tx.QueryRow("SELECT node_id FROM local_node WHERE id=1").Scan(&draft.Node); err != nil {
							return err
						}
						data, err = json.Marshal(draft)
						if err != nil {
							return err
						}
					}
					if _, err := tx.Exec("INSERT OR IGNORE INTO registry_drafts(id,body) VALUES (?,?)", src.id, string(data)); err != nil {
						return err
					}
					var saved []byte
					if err := tx.QueryRow("SELECT body FROM registry_drafts WHERE id=?", src.id).Scan(&saved); err != nil {
						return err
					}
					if !bytes.Equal(saved, data) {
						return fmt.Errorf("legacy registry draft conflicts with the local database; source files were kept")
					}
					return nil
				}); err != nil {
					return err
				}
			}
		}
		if _, err := s.readIndex(); err != nil {
			return err
		}
		for _, node := range peers.Peers {
			if _, err := s.sql.DB.Exec("INSERT OR IGNORE INTO registry_peers(node_id,mesh) VALUES (?,?)", node, slices.Contains(peers.Mesh, node)); err != nil {
				return err
			}
		}
		for _, node := range peers.Left {
			if _, err := s.sql.DB.Exec("INSERT OR IGNORE INTO registry_peers(node_id,removed) VALUES (?,1)", node); err != nil {
				return err
			}
		}
		if peerErr == nil {
			if err := removeDurable(peerFile); err != nil {
				return err
			}
			_ = os.Remove(filepath.Dir(peerFile))
		}
		for _, src := range sources {
			if err := removeDurable(src.path); err != nil {
				return err
			}
		}
		for _, kind := range []string{objectsDir, tombstonesDir, metadataDir, bindingsDir, draftsDir} {
			// Unknown support files are retained; only an empty legacy directory disappears.
			_ = os.Remove(filepath.Join(s.root, kind))
		}
		if err := os.Remove(filepath.Join(s.root, indexName)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
}
