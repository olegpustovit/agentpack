// Package state owns gateway state; launch setup must never open this store.
package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/paths"
)

type Store struct {
	DB     *sql.DB
	Reader *sql.DB
	home   string
}

func Open(home string) (*Store, error) {
	if err := paths.EnsurePrivateDirectory(home); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(home, "state.db"))
	if err != nil {
		return nil, fmt.Errorf("state path: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("state database must be a regular file: %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect state database: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("create state database: %w", err)
	}
	modeErr := file.Chmod(0600)
	if err := errors.Join(modeErr, file.Close()); err != nil {
		return nil, fmt.Errorf("secure state database: %w", err)
	}
	params := url.Values{"_journal_mode": {"WAL"}, "_synchronous": {"FULL"}, "_busy_timeout": {"5000"}, "_txlock": {"immediate"}}
	dsn := "file:" + url.PathEscape(filepath.ToSlash(path)) + "?"
	writer, err := sql.Open(driverName, dsn+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("open state writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	// Imported pages advance the persisted local clock; a fresh writer reloads it before its next transaction.
	writer.SetMaxIdleConns(0)
	if err := writer.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("connect state writer: %w", err), writer.Close())
	}
	if err := migrate(context.Background(), writer, migrationFiles); err != nil {
		return nil, errors.Join(err, writer.Close())
	}
	params.Set("_query_only", "1")
	params.Set("_txlock", "deferred")
	reader, err := sql.Open(driverName, dsn+params.Encode())
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open state reader: %w", err), writer.Close())
	}
	reader.SetMaxOpenConns(4)
	if err := reader.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("connect state reader: %w", err), reader.Close(), writer.Close())
	}
	return &Store{DB: writer, Reader: reader, home: home}, nil
}

func (s *Store) Close() error { return errors.Join(s.Reader.Close(), s.DB.Close()) }

// Transaction commits only when every operation succeeds; panics also roll back.
func Transaction(ctx context.Context, db *sql.DB, run func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := run(tx); err != nil {
		return err
	}
	return tx.Commit()
}
