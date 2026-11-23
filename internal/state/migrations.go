package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/binary"
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
		return nil
	}
	contents := make([]string, len(files))
	for i, name := range files {
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		contents[i] = string(data)
	}
	return migratePending(ctx, conn, files, contents)
}

// migratePending applies every missing migration in one transaction: one sync instead of one per
// migration, so a fresh database opens several times faster, and a failed upgrade leaves the old version.
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
const ReplicationVersion = 1620642161

func replicationVersion(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT t.name,c.name,c.type,c."notnull",coalesce(c.dflt_value,''),c.pk
 FROM sqlite_schema t JOIN pragma_table_info(t.name) c
 WHERE t.type='table' AND EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name=t.name||'__crsql_clock')
 ORDER BY t.name,c.name`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	digest := sha256.New()
	for rows.Next() {
		var table, column, kind, defaultValue string
		var notNull, pk int
		if err := rows.Scan(&table, &column, &kind, &notNull, &defaultValue, &pk); err != nil {
			return 0, err
		}
		fmt.Fprintf(digest, "%s\x00%s\x00%s\x00%d\x00%s\x00%d\n", table, column, kind, notNull, defaultValue, pk)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Bit 30 keeps the value apart from the migration counts older peers send, which they read as Needs update.
	return int(binary.BigEndian.Uint32(digest.Sum(nil))&0x3fffffff | 0x40000000), nil
}
