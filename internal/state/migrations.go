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
	for i, name := range files {
		contents, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		if err := migrateOne(ctx, conn, i+1, len(files), string(contents)); err != nil {
			return fmt.Errorf("state migration %s: %w", name, err)
		}
	}
	return nil
}

func migrateOne(ctx context.Context, conn *sql.Conn, version, latest int, contents string) (err error) {
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
	if current > latest {
		return fmt.Errorf("state schema %d requires a newer agentpack (supports %d)", current, latest)
	}
	if current < version {
		if current != version-1 {
			return fmt.Errorf("state schema %d cannot apply migration %d", current, version)
		}
		first, _, _ := strings.Cut(contents, "\n")
		if table, alters := strings.CutPrefix(first, "-- crsql-alter: "); alters {
			err = alterCRR(ctx, conn, strings.TrimSpace(table), contents)
		} else {
			_, err = conn.ExecContext(ctx, contents)
		}
		if err != nil {
			return err
		}
		if version == 1 {
			_, err = conn.ExecContext(ctx, "INSERT INTO schema_meta(version) VALUES (?)", version)
		} else {
			_, err = conn.ExecContext(ctx, "UPDATE schema_meta SET version = ?", version)
		}
		if err != nil {
			return err
		}
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
