package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

var ErrInvalidChange = errors.New("invalid registry change")

// A peer's registry change that validateChanges refuses is quarantined.
func init() {
	state.RegisterValidation("registry_", func(store *state.Store, change state.Change) (string, error) {
		err := validateChanges(store, []state.Change{change})
		if errors.Is(err, ErrInvalidChange) {
			return err.Error(), nil
		}
		return "", err
	})
}

// validateChanges checks public records before a peer page can enter the local database.
func validateChanges(store *state.Store, changes []state.Change) error {
	for _, change := range changes {
		if !strings.HasPrefix(change.Table, "registry_") || change.Column != "body" {
			continue
		}
		var body string
		if change.Value.Type != "text" || json.Unmarshal(change.Value.Data, &body) != nil {
			return fmt.Errorf("%w: invalid registry change body", ErrInvalidChange)
		}
		rows, err := store.Reader.Query("SELECT cell FROM crsql_unpack_columns(?)", change.PK)
		if err != nil {
			return err
		}
		var id string
		validKey := rows.Next() && rows.Scan(&id) == nil && !rows.Next()
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if !validKey {
			return fmt.Errorf("%w: invalid registry change key", ErrInvalidChange)
		}
		switch change.Table {
		case "registry_objects", "registry_names", "registry_tombstones":
			obj, err := decodeImported([]byte(body))
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidChange, err)
			}
			if !validDigest(id) || id != obj.id {
				return fmt.Errorf("%w: registry object digest mismatch", ErrInvalidChange)
			}
			if (change.Table == "registry_names") != (obj.name != nil) || (change.Table == "registry_tombstones") != (obj.tombstone != nil) {
				return fmt.Errorf("%w: registry object type mismatch", ErrInvalidChange)
			}
		case "registry_heads":
			var pack Agentpack
			if json.Unmarshal([]byte(body), &pack) != nil || !validID(id) || pack.ID != id || obs.CredentialShaped(pack.Name) {
				return fmt.Errorf("%w: invalid registry heads", ErrInvalidChange)
			}
			for _, revision := range append(pack.Revisions, pack.Heads...) {
				if !validDigest(revision) {
					return fmt.Errorf("%w: invalid registry head digest", ErrInvalidChange)
				}
			}
		case "registry_drafts":
			if !validID(id) {
				return fmt.Errorf("%w: invalid registry draft key", ErrInvalidChange)
			}
			draft, err := decodeDraft(id, []byte(body))
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidChange, err)
			}
			if draft.Node == "local" {
				return fmt.Errorf("%w: replicated draft requires a stable target node", ErrInvalidChange)
			}
		default:
			return fmt.Errorf("%w: invalid replicated registry table", ErrInvalidChange)
		}
	}
	return nil
}
