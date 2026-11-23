package state

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The web UI maps every replicated table to the queries it feeds (ui/src/features/events/use-events.tsx).
// This file is its list of tables; a Vitest fails when the map misses one.
const replicatedTablesFile = "../../ui/src/generated/replicated-tables.json"

func TestReplicatedTablesMatchUIList(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.Reader.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name LIKE '%\_\_crsql\_clock' ESCAPE '\' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	tables := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, strings.TrimSuffix(name, "__crsql_clock"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want, err := json.MarshalIndent(tables, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("UPDATE_REPLICATED_TABLES") != "" {
		if err := os.WriteFile(filepath.FromSlash(replicatedTablesFile), want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.FromSlash(replicatedTablesFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; run UPDATE_REPLICATED_TABLES=1 go test ./internal/state -run TestReplicatedTablesMatchUIList and map new tables in use-events.tsx\ngot:\n%s\nwant:\n%s", replicatedTablesFile, got, want)
	}
}
