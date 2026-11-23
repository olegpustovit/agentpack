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
		rows, err := tx.QueryContext(ctx, `SELECT printf('"%w"',s.name),printf('"%w"',c.name)
 FROM sqlite_schema s JOIN pragma_table_info(s.name) c
 WHERE s.type='table' AND s.name<>'local_node_aliases'
 AND s.name NOT GLOB '*__crsql_*'
 AND (c.name='node_id' OR c.name GLOB '*_node')`)
		if err != nil {
			return err
		}
		type nodeColumn struct{ table, column string }
		var columns []nodeColumn
		for rows.Next() {
			var c nodeColumn
			if err := rows.Scan(&c.table, &c.column); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, c)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, c := range columns {
			if _, err := tx.ExecContext(ctx, "UPDATE "+c.table+" SET "+c.column+"=? WHERE "+c.column+"=?", id, old); err != nil {
				return fmt.Errorf("bind node in %s: %w", c.table, err)
			}
		}
		for _, query := range []string{
			"UPDATE registry_drafts SET body=json_set(body,'$.node',?) WHERE json_valid(body) AND json_extract(body,'$.node')=?",
			"UPDATE agent_credentials SET value=json_set(value,'$.node',?) WHERE json_valid(value) AND json_extract(value,'$.node')=?",
		} {
			if _, err := tx.ExecContext(ctx, query, id, old); err != nil {
				return fmt.Errorf("bind node in JSON records: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE mesh_settings SET value=? WHERE key IN ('main_node','archive_node') AND value=?", id, old); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO local_node_aliases(node_id) VALUES (?)", old)
		return err
	})
}
