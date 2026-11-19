package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// LocalNodeSQL reads inside the caller's transaction, including already-running workers.
const LocalNodeSQL = "(SELECT node_id FROM local_node WHERE id=1)"

func (s *Store) NodeID(ctx context.Context) (string, error) {
	var id string
	err := s.Reader.QueryRowContext(ctx, "SELECT node_id FROM local_node WHERE id=1").Scan(&id)
	return id, err
}

// BindNode replaces the offline site identity once, when Tailscale identifies this machine.
func (s *Store) BindNode(ctx context.Context, id string) error {
	if id == "" || id == "local" || strings.HasPrefix(id, "site:") || len(id) > 256 || strings.ContainsAny(id, " /\\\t\r\n\x00") {
		return errors.New("invalid stable node identity")
	}
	return Transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, "SELECT node_id FROM local_node WHERE id=1").Scan(&old); err != nil {
			return err
		}
		if old == id {
			return nil
		}
		if !strings.HasPrefix(old, "site:") {
			return errors.New("state belongs to a different stable node identity")
		}
		for _, table := range []string{"node_settings", "limits", "limit_samples", "usage_hourly", "sessions", "issue_counts", "mesh_nodes", "cache_locations", "blob_locations"} {
			if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET node_id=? WHERE node_id=?", id, old); err != nil {
				return fmt.Errorf("bind node in %s: %w", table, err)
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE registry_drafts SET body=json_set(body,'$.node',?) WHERE json_extract(body,'$.node')=?", id, old); err != nil {
			return fmt.Errorf("bind node in registry drafts: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE mesh_settings SET value=? WHERE key IN ('main_node','archive_node') AND value=?", id, old); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO local_node_aliases(node_id) VALUES (?)", old); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE local_node SET node_id=? WHERE id=1", id)
		return err
	})
}
