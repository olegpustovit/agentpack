package state

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Mesh owns the replicated machine directory (mesh_nodes) and mesh-wide settings (mesh_settings).
type Mesh struct{ s *Store }

func (s *Store) Mesh() Mesh { return Mesh{s} }

type MeshNode struct {
	ID, Name, AppVersion string
	Schema               int
}

// LocalArchiveSQL is true when this machine is both Main and the archive holder.
const LocalArchiveSQL = `coalesce((SELECT value FROM mesh_settings WHERE key='main_node'),'')=` + LocalNodeSQL + `
 AND coalesce((SELECT value FROM mesh_settings WHERE key='archive_node'),'')=` + LocalNodeSQL

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// upsertLocalNode is the only writer of this machine's mesh_nodes row. An empty app version
// keeps the recorded one, so role changes made without a running gateway do not erase it.
func upsertLocalNode(ctx context.Context, db execer, name string, site []byte, app string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO mesh_nodes(node_id,name,site_id,schema_version,app_version,updated_at)
 VALUES (`+LocalNodeSQL+`,?,?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET
 name=excluded.name,site_id=excluded.site_id,schema_version=excluded.schema_version,
 app_version=CASE WHEN excluded.app_version='' THEN app_version ELSE excluded.app_version END,
 updated_at=CASE WHEN excluded.app_version='' THEN updated_at ELSE excluded.updated_at END
 WHERE name IS NOT excluded.name OR site_id IS NOT excluded.site_id OR schema_version IS NOT excluded.schema_version
 OR (excluded.app_version<>'' AND app_version IS NOT excluded.app_version)`, name, site, ReplicationVersion, app, time.Now().UnixMilli())
	return err
}

// Publish records this machine's name, site and versions for the rest of the mesh.
func (m Mesh) Publish(ctx context.Context, name, app string) error {
	site, err := m.s.SiteID(ctx)
	if err != nil {
		return err
	}
	return upsertLocalNode(ctx, m.s.DB, name, site, app)
}

// Nodes lists every machine the mesh has recorded, this one included.
func (m Mesh) Nodes(ctx context.Context) ([]MeshNode, error) {
	rows, err := m.s.Reader.QueryContext(ctx, "SELECT node_id,name,app_version,schema_version FROM mesh_nodes ORDER BY node_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []MeshNode
	for rows.Next() {
		var node MeshNode
		if err := rows.Scan(&node.ID, &node.Name, &node.AppVersion, &node.Schema); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

// NodeName is the recorded name of a machine, or "" when none is known.
func (m Mesh) NodeName(ctx context.Context, id string) (string, error) {
	var name string
	err := m.s.Reader.QueryRowContext(ctx, "SELECT name FROM mesh_nodes WHERE node_id=?", id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// Setting reads a mesh-wide setting; a missing key is "".
func (m Mesh) Setting(ctx context.Context, key string) (string, error) {
	var value string
	err := m.s.Reader.QueryRowContext(ctx, "SELECT value FROM mesh_settings WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (m Mesh) SetSetting(ctx context.Context, key, value string) error {
	_, err := m.s.DB.ExecContext(ctx, "INSERT INTO mesh_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}
