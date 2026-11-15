package state

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/olegpustovit/agentpack/internal/paths"
)

const archiveSchema = `CREATE TABLE conversation_events (
 session_id TEXT NOT NULL, seq INTEGER NOT NULL, body TEXT NOT NULL,
 PRIMARY KEY(session_id,seq))`

// VerifyArchive validates a copied archive without creating or changing it.
func VerifyArchive(home string) error {
	db, err := ReadArchive(home)
	if err != nil {
		return err
	}
	defer db.Close()
	var check string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil {
		return fmt.Errorf("check archive: %w", err)
	}
	if check != "ok" {
		return errors.New("archive integrity check failed")
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return err
	}
	if tables != 1 {
		return errors.New("archive must contain only the plain conversation_events table")
	}
	var kind string
	if err := db.QueryRow("SELECT type FROM sqlite_schema WHERE name='conversation_events'").Scan(&kind); err != nil || kind != "table" {
		return errors.New("archive conversation_events table is missing")
	}
	rows, err := db.Query("PRAGMA table_info(conversation_events)")
	if err != nil {
		return err
	}
	defer rows.Close()
	columns := []struct {
		name, kind string
		pk         int
	}{{"session_id", "TEXT", 1}, {"seq", "INTEGER", 2}, {"body", "TEXT", 0}}
	index := 0
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if index >= len(columns) || name != columns[index].name || strings.ToUpper(kind) != columns[index].kind || notNull != 1 || pk != columns[index].pk {
			return errors.New("archive schema is invalid")
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if index != len(columns) {
		return errors.New("archive schema is invalid")
	}
	return nil
}

// OpenArchive is plain SQLite; it never loads cr-sqlite or changes an existing archive's schema.
func OpenArchive(home string) (*sql.DB, error) {
	if err := paths.EnsurePrivateDirectory(home); err != nil {
		return nil, err
	}
	path, err := filepath.Abs(filepath.Join(home, "archive.db"))
	if err != nil {
		return nil, err
	}
	created := false
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		created = true
	} else if err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, errors.New("archive must be a regular file")
	}
	if !created {
		if err := VerifyArchive(home); err != nil {
			return nil, err
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(path))+"?agentpack_plain=1&mode=rw&_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if created {
		_, err = db.Exec(archiveSchema)
	} else {
		err = db.Ping()
	}
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

// ReadArchive never creates a missing archive or enables writes.
func ReadArchive(home string) (*sql.DB, error) {
	path, err := filepath.Abs(filepath.Join(home, "archive.db"))
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("archive must be a regular file")
	}
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(path))+"?agentpack_plain=1&mode=ro&_query_only=1&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
