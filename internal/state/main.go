package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type MainState struct {
	MainNode    string
	ArchiveNode string
	MainName    string
	ArchivePath string
	ArchiveName string
}

func (s *Store) Main(ctx context.Context) (MainState, error) {
	var result MainState
	err := s.Reader.QueryRowContext(ctx, `SELECT
 coalesce((SELECT value FROM mesh_settings WHERE key='main_node'),''),
 coalesce((SELECT value FROM mesh_settings WHERE key='archive_node'),'')`).Scan(&result.MainNode, &result.ArchiveNode)
	if err != nil {
		return result, err
	}
	err = s.Reader.QueryRowContext(ctx, `SELECT
 coalesce((SELECT name FROM mesh_nodes WHERE node_id=? AND name<>''),?),
 coalesce((SELECT name FROM mesh_nodes WHERE node_id=? AND name<>''),?),
 coalesce((SELECT value FROM node_settings WHERE node_id=? AND key='archive_path'),'$AGENTPACK_HOME/archive.db')`,
		result.MainNode, result.MainNode, result.ArchiveNode, result.ArchiveNode, result.ArchiveNode).Scan(&result.MainName, &result.ArchiveName, &result.ArchivePath)
	return result, err
}

var ErrMainChanged = errors.New("main or archive gateway changed; review the current roles before trying again")

// SetMain compares the role shown by the CLI before committing its confirmed switch.
func (s *Store) SetMain(ctx context.Context, expected MainState, home string) error {
	path, err := filepath.Abs(filepath.Join(home, "archive.db"))
	if err != nil {
		return err
	}
	name, err := os.Hostname()
	if err != nil {
		return err
	}
	site, err := s.SiteID(ctx)
	if err != nil {
		return err
	}
	return Transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var main, archive, local string
		if err := tx.QueryRowContext(ctx, "SELECT coalesce((SELECT value FROM mesh_settings WHERE key='main_node'),''),coalesce((SELECT value FROM mesh_settings WHERE key='archive_node'),''),node_id FROM local_node WHERE id=1").Scan(&main, &archive, &local); err != nil {
			return err
		}
		if main != expected.MainNode || archive != expected.ArchiveNode {
			return ErrMainChanged
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_settings(node_id,key,value) VALUES (?,'archive_path',?)
 ON CONFLICT(node_id,key) DO UPDATE SET value=excluded.value WHERE value<>excluded.value`, local, path); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO mesh_nodes(node_id,name,site_id,schema_version) VALUES (?,?,?,?)
 ON CONFLICT(node_id) DO UPDATE SET name=excluded.name,site_id=excluded.site_id,schema_version=excluded.schema_version
 WHERE name IS NOT excluded.name OR site_id IS NOT excluded.site_id OR schema_version IS NOT excluded.schema_version`, local, name, site, SchemaVersion()); err != nil {
			return err
		}
		if main == local {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO mesh_settings(key,value) VALUES ('main_node',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", local); err != nil {
			return err
		}
		if main == "" && archive == "" {
			_, err := tx.ExecContext(ctx, "INSERT INTO mesh_settings(key,value) VALUES ('archive_node',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", local)
			return err
		}
		return nil
	})
}

func (s *Store) ArchiveReady(ctx context.Context, home string, empty bool) error {
	roles, err := s.Main(ctx)
	if err != nil {
		return err
	}
	local, err := s.NodeID(ctx)
	if err != nil {
		return err
	}
	if roles.MainNode != local {
		return errors.New("run archive-ready on the main gateway")
	}
	if !empty {
		if err := VerifyArchive(home); err != nil {
			return err
		}
	}
	return Transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var main string
		if err := tx.QueryRowContext(ctx, "SELECT value FROM mesh_settings WHERE key='main_node'").Scan(&main); err != nil {
			return err
		}
		if main != local {
			return ErrMainChanged
		}
		if empty {
			var archived bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sessions WHERE archived_at>0)").Scan(&archived); err != nil {
				return err
			}
			if archived {
				return errors.New("cannot use --empty while archived sessions exist")
			}
			archive, err := OpenArchive(home)
			if err != nil {
				return err
			}
			if err := archive.Close(); err != nil {
				return err
			}
		}
		archive, err := ReadArchive(home)
		if err != nil {
			return err
		}
		defer archive.Close()
		rows, err := tx.QueryContext(ctx, "SELECT id,archived_seq FROM sessions WHERE archived_at>0 ORDER BY id")
		if err != nil {
			return err
		}
		defer rows.Close()
		var short []string
		for rows.Next() {
			var id string
			var want, have int64
			if err := rows.Scan(&id, &want); err != nil {
				return err
			}
			if err := archive.QueryRowContext(ctx, "SELECT coalesce(max(seq),0) FROM conversation_events WHERE session_id=?", id).Scan(&have); err != nil {
				return err
			}
			if have < want {
				short = append(short, fmt.Sprintf("%s (%d of %d)", id, have, want))
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(short) > 0 {
			return fmt.Errorf("archive history is incomplete for %s", strings.Join(short, ", "))
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO mesh_settings(key,value) VALUES ('archive_node',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", local)
		return err
	})
}
