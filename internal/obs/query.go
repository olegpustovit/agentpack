package obs

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Filter struct {
	Since                                                time.Time
	Kind, Level, Session, Node, Trace, Fingerprint, Grep string
	Limit                                                int
}

func readOnly(path string) (*sql.DB, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("diagnostic database must be a regular file: %s", path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(absolute))+"?agentpack_plain=1&mode=ro&_query_only=1&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func logDays(home string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(home, "logs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var days []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		if _, err := time.Parse(time.DateOnly, strings.TrimSuffix(entry.Name(), ".db")); err == nil {
			days = append(days, entry.Name())
		}
	}
	slices.Sort(days)
	slices.Reverse(days)
	return days, nil
}

// Query reads retained day files and unconsumed spool lines without starting a writer.
func Query(ctx context.Context, home string, filter Filter) ([]Event, error) {
	if filter.Limit == 0 {
		filter.Limit = 100
	}
	if filter.Limit < 1 || filter.Limit > 10000 {
		return nil, errors.New("limit must be between 1 and 10000")
	}
	return queryEvents(ctx, home, filter)
}

func TraceEvents(ctx context.Context, home, trace string) ([]Event, error) {
	if trace == "" {
		return nil, errors.New("trace ID is required")
	}
	return queryEvents(ctx, home, Filter{Trace: trace})
}

func queryEvents(ctx context.Context, home string, filter Filter) ([]Event, error) {
	if filter.Level != "" && filter.Level != "info" && filter.Level != "error" {
		return nil, errors.New("level must be info or error")
	}
	if filter.Trace != "" {
		decoded, err := hex.DecodeString(filter.Trace)
		if err != nil || len(decoded) != 16 || strings.Trim(filter.Trace, "0") == "" {
			return nil, errors.New("trace ID must contain 32 hexadecimal characters")
		}
		filter.Trace = strings.ToLower(filter.Trace)
	}
	days, err := logDays(home)
	if err != nil {
		return nil, err
	}
	result := []Event{}
	seen := map[string]bool{}
	processed := map[string]map[string]int64{}
	accept := func(event Event) {
		key := event.TraceID + ":" + event.SpanID + ":" + event.NodeID
		if !seen[key] {
			seen[key] = true
			result = append(result, event.Sanitized())
		}
	}
	for _, day := range days {
		if !filter.Since.IsZero() && strings.TrimSuffix(day, ".db") < filter.Since.UTC().Format(time.DateOnly) {
			continue
		}
		db, err := readOnly(filepath.Join(home, "logs", day))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		events, cursors, readErr := queryDay(ctx, db, filter)
		if err := errors.Join(readErr, db.Close()); err != nil {
			return nil, fmt.Errorf("read log day %s: %w", day, err)
		}
		processed[strings.TrimSuffix(day, ".db")] = cursors
		for _, event := range events {
			accept(event)
		}
	}
	files, err := spoolFiles(filepath.Join(home, "logs", "spool"))
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		_, err := readSpoolFile(ctx, filepath.Join(home, "logs", "spool", file.name), func(event Event) error {
			if event.sourceLine > processed[event.TS.UTC().Format(time.DateOnly)][file.name] && matches(event, filter) {
				accept(event)
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	slices.SortFunc(result, func(a, b Event) int {
		order := a.TS.Compare(b.TS)
		if order == 0 {
			order = strings.Compare(a.NodeID+a.TraceID+a.SpanID, b.NodeID+b.TraceID+b.SpanID)
		}
		if filter.Trace == "" {
			return -order
		}
		return order
	})
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func queryDay(ctx context.Context, db *sql.DB, filter Filter) ([]Event, map[string]int64, error) {
	var tables int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name IN ('events','stacks','meta')").Scan(&tables); err != nil {
		return nil, nil, err
	}
	if tables != 3 {
		return nil, nil, nil
	} // A concurrent writer may still be creating its first day.
	cursors := map[string]int64{}
	rows, err := db.QueryContext(ctx, "SELECT substr(key,7),CAST(value AS INTEGER) FROM meta WHERE key LIKE 'spool:%'")
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var name string
		var line int64
		if err := rows.Scan(&name, &line); err != nil {
			rows.Close()
			return nil, nil, err
		}
		cursors[name] = line
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	query := `SELECT e.ts,e.duration_us,e.trace_id,e.span_id,e.node_id,e.proc,e.kind,e.name,e.level,e.status,COALESCE(e.session_id,''),e.user,COALESCE(e.fingerprint,''),COALESCE(s.text,''),json(e.attrs) FROM events e LEFT JOIN stacks s ON s.hash=e.stack WHERE 1=1`
	args := []any{}
	if !filter.Since.IsZero() {
		query += " AND e.ts>=?"
		args = append(args, filter.Since.UnixMicro())
	}
	for _, part := range []struct{ column, value string }{{"e.kind", filter.Kind}, {"e.level", filter.Level}, {"e.session_id", filter.Session}, {"e.node_id", filter.Node}, {"e.fingerprint", filter.Fingerprint}} {
		if part.value != "" {
			query += " AND " + part.column + "=?"
			args = append(args, part.value)
		}
	}
	if filter.Trace != "" {
		value, _ := hex.DecodeString(filter.Trace)
		query += " AND e.trace_id=?"
		args = append(args, value)
	}
	if filter.Grep != "" {
		query += " AND instr(lower(e.name||' '||e.kind||' '||e.node_id||' '||e.proc||' '||COALESCE(e.session_id,'')||' '||json(e.attrs)||' '||COALESCE(s.text,'')),lower(?))>0"
		args = append(args, filter.Grep)
	}
	if filter.Trace != "" {
		query += " ORDER BY e.ts,e.id"
	} else {
		query += " ORDER BY e.ts DESC,e.id DESC"
	}
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err = db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		var at int64
		var trace, span []byte
		var attrs string
		if err := rows.Scan(&at, &event.DurationUS, &trace, &span, &event.NodeID, &event.Proc, &event.Kind, &event.Name, &event.Level, &event.Status, &event.SessionID, &event.User, &event.Fingerprint, &event.Stack, &attrs); err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(attrs), &event.Attrs); err != nil {
			return nil, nil, err
		}
		event.TS = time.UnixMicro(at).UTC()
		event.TraceID = hex.EncodeToString(trace)
		event.SpanID = hex.EncodeToString(span)
		events = append(events, event)
	}
	return events, cursors, rows.Err()
}

func matches(event Event, filter Filter) bool {
	if !filter.Since.IsZero() && event.TS.Before(filter.Since) {
		return false
	}
	for _, part := range [][2]string{{filter.Kind, event.Kind}, {filter.Level, event.Level}, {filter.Session, event.SessionID}, {filter.Node, event.NodeID}, {filter.Trace, event.TraceID}, {filter.Fingerprint, event.Fingerprint}} {
		if part[0] != "" && part[0] != part[1] {
			return false
		}
	}
	if filter.Grep != "" {
		data, _ := json.Marshal(event.Sanitized().Attrs)
		text := strings.Join([]string{event.Name, event.Kind, event.NodeID, event.Proc, event.SessionID, string(data), event.Stack}, " ")
		return strings.Contains(strings.ToLower(text), strings.ToLower(filter.Grep))
	}
	return true
}
