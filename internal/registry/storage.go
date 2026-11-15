package registry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/olegpustovit/agentpack/internal/state"
)

func (s *Store) objectData(id string) ([]byte, error) {
	if !validDigest(id) {
		return nil, fmt.Errorf("invalid object digest")
	}
	var data []byte
	err := s.sql.Reader.QueryRow(`SELECT body FROM registry_objects WHERE digest=?
 UNION ALL SELECT body FROM registry_names WHERE digest=?`, id, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		err = os.ErrNotExist
	}
	return data, err
}

func (s *Store) storedIDs(table string) ([]string, error) {
	key := "digest"
	switch table {
	case "registry_objects", "registry_names", "registry_tombstones":
	case "registry_drafts":
		key = "id"
	default:
		return nil, fmt.Errorf("invalid registry table")
	}
	rows, err := s.sql.Reader.Query("SELECT " + key + " FROM " + table + " ORDER BY " + key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) objectIDs() ([]string, error) {
	objects, err := s.storedIDs("registry_objects")
	if err != nil {
		return nil, err
	}
	names, err := s.storedIDs("registry_names")
	return append(objects, names...), err
}

func (s *Store) storeObject(table, id string, data []byte) error {
	switch table {
	case "registry_objects", "registry_names", "registry_tombstones":
	default:
		return fmt.Errorf("invalid registry object table")
	}
	return state.Transaction(context.Background(), s.sql.DB, func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT OR IGNORE INTO "+table+"(digest,body) VALUES (?,?)", id, string(data)); err != nil {
			return err
		}
		var existing []byte
		if err := tx.QueryRow("SELECT body FROM "+table+" WHERE digest=?", id).Scan(&existing); err != nil {
			return err
		}
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("registry object collision")
		}
		return nil
	})
}

func (s *Store) removeObject(id string) error {
	return state.Transaction(context.Background(), s.sql.DB, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM registry_objects WHERE digest=?", id); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM registry_names WHERE digest=?", id)
		return err
	})
}

func (s *Store) readBindings(id string) ([]byte, bool, error) {
	var data []byte
	err := s.sql.Reader.QueryRow("SELECT body FROM registry_bindings WHERE revision_id=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func (s *Store) metadata(id string) (Agentpack, error) {
	var pack Agentpack
	var data []byte
	err := s.sql.Reader.QueryRow("SELECT body FROM registry_heads WHERE agentpack_id=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return pack, os.ErrNotExist
	}
	if err != nil {
		return pack, err
	}
	if err := json.Unmarshal(data, &pack); err != nil || pack.ID != id {
		return pack, fmt.Errorf("invalid registry metadata")
	}
	return pack, nil
}
