package state

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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

// SiteID is fixed for the life of the database, so every poll after the first reads the cached copy.
func (s *Store) SiteID(ctx context.Context) ([]byte, error) {
	if site := s.site.Load(); site != nil {
		return bytes.Clone(*site), nil
	}
	var site []byte
	if err := s.Reader.QueryRowContext(ctx, "SELECT crsql_site_id()").Scan(&site); err != nil {
		return nil, err
	}
	s.site.Store(&site)
	return bytes.Clone(site), nil
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
func (r *Replicator) WriteVersion(ctx context.Context, version int64, exclude []byte, out io.Writer) error {
	s := r.s
	if version < 0 || len(exclude) != 0 && len(exclude) != 16 {
		return errors.New("invalid change version parameters")
	}
	tx, err := s.replicationReads.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
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

// ErrCursorAhead means the peer saw versions this database no longer has: it was restored from a copy.
var ErrCursorAhead = errors.New("sync cursor is ahead of the source database")

// changeRowCost is less than any encoded change, so a page reaches its byte budget before its row limit.
const changeRowCost = 96

// ChangePage encodes complete source versions after since as one JSON array within maxBytes.
// It never splits a source version; next also advances past excluded-only versions.
func (r *Replicator) ChangePage(ctx context.Context, since int64, exclude []byte, maxBytes int) (json.RawMessage, int64, error) {
	s := r.s
	if since < 0 || maxBytes < 2 || len(exclude) != 0 && len(exclude) != 16 {
		return nil, since, errors.New("invalid change page parameters")
	}
	tx, err := s.replicationReads.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, since, err
	}
	defer tx.Rollback()
	var high int64
	if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(db_version),0) FROM crsql_db_versions").Scan(&high); err != nil {
		return nil, since, err
	}
	if since > high {
		return nil, since, ErrCursorAhead
	}
	limit := maxBytes/changeRowCost + 1
	rows, err := tx.QueryContext(ctx, `SELECT "table",pk,cid,val,col_version,db_version,site_id,cl,seq,ts
		FROM crsql_changes WHERE db_version>? AND (? IS NULL OR site_id IS NOT ?)
		ORDER BY db_version,seq,"table",pk,cid LIMIT ?`, since, exclude, exclude, limit)
	if err != nil {
		return nil, since, err
	}
	defer rows.Close()
	var page bytes.Buffer
	page.WriteByte('[')
	encoder := json.NewEncoder(&page)
	// complete is the page length through the last whole version.
	complete, next, version, count := 1, since, int64(-1), 0
	for rows.Next() {
		count++
		var change Change
		var value any
		if err := rows.Scan(&change.Table, &change.PK, &change.Column, &value, &change.ColVersion, &change.DBVersion, &change.SiteID, &change.CL, &change.Seq, &change.TS); err != nil {
			return nil, since, err
		}
		if change.DBVersion != version {
			if version >= 0 {
				complete, next = page.Len(), version
			}
			version = change.DBVersion
		}
		if change.Value, err = sqliteValue(value); err != nil {
			return nil, since, err
		}
		if page.Len() > 1 {
			page.WriteByte(',')
		}
		if err := encoder.Encode(change); err != nil {
			return nil, since, err
		}
		if page.Len()+1 > maxBytes {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, since, err
	}
	if page.Len()+1 <= maxBytes && count < limit {
		page.WriteByte(']')
		return page.Bytes(), high, nil
	}
	if complete == 1 {
		return nil, since, &ChangeVersionTooLargeError{Version: version, Limit: maxBytes}
	}
	page.Truncate(complete)
	page.WriteByte(']')
	return page.Bytes(), next, nil
}

// servedFile sits beside state.db, so restoring the database from a copy leaves it behind.
func (s *Store) servedFile() string { return filepath.Join(s.home, "state.served") }

// NoteServed records the highest version a peer has been sent; Open uses it to detect a restored database.
func (r *Replicator) NoteServed(version int64) error {
	s := r.s
	if version <= s.served.Load() {
		return nil
	}
	s.served.Store(version)
	// A torn write after a crash reads as 0 and only skips detection; it never blocks opening.
	return os.WriteFile(s.servedFile(), []byte(strconv.FormatInt(version, 10)), 0o600)
}

// catchUpServed moves the version counter past what peers already hold after a restore,
// so new writes get versions their cursors have not passed.
func (s *Store) catchUpServed(ctx context.Context) error {
	data, err := os.ReadFile(s.servedFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	served, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	s.served.Store(served)
	if served <= 0 {
		return nil
	}
	// Opening a current database must coexist with live worker writes. Even an
	// INSERT that changes no rows must acquire the WAL writer lock.
	version, err := s.DBVersion(ctx)
	if err != nil || served <= version {
		return err
	}
	// Check again inside the write in case another opener or worker advanced it.
	_, err = s.DB.ExecContext(ctx, `INSERT INTO crsql_db_versions(site_id,db_version) SELECT crsql_site_id(),?1
 WHERE ?1>(SELECT coalesce(max(db_version),0) FROM crsql_db_versions)
 ON CONFLICT(site_id) DO UPDATE SET db_version=excluded.db_version WHERE db_version<excluded.db_version`, served)
	return err
}

// ResetCursor rewinds a peer's cursor to the start, after the peer's database was restored; reapplying is idempotent.
func (r *Replicator) ResetCursor(ctx context.Context, site []byte) error {
	s := r.s
	if len(site) != 16 {
		return errors.New("invalid peer site ID")
	}
	_, err := s.DB.ExecContext(ctx, "UPDATE crsql_tracked_peers SET version=0 WHERE site_id=? AND tag=0 AND event=0", site)
	return err
}

// Changes decodes ChangePage for callers that inspect the changes.
func (r *Replicator) Changes(ctx context.Context, since int64, exclude []byte, maxBytes int) ([]Change, int64, error) {
	page, next, err := r.ChangePage(ctx, since, exclude, maxBytes)
	if err != nil {
		return nil, next, err
	}
	var changes []Change
	return changes, next, json.Unmarshal(page, &changes)
}

func decodeChanges(changes []Change) ([]any, error) {
	values := make([]any, len(changes))
	for i, change := range changes {
		value, err := decodeChange(change)
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	return values, nil
}

func decodeChange(change Change) (any, error) {
	if change.Table == "" || change.Column == "" || len(change.Table) > 255 || len(change.Column) > 255 || len(change.PK) == 0 || len(change.SiteID) != 16 || change.ColVersion < 0 || change.DBVersion < 0 || change.CL < 0 || change.Seq < 0 {
		return nil, errors.New("invalid replicated change identity or version")
	}
	return change.Value.value()
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

func (r *Replicator) Apply(ctx context.Context, changes []Change) error {
	s := r.s
	values, err := decodeChanges(changes)
	if err != nil {
		return err
	}
	return s.applyReplication(ctx, changes, values, nil)
}

func (r *Replicator) Cursor(ctx context.Context, site []byte) (int64, error) {
	s := r.s
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

func (r *Replicator) SetCursor(ctx context.Context, site []byte, version int64) error {
	s := r.s
	return Transaction(ctx, s.DB, func(tx *sql.Tx) error { return setCursor(ctx, tx, site, version) })
}

type RejectedChange struct {
	Version int64
	Reason  string
}

// ApplyPage commits a peer page and its cursor together, or neither. Malformed changes and ones a
// validation hook refuses are quarantined in the same commit. A source version the archive guard
// refuses is deferred whole, with every later version touching its sessions, and replayed by a
// later page once the guard clears; storage failures stay retryable.
func (r *Replicator) ApplyPage(ctx context.Context, changes []Change, site []byte, next int64) ([]RejectedChange, error) {
	s := r.s
	if len(site) != 16 || next < 0 {
		return nil, errors.New("invalid peer cursor")
	}
	valid := make([]Change, 0, len(changes))
	values := make([]any, 0, len(changes))
	columns := map[string]map[string]bool{}
	var rejected []RejectedChange
	for _, change := range changes {
		value, err := decodeChange(change)
		reason := ""
		switch {
		case change.DBVersion > next:
			reason = "change version exceeds page cursor"
		case err != nil:
			reason = "invalid replicated change"
		default:
			known, ok := columns[change.Table]
			if !ok {
				known, err = s.replicatedColumns(ctx, change.Table)
				if err != nil {
					return nil, err
				}
				columns[change.Table] = known
			}
			if !known[change.Column] {
				reason = "unknown replicated table or column"
			} else if reason, err = r.validate(change); err != nil {
				return nil, err
			}
		}
		if reason != "" {
			rejected = append(rejected, RejectedChange{Version: change.DBVersion, Reason: reason})
			continue
		}
		valid = append(valid, change)
		values = append(values, value)
	}
	blocked, err := r.replayDeferred(ctx)
	if err != nil {
		return nil, err
	}
	for {
		apply, applyValues, deferred, err := s.splitDeferred(ctx, valid, values, blocked)
		if err != nil {
			return nil, err
		}
		err = s.applyReplication(ctx, apply, applyValues, func(tx *sql.Tx) error {
			for _, change := range rejected {
				if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO sync_quarantine(peer,version,reason) VALUES (?,?,?)", site, change.Version, change.Reason); err != nil {
					return err
				}
			}
			for _, version := range deferred {
				if err := deferVersion(ctx, tx, site, version); err != nil {
					return err
				}
			}
			return setCursor(ctx, tx, site, next)
		})
		var guard archiveGuardError
		var data archiveDataError
		switch {
		case errors.As(err, &guard) && !blocked[guard.session]:
			blocked[guard.session] = true
			continue
		case errors.As(err, &data):
			rejected = append(rejected, RejectedChange{Version: data.version, Reason: data.Error()})
			valid, values = withoutVersion(valid, values, data.version)
			continue
		case err != nil:
			return nil, err
		}
		r.signal(apply)
		return rejected, nil
	}
}

type deferredVersion struct {
	version  int64
	sessions []string
	changes  []Change
}

func withoutVersion(changes []Change, values []any, version int64) ([]Change, []any) {
	keptChanges, keptValues := changes[:0:0], values[:0:0]
	for i, change := range changes {
		if change.DBVersion != version {
			keptChanges = append(keptChanges, change)
			keptValues = append(keptValues, values[i])
		}
	}
	return keptChanges, keptValues
}

// changeSession names the chat a change belongs to, or "" for other tables.
func (s *Store) changeSession(ctx context.Context, change Change) (string, error) {
	switch change.Table {
	case "sessions", "conversation_events", "archive_blobs", "archive_event_clocks":
	default:
		return "", nil
	}
	var cell any
	if err := s.Reader.QueryRowContext(ctx, "SELECT cell FROM crsql_unpack_columns(?)", change.PK).Scan(&cell); err != nil {
		return "", err
	}
	if pk, ok := cell.([]byte); ok && change.Table == "archive_event_clocks" {
		if err := s.Reader.QueryRowContext(ctx, "SELECT cell FROM crsql_unpack_columns(?)", pk).Scan(&cell); err != nil {
			return "", err
		}
	}
	id, _ := cell.(string)
	return id, nil
}

// splitDeferred holds back every source version touching a blocked session and blocks the other
// sessions those versions touch, so each chat's changes keep their order.
func (s *Store) splitDeferred(ctx context.Context, changes []Change, values []any, blocked map[string]bool) ([]Change, []any, []deferredVersion, error) {
	if len(blocked) == 0 {
		return changes, values, nil, nil
	}
	var order []int64
	touched := map[int64]map[string]bool{}
	for _, change := range changes {
		if touched[change.DBVersion] == nil {
			touched[change.DBVersion] = map[string]bool{}
			order = append(order, change.DBVersion)
		}
		id, err := s.changeSession(ctx, change)
		if err != nil {
			return nil, nil, nil, err
		}
		if id != "" {
			touched[change.DBVersion][id] = true
		}
	}
	slices.Sort(order)
	held := map[int64]bool{}
	var deferred []deferredVersion
	for _, version := range order {
		hold := false
		for id := range touched[version] {
			hold = hold || blocked[id]
		}
		if !hold {
			continue
		}
		entry := deferredVersion{version: version}
		for id := range touched[version] {
			blocked[id] = true
			entry.sessions = append(entry.sessions, id)
		}
		deferred = append(deferred, entry)
		held[version] = true
	}
	if len(deferred) == 0 {
		return changes, values, nil, nil
	}
	apply, applyValues := changes[:0:0], values[:0:0]
	for i, change := range changes {
		if !held[change.DBVersion] {
			apply = append(apply, change)
			applyValues = append(applyValues, values[i])
		}
	}
	for i := range deferred {
		for _, change := range changes {
			if change.DBVersion == deferred[i].version {
				deferred[i].changes = append(deferred[i].changes, change)
			}
		}
	}
	return apply, applyValues, deferred, nil
}

func deferVersion(ctx context.Context, tx *sql.Tx, site []byte, version deferredVersion) error {
	sessions, err := json.Marshal(version.sessions)
	if err != nil {
		return err
	}
	changes, err := json.Marshal(version.changes)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO sync_deferred(peer,version,sessions,changes) VALUES (?,?,?,?)", site, version.version, string(sessions), string(changes))
	return err
}

// replayDeferred applies held-back versions in arrival order once the archive guard clears, and
// returns the sessions still blocked.
func (r *Replicator) replayDeferred(ctx context.Context) (map[string]bool, error) {
	s := r.s
	blocked := map[string]bool{}
	rows, err := s.Reader.QueryContext(ctx, "SELECT id,peer,version,sessions,changes FROM sync_deferred ORDER BY id")
	if err != nil {
		return nil, err
	}
	type row struct {
		id, version       int64
		peer              []byte
		sessions, changes string
	}
	var pending []row
	for rows.Next() {
		var entry row
		if err := rows.Scan(&entry.id, &entry.peer, &entry.version, &entry.sessions, &entry.changes); err != nil {
			rows.Close()
			return nil, err
		}
		pending = append(pending, entry)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, entry := range pending {
		var sessions []string
		var changes []Change
		if err := errors.Join(json.Unmarshal([]byte(entry.sessions), &sessions), json.Unmarshal([]byte(entry.changes), &changes)); err != nil {
			return nil, err
		}
		hold := false
		for _, id := range sessions {
			hold = hold || blocked[id]
		}
		var values []any
		if !hold {
			if values, err = decodeChanges(changes); err != nil {
				return nil, err
			}
			err = s.applyReplication(ctx, changes, values, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "DELETE FROM sync_deferred WHERE id=?", entry.id)
				return err
			})
			var data archiveDataError
			if errors.As(err, &data) {
				err = Transaction(ctx, s.DB, func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO sync_quarantine(peer,version,reason) VALUES (?,?,?)", entry.peer, entry.version, data.Error()); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, "DELETE FROM sync_deferred WHERE id=?", entry.id)
					return err
				})
				changes = nil
			}
			hold = errors.Is(err, ErrArchivePending)
			if err != nil && !hold {
				return nil, err
			}
			if !hold {
				r.signal(changes)
			}
		}
		if hold {
			for _, id := range sessions {
				blocked[id] = true
			}
		}
	}
	return blocked, nil
}

func (s *Store) replicatedColumns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.Reader.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)
 WHERE EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name=?||'__crsql_clock')`, table, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	if len(columns) > 0 {
		columns["-1"] = true
	}
	return columns, rows.Err()
}

func (s *Store) applyReplication(ctx context.Context, changes []Change, values []any, finish func(*sql.Tx) error) error {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Keep the writer leased between journaling and applying so a local prune cannot race the clock read.
	if err := s.preserveArchiveEdits(ctx, conn, changes, values); err != nil {
		return err
	}
	if err := Transaction(ctx, conn, func(tx *sql.Tx) error {
		if len(changes) > 0 {
			if err := s.applyChanges(ctx, tx, changes, values); err != nil {
				return err
			}
		}
		if finish != nil {
			return finish(tx)
		}
		return nil
	}); err != nil {
		return err
	}
	if len(changes) > 0 {
		// The extension caches its local clock, which replication advances directly.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	return nil
}
