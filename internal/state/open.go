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
	"sync/atomic"

	"github.com/olegpustovit/agentpack/internal/paths"
)

type Store struct {
	DB     *sql.DB
	Reader *sql.DB
	home   string
	site   atomic.Pointer[[]byte]
	served atomic.Int64

	replicator       *Replicator
	replicationReads *sql.DB
}

func Open(home string) (*Store, error) {
	if err := paths.EnsurePrivateDirectory(home); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(home, "state.db"))
	if err != nil {
		return nil, fmt.Errorf("state path: %w", err)
	}
	if err := prepareStateFile(path); err != nil {
		return nil, err
	}
	params := url.Values{"_journal_mode": {"WAL"}, "_synchronous": {"FULL"}, "_busy_timeout": {"5000"}, "_txlock": {"immediate"}}
	dsn := "file:" + url.PathEscape(filepath.ToSlash(path)) + "?"
	writer, err := sql.Open(driverName, dsn+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("open state writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
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
	// Bulk replication holds snapshots while scanning and encoding changes. Keep
	// those connections separate from short account, session and worker reads.
	replicationReads, err := sql.Open(driverName, dsn+params.Encode())
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open replication reader: %w", err), reader.Close(), writer.Close())
	}
	replicationReads.SetMaxOpenConns(2)
	store := &Store{DB: writer, Reader: reader, home: home, replicationReads: replicationReads}
	store.replicator = newReplicator(store)
	if err := store.catchUpServed(context.Background()); err != nil {
		return nil, errors.Join(fmt.Errorf("check restored state database: %w", err), store.Close())
	}
	return store, nil
}

func prepareStateFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if createErr == nil {
			if err := errors.Join(file.Chmod(0600), file.Close()); err != nil {
				return fmt.Errorf("secure state database: %w", err)
			}
			return nil
		}
		if !errors.Is(createErr, os.ErrExist) {
			return fmt.Errorf("create state database: %w", createErr)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect state database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("state database must be a regular file: %s", path)
	}
	// Closing a separate raw descriptor releases this process's POSIX locks on
	// the inode, including locks held by SQLite connections from another Store.
	if info.Mode().Perm() != 0600 {
		if err := os.Chmod(path, 0600); err != nil {
			return fmt.Errorf("secure state database: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error {
	return errors.Join(s.replicationReads.Close(), s.Reader.Close(), s.DB.Close())
}

type transactionStarter interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// Transaction commits only when every operation succeeds; panics also roll back.
func Transaction(ctx context.Context, db transactionStarter, run func(*sql.Tx) error) error {
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
