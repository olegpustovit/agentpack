package state

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func SchemaVersion() int {
	files, _ := fs.Glob(migrationFiles, "migrations/*.sql")
	return len(files)
}

func migrate(ctx context.Context, db *sql.DB, source fs.FS) error {
	files, err := fs.Glob(source, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list state migrations: %w", err)
	}
	for i, name := range files {
		if !strings.HasPrefix(name, fmt.Sprintf("migrations/%04d_", i+1)) {
			return fmt.Errorf("state migrations must be contiguous: %s", name)
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// A current database needs no write lock: per-migration checks each take one and stall a streaming worker.
	var current int
	if conn.QueryRowContext(ctx, "SELECT CASE WHEN count(*)=1 THEN max(version) END FROM schema_meta").Scan(&current) == nil && current == len(files) {
		return ensureVersionIndexes(ctx, conn)
	}
	contents := make([]string, len(files))
	for i, name := range files {
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		contents[i] = string(data)
	}
	if err := migratePending(ctx, conn, files, contents); err != nil {
		return err
	}
	return ensureVersionIndexes(ctx, conn)
}

// ensureVersionIndexes indexes every clock table on db_version. cr-sqlite indexes them on
// (site_id, db_version), but sync reads by db_version alone, so each peer pull scanned every clock
// row (281k rows, 144 ms on an M-series Air). It runs after migrations rather than as one, so it also
// covers clock tables created by later migrations, and the index is local: the mesh wire version does
// not change.
func ensureVersionIndexes(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `SELECT t.name FROM sqlite_master t WHERE t.type='table'
		AND t.name LIKE '%\_\_crsql\_clock' ESCAPE '\'
		AND NOT EXISTS (SELECT 1 FROM sqlite_master i WHERE i.type='index' AND i.name=t.name||'_dbv')`)
	if err != nil {
		return err
	}
	var missing []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		missing = append(missing, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, table := range missing {
		if _, err := conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS "`+table+`_dbv" ON "`+table+`"(db_version)`); err != nil {
			return fmt.Errorf("index %s: %w", table, err)
		}
	}
	return nil
}

// migratePending applies every missing migration in one transaction: one sync instead of one per
// migration, so a fresh database opens several times faster, and a failed upgrade leaves the last
// committed version.
func migratePending(ctx context.Context, conn *sql.Conn, files, contents []string) (err error) {
	// Serialise version checks with other gateways/workers before reading the version.
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, rollbackErr := conn.ExecContext(context.Background(), "ROLLBACK")
			err = errors.Join(err, rollbackErr)
		}
	}()
	var exists, current int
	if err = conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='schema_meta'").Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		var count, highest int
		if err = conn.QueryRowContext(ctx, "SELECT count(*), coalesce(min(version),0), coalesce(max(version),0) FROM schema_meta").Scan(&count, &current, &highest); err != nil {
			return err
		}
		if count != 1 || current < 1 || current != highest {
			return fmt.Errorf("invalid state schema version")
		}
	}
	latest := len(contents)
	if current > latest {
		return fmt.Errorf("state schema %d requires a newer agentpack (supports %d)", current, latest)
	}
	if current == latest {
		_, err = conn.ExecContext(ctx, "COMMIT")
		committed = err == nil
		return err
	}
	for version := current + 1; version <= latest; version++ {
		first, _, _ := strings.Cut(contents[version-1], "\n")
		if table, alters := strings.CutPrefix(first, "-- crsql-alter: "); alters {
			err = alterCRR(ctx, conn, strings.TrimSpace(table), contents[version-1])
		} else {
			_, err = conn.ExecContext(ctx, contents[version-1])
		}
		if err != nil {
			return fmt.Errorf("state migration %s: %w", files[version-1], err)
		}
		// cr-sqlite does not see tables created later in the transaction that altered a table, so
		// an upgrade commits each alteration; a fresh database has no rows to trip on.
		if current > 0 && version < latest && strings.HasPrefix(first, "-- crsql-alter: ") {
			if _, err = conn.ExecContext(ctx, "UPDATE schema_meta SET version = ?", version); err != nil {
				return err
			}
			if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
				return err
			}
			if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
				committed = true
				return err
			}
		}
	}
	if current == 0 {
		_, err = conn.ExecContext(ctx, "INSERT INTO schema_meta(version) VALUES (?)", latest)
	} else {
		_, err = conn.ExecContext(ctx, "UPDATE schema_meta SET version = ?", latest)
	}
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func alterCRR(ctx context.Context, conn *sql.Conn, table, contents string) error {
	if _, err := conn.ExecContext(ctx, "SELECT crsql_begin_alter(?)", table); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, contents); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "SELECT crsql_commit_alter(?)", table)
	return err
}

// ReplicationVersion is the mesh wire version: peers exchange changes only when their replicated
// tables have the same columns, so a local-only migration never splits the mesh.
// TestReplicationVersionIsPinned recomputes it from the migrations; update it with every replicated column change.
const ReplicationVersion = 1597074445
