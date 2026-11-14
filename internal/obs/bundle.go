package obs

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// IssueEvents includes the complete retained traces containing this issue.
func IssueEvents(ctx context.Context, home, fingerprint string) ([]Event, error) {
	if !validFingerprint(fingerprint) {
		return nil, errors.New("fingerprint must contain 64 hexadecimal characters")
	}
	occurrences, err := queryEvents(ctx, home, Filter{Fingerprint: strings.ToLower(fingerprint), Level: "error"})
	if err != nil {
		return nil, err
	}
	traces := map[string]bool{}
	result := []Event{}
	for _, event := range occurrences {
		if traces[event.TraceID] {
			continue
		}
		traces[event.TraceID] = true
		events, err := TraceEvents(ctx, home, event.TraceID)
		if err != nil {
			return nil, err
		}
		result = append(result, events...)
	}
	slices.SortFunc(result, func(a, b Event) int { return a.TS.Compare(b.TS) })
	return result, nil
}

type DiagnosticRows struct {
	Nodes         []map[string]any `json:"mesh_nodes"`
	Sessions      []map[string]any `json:"sessions"`
	MissingTables []string         `json:"unavailable_tables,omitempty"`
}

// RelatedRows reads only the involved session and mesh-node records, never credentials.
func RelatedRows(ctx context.Context, home string, nodes, sessions []string) (DiagnosticRows, error) {
	result := DiagnosticRows{Nodes: []map[string]any{}, Sessions: []map[string]any{}}
	db, err := readOnly(filepath.Join(home, "state.db"))
	if errors.Is(err, os.ErrNotExist) {
		result.MissingTables = []string{"mesh_nodes", "sessions"}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer db.Close()
	for _, table := range []struct {
		name, key string
		ids       []string
		into      *[]map[string]any
	}{{"mesh_nodes", "node_id", nodes, &result.Nodes}, {"sessions", "id", sessions, &result.Sessions}} {
		var exists int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", table.name).Scan(&exists); err != nil {
			return result, err
		}
		if exists == 0 {
			result.MissingTables = append(result.MissingTables, table.name)
			continue
		}
		for _, id := range table.ids {
			row, err := diagnosticRow(ctx, db, table.name, table.key, id)
			if err != nil {
				return result, err
			}
			if row != nil {
				*table.into = append(*table.into, row)
			}
		}
	}
	return result, nil
}

func diagnosticRow(ctx context.Context, db *sql.DB, table, key, id string) (map[string]any, error) {
	// Table and column names are constants from RelatedRows, not caller SQL.
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+table+" WHERE "+key+"=?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, rows.Err()
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	if err := rows.Scan(pointers...); err != nil {
		return nil, err
	}
	row := map[string]any{}
	for i, name := range columns {
		value := values[i]
		if bytes, ok := value.([]byte); ok {
			value = string(bytes)
		}
		if name == "name" || name == "started_by" {
			value = "<private>"
		}
		row[name] = value
	}
	return row, rows.Err()
}
