package state

import (
	"database/sql"
	"net/url"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// PlainOptions selects how OpenPlain opens a non-replicated SQLite file.
type PlainOptions struct {
	ReadOnly bool
	// Durable syncs every commit (FULL) and takes the write lock at BEGIN; otherwise NORMAL.
	Durable bool
}

// OpenPlain opens a plain SQLite database (archive, cache index, diagnostics) on one
// connection. agentpack_plain keeps cr-sqlite out of it; callers validate the file first.
func OpenPlain(path string, opts PlainOptions) (*sql.DB, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	params := url.Values{"agentpack_plain": {"1"}, "_busy_timeout": {"5000"}}
	if opts.ReadOnly {
		params.Set("mode", "ro")
		params.Set("_query_only", "1")
	} else {
		params.Set("mode", "rw")
		params.Set("_journal_mode", "WAL")
		params.Set("_synchronous", "NORMAL")
		if opts.Durable {
			params.Set("_synchronous", "FULL")
			params.Set("_txlock", "immediate")
		}
	}
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(path))+"?"+params.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
