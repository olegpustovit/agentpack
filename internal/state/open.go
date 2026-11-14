// Package state owns gateway state; launch setup must never open this store.
package state

import (
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	DB *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open state: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect state: %w", err)
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error {
	_, finalizeErr := s.DB.Exec("SELECT crsql_finalize()")
	return errors.Join(finalizeErr, s.DB.Close())
}
