package registry

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

// ValidateChanges checks public records before a peer page can enter the local database.
func ValidateChanges(store *state.Store, changes []state.Change) error {
	for _, change := range changes {
		if !strings.HasPrefix(change.Table, "registry_") || change.Column != "body" {
			continue
		}
		var body string
		if change.Value.Type != "text" || json.Unmarshal(change.Value.Data, &body) != nil {
			return fmt.Errorf("invalid registry change body")
		}
		rows, err := store.Reader.Query("SELECT cell FROM crsql_unpack_columns(?)", change.PK)
		if err != nil {
			return err
		}
		var id string
		if !rows.Next() || rows.Scan(&id) != nil || rows.Next() {
			rows.Close()
			return fmt.Errorf("invalid registry change key")
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		switch change.Table {
		case "registry_objects", "registry_names", "registry_tombstones":
			obj, err := decodeImported([]byte(body))
			if err != nil {
				return err
			}
			if !validDigest(id) || id != obj.id {
				return fmt.Errorf("registry object digest mismatch")
			}
			if (change.Table == "registry_names") != (obj.name != nil) || (change.Table == "registry_tombstones") != (obj.tombstone != nil) {
				return fmt.Errorf("registry object type mismatch")
			}
		case "registry_heads":
			var pack Agentpack
			if json.Unmarshal([]byte(body), &pack) != nil || !validID(id) || pack.ID != id || obs.CredentialShaped(pack.Name) {
				return fmt.Errorf("invalid registry heads")
			}
			for _, revision := range append(pack.Revisions, pack.Heads...) {
				if !validDigest(revision) {
					return fmt.Errorf("invalid registry head digest")
				}
			}
		case "registry_drafts":
			if !validID(id) {
				return fmt.Errorf("invalid registry draft key")
			}
			draft, err := decodeDraft(id, []byte(body))
			if err != nil {
				return err
			}
			if draft.Node == "local" {
				return fmt.Errorf("replicated draft requires a stable target node")
			}
		default:
			return fmt.Errorf("invalid replicated registry table")
		}
	}
	return nil
}
