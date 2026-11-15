package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// SQLiteValue keeps integers exact and distinguishes an empty blob from NULL.
type SQLiteValue struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func sqliteValue(value any) (SQLiteValue, error) {
	var kind string
	switch value.(type) {
	case nil:
		kind = "null"
	case int64:
		kind = "integer"
	case float64:
		kind = "real"
	case string:
		kind = "text"
	case []byte:
		kind = "blob"
	default:
		return SQLiteValue{}, errors.New("unsupported SQLite value type")
	}
	data, err := json.Marshal(value)
	return SQLiteValue{Type: kind, Data: data}, err
}

func (v SQLiteValue) value() (any, error) {
	var target any
	switch v.Type {
	case "null":
		if !bytes.Equal(bytes.TrimSpace(v.Data), []byte("null")) {
			return nil, errors.New("invalid SQLite NULL")
		}
		return nil, nil
	case "integer":
		target = new(int64)
	case "real":
		target = new(float64)
	case "text":
		target = new(string)
	case "blob":
		target = new([]byte)
	default:
		return nil, errors.New("invalid SQLite value type")
	}
	if bytes.Equal(bytes.TrimSpace(v.Data), []byte("null")) {
		return nil, errors.New("typed SQLite value must not be NULL")
	}
	if err := json.Unmarshal(v.Data, target); err != nil {
		return nil, fmt.Errorf("decode SQLite %s: %w", v.Type, err)
	}
	switch value := target.(type) {
	case *int64:
		return *value, nil
	case *float64:
		return *value, nil
	case *string:
		return *value, nil
	case *[]byte:
		return bytes.Clone(*value), nil
	}
	panic("unreachable SQLite type")
}

type Change struct {
	Table      string      `json:"table"`
	PK         []byte      `json:"pk"`
	Column     string      `json:"cid"`
	Value      SQLiteValue `json:"val"`
	ColVersion int64       `json:"col_version"`
	DBVersion  int64       `json:"db_version"`
	SiteID     []byte      `json:"site_id"`
	CL         int64       `json:"cl"`
	Seq        int64       `json:"seq"`
	TS         *string     `json:"ts"`
}

func (s *Store) SiteID(ctx context.Context) ([]byte, error) {
	var site []byte
	err := s.Reader.QueryRowContext(ctx, "SELECT crsql_site_id()").Scan(&site)
	return site, err
}

// The persisted counter cannot fall when a newer value supersedes the last row of an older version.
func (s *Store) DBVersion(ctx context.Context) (int64, error) {
	var version int64
	err := s.Reader.QueryRowContext(ctx, "SELECT coalesce(max(db_version),0) FROM crsql_db_versions").Scan(&version)
	return version, err
}

var ErrChangeVersionTooLarge = errors.New("replicated transaction exceeds the change page byte limit")

type ChangeVersionTooLargeError struct {
	Version int64
	Limit   int
}

func (e *ChangeVersionTooLargeError) Error() string {
	return fmt.Sprintf("%s: version %d, limit %d", ErrChangeVersionTooLarge, e.Version, e.Limit)
}

func (e *ChangeVersionTooLargeError) Unwrap() error { return ErrChangeVersionTooLarge }

// WriteVersion freezes one oversized source version in a read snapshot without buffering it all.
func (s *Store) WriteVersion(ctx context.Context, version int64, exclude []byte, out io.Writer) error {
	if version < 0 || len(exclude) != 0 && len(exclude) != 16 {
		return errors.New("invalid change version parameters")
	}
	tx, err := s.Reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT "table",pk,cid,val,col_version,db_version,site_id,cl,seq,ts
		FROM crsql_changes WHERE db_version=? AND (? IS NULL OR site_id IS NOT ?)
		ORDER BY seq,"table",pk,cid`, version, exclude, exclude)
	if err != nil {
		return err
	}
	defer rows.Close()
	if _, err := io.WriteString(out, "["); err != nil {
		return err
	}
	separator := ""
	encoder := json.NewEncoder(out)
	for rows.Next() {
		var change Change
		var value any
		if err := rows.Scan(&change.Table, &change.PK, &change.Column, &value, &change.ColVersion, &change.DBVersion, &change.SiteID, &change.CL, &change.Seq, &change.TS); err != nil {
			return err
		}
		change.Value, err = sqliteValue(value)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(out, separator); err != nil {
			return err
		}
		if err := encoder.Encode(change); err != nil {
			return err
		}
		separator = ","
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = io.WriteString(out, "]")
	return err
}

// Changes never splits a source version; next also advances past excluded-only versions.
func (s *Store) Changes(ctx context.Context, since int64, exclude []byte, maxBytes int) ([]Change, int64, error) {
	if since < 0 || maxBytes < 2 || len(exclude) != 0 && len(exclude) != 16 {
		return nil, since, errors.New("invalid change page parameters")
	}
	tx, err := s.Reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, since, err
	}
	defer tx.Rollback()
	var high int64
	if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(db_version),0) FROM crsql_db_versions").Scan(&high); err != nil {
		return nil, since, err
	}
	if since > high {
		return nil, since, errors.New("sync cursor is ahead of the source database")
	}
	rows, err := tx.QueryContext(ctx, `SELECT "table",pk,cid,val,col_version,db_version,site_id,cl,seq,ts
		FROM crsql_changes WHERE db_version>? AND (? IS NULL OR site_id IS NOT ?)
		ORDER BY db_version,seq,"table",pk,cid`, since, exclude, exclude)
	if err != nil {
		return nil, since, err
	}
	defer rows.Close()
	result, group := []Change{}, []Change{}
	next, version := since, int64(-1)
	size, groupSize := 2, 0
	for rows.Next() {
		var change Change
		var value any
		if err := rows.Scan(&change.Table, &change.PK, &change.Column, &value, &change.ColVersion, &change.DBVersion, &change.SiteID, &change.CL, &change.Seq, &change.TS); err != nil {
			return nil, since, err
		}
		if change.DBVersion != version {
			result = append(result, group...)
			size += groupSize
			if version >= 0 {
				next = version
			}
			group, groupSize, version = nil, 0, change.DBVersion
		}
		change.Value, err = sqliteValue(value)
		if err != nil {
			return nil, since, err
		}
		body, err := json.Marshal(change)
		if err != nil {
			return nil, since, err
		}
		cost := len(body) + 1
		if size+groupSize+cost > maxBytes {
			if len(result) == 0 {
				return nil, since, &ChangeVersionTooLargeError{Version: version, Limit: maxBytes}
			}
			return result, next, nil
		}
		group = append(group, change)
		groupSize += cost
	}
	if err := rows.Err(); err != nil {
		return nil, since, err
	}
	return append(result, group...), high, nil
}

func decodeChanges(changes []Change) ([]any, error) {
	values := make([]any, len(changes))
	for i, change := range changes {
		if change.Table == "" || change.Column == "" || len(change.Table) > 255 || len(change.Column) > 255 || len(change.PK) == 0 || len(change.SiteID) != 16 || change.ColVersion < 0 || change.DBVersion < 0 || change.CL < 0 || change.Seq < 0 {
			return nil, errors.New("invalid replicated change identity or version")
		}
		value, err := change.Value.value()
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	return values, nil
}

func (s *Store) applyChanges(ctx context.Context, tx *sql.Tx, changes []Change, values []any) error {
	var local []byte
	var high int64
	if err := tx.QueryRowContext(ctx, "SELECT crsql_site_id(),coalesce(max(db_version),0) FROM crsql_db_versions").Scan(&local, &high); err != nil {
		return err
	}
	versions := map[int64]int64{}
	localVersions := make([]int64, len(changes))
	for i, change := range changes {
		// A relayed copy cannot author changes on behalf of this database.
		if bytes.Equal(change.SiteID, local) {
			continue
		}
		version, ok := versions[change.DBVersion]
		if !ok {
			high++
			version = high
			versions[change.DBVersion] = version
		}
		localVersions[i] = version
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO crsql_changes ("table",pk,cid,val,col_version,db_version,site_id,cl,seq,ts) VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for i, change := range changes {
		if localVersions[i] == 0 {
			continue
		}
		if change.Table == "archive_event_clocks" || change.Table == "sessions" && change.Column == "archived_seq" {
			if _, err := statement.ExecContext(ctx, change.Table, change.PK, change.Column, values[i], change.ColVersion, localVersions[i], change.SiteID, change.CL, change.Seq, change.TS); err != nil {
				return fmt.Errorf("apply archive clock: %w", err)
			}
		}
	}
	if err := guardArchiveMarkers(ctx, tx, changes, values); err != nil {
		return err
	}
	for i, change := range changes {
		if localVersions[i] == 0 {
			continue
		}
		if change.Table == "archive_event_clocks" || change.Table == "sessions" && change.Column == "archived_seq" {
			continue
		}
		if _, err := statement.ExecContext(ctx, change.Table, change.PK, change.Column, values[i], change.ColVersion, localVersions[i], change.SiteID, change.CL, change.Seq, change.TS); err != nil {
			return fmt.Errorf("apply replicated change to %s: %w", change.Table, err)
		}
	}
	if len(versions) > 0 {
		// Conflict clocks and authors stay intact; transport versions belong to the receiving database.
		_, err := tx.ExecContext(ctx, `INSERT INTO crsql_db_versions(site_id,db_version) VALUES (?,?)
 ON CONFLICT(site_id) DO UPDATE SET db_version=excluded.db_version WHERE db_version<excluded.db_version`, local, high)
		return err
	}
	return nil
}

func (s *Store) Apply(ctx context.Context, changes []Change) error {
	values, err := decodeChanges(changes)
	if err != nil {
		return err
	}
	if err := s.preserveArchiveEdits(ctx, changes, values); err != nil {
		return err
	}
	return Transaction(ctx, s.DB, func(tx *sql.Tx) error { return s.applyChanges(ctx, tx, changes, values) })
}

func (s *Store) Cursor(ctx context.Context, site []byte) (int64, error) {
	if len(site) != 16 {
		return 0, errors.New("invalid peer site ID")
	}
	var version int64
	err := s.Reader.QueryRowContext(ctx, "SELECT version FROM crsql_tracked_peers WHERE site_id=? AND tag=0 AND event=0", site).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return version, err
}

func setCursor(ctx context.Context, tx *sql.Tx, site []byte, version int64) error {
	if len(site) != 16 || version < 0 {
		return errors.New("invalid peer cursor")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO crsql_tracked_peers(site_id,version,seq,tag,event) VALUES (?,?,0,0,0)
		ON CONFLICT(site_id,tag,event) DO UPDATE SET version=excluded.version WHERE excluded.version>version`, site, version)
	return err
}

func (s *Store) SetCursor(ctx context.Context, site []byte, version int64) error {
	return Transaction(ctx, s.DB, func(tx *sql.Tx) error { return setCursor(ctx, tx, site, version) })
}

// ApplyPage commits the received data and its cursor together, or neither.
func (s *Store) ApplyPage(ctx context.Context, changes []Change, site []byte, next int64) error {
	if len(site) != 16 || next < 0 {
		return errors.New("invalid peer cursor")
	}
	for _, change := range changes {
		if change.DBVersion > next {
			return fmt.Errorf("change version %d exceeds its page cursor %d", change.DBVersion, next)
		}
	}
	values, err := decodeChanges(changes)
	if err != nil {
		return err
	}
	if err := s.preserveArchiveEdits(ctx, changes, values); err != nil {
		return err
	}
	return Transaction(ctx, s.DB, func(tx *sql.Tx) error {
		if err := s.applyChanges(ctx, tx, changes, values); err != nil {
			return err
		}
		return setCursor(ctx, tx, site, next)
	})
}
