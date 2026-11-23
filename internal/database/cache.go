package database

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// OpenCache opens the disposable plain SQLite index; package trees and lockfiles remain authoritative.
func OpenCache(readOnly bool) (_ *sql.DB, result error) {
	path, err := paths.CacheDBPath()
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if readOnly {
			return nil, nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err == nil {
			if err := file.Close(); err != nil {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, errors.New("cache database must be a regular file")
	}
	params := url.Values{"agentpack_plain": {"1"}, "_busy_timeout": {"2000"}}
	if readOnly {
		params.Set("mode", "ro")
		params.Set("_query_only", "1")
	} else {
		params.Set("mode", "rw")
		params.Set("_journal_mode", "WAL")
		params.Set("_synchronous", "NORMAL")
	}
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(path))+"?"+params.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	defer func() {
		if result != nil {
			result = errors.Join(result, db.Close())
		}
	}()
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("open cache index: %w", err)
	}
	if !readOnly {
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS cache_entries(cache_key TEXT PRIMARY KEY,body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS aliases(alias TEXT PRIMARY KEY,cache_key TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS github_metadata(bucket BLOB NOT NULL,key BLOB NOT NULL,body BLOB NOT NULL,PRIMARY KEY(bucket,key));`)
		if err != nil {
			return nil, fmt.Errorf("initialize cache index: %w", err)
		}
	}
	return db, nil
}
