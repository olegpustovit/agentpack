// Package state owns gateway state; launch setup must never open this store.
package state

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

type Store struct {
	DB     *sql.DB
	Reader *sql.DB
}

func Open(home string) (*Store, error) {
	if err := os.MkdirAll(home, 0700); err != nil {
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
	params := url.Values{"_journal_mode": {"WAL"}, "_synchronous": {"FULL"}, "_busy_timeout": {"5000"}}
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
	params.Set("_query_only", "1")
	reader, err := sql.Open(driverName, dsn+params.Encode())
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open state reader: %w", err), writer.Close())
	}
	reader.SetMaxOpenConns(4)
	if err := reader.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("connect state reader: %w", err), reader.Close(), writer.Close())
	}
	return &Store{DB: writer, Reader: reader}, nil
}

func (s *Store) Close() error { return errors.Join(s.Reader.Close(), s.DB.Close()) }
