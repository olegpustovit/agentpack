package network

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func projectsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := EnsurePrivateDirectory(filepath.Join(home, "network")); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestProjectsGroupsLaunchesIntoOneRow(t *testing.T) {
	home := projectsHome(t)
	directory := t.TempDir()
	for _, launch := range []SessionInfo{
		{Directory: directory, Harness: "claude", Mode: "default"},
		{Directory: directory, Harness: "codex", Mode: "default"},
		{Directory: directory, Harness: "grok", Mode: "review"},
	} {
		if err := remember(home, launch); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	calls := 0
	rows, err := Projects(context.Background(), home, func(_ context.Context, _ string) (ProjectContents, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return ProjectContents{Packages: []ProjectPackage{{Module: "github.com/o/r"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Launches) != 3 {
		t.Fatalf("want one row with three launches, got %+v", rows)
	}
	if calls != 1 || len(rows[0].Packages) != 1 {
		t.Fatalf("want one contents read and its packages, got calls=%d packages=%+v", calls, rows[0].Packages)
	}
}

func TestProjectsMergesTrailingSlashDuplicates(t *testing.T) {
	home := projectsHome(t)
	directory := t.TempDir()
	identity, err := FolderIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	older := time.Now().UTC().Add(-time.Hour)
	newer := time.Now().UTC()
	data, err := json.Marshal([]SessionInfo{
		{Directory: directory + string(filepath.Separator), Harness: "claude", Mode: "default", FolderID: identity, LastUsed: older},
		{Directory: directory, Harness: "claude", Mode: "default", FolderID: identity, LastUsed: newer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "network", "recent.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := Projects(context.Background(), home, func(_ context.Context, _ string) (ProjectContents, error) {
		return ProjectContents{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Directory != directory || len(rows[0].Launches) != 1 {
		t.Fatalf("want one merged row for %s, got %+v", directory, rows)
	}
	if !rows[0].LastUsed.Equal(newer) || !rows[0].Launches[0].LastUsed.Equal(newer) {
		t.Fatalf("want newest last_used, got %+v", rows[0])
	}
}

func TestProjectsDoesNotReadUnavailableFolder(t *testing.T) {
	home := projectsHome(t)
	directory := t.TempDir()
	if err := remember(home, SessionInfo{Directory: directory, Harness: "claude", Mode: "default"}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	var read []string
	rows, err := Projects(context.Background(), home, func(_ context.Context, dir string) (ProjectContents, error) {
		read = append(read, dir)
		return ProjectContents{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 0 {
		t.Fatalf("unavailable folder must not be read, read %v", read)
	}
	if len(rows) != 1 || rows[0].Available || rows[0].UnavailableReason == "" {
		t.Fatalf("want one unavailable row with a reason, got %+v", rows)
	}
	data, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"packages":[]`) || !strings.Contains(string(data), `"mcp":[]`) {
		t.Fatalf("empty lists must serialize as [], got %s", data)
	}
}

func TestProjectsIsolatesFailingFolder(t *testing.T) {
	home := projectsHome(t)
	good := t.TempDir()
	bad := t.TempDir()
	for _, directory := range []string{good, bad} {
		if err := remember(home, SessionInfo{Directory: directory, Harness: "claude", Mode: "default"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := Projects(context.Background(), home, func(_ context.Context, dir string) (ProjectContents, error) {
		if dir == bad {
			return ProjectContents{}, errors.New("lock unreadable")
		}
		return ProjectContents{Packages: []ProjectPackage{{Module: "github.com/o/r"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	byDirectory := map[string]ProjectRow{}
	for _, row := range rows {
		byDirectory[row.Directory] = row
	}
	if len(rows) != 2 || byDirectory[good].Error != "" || len(byDirectory[good].Packages) != 1 {
		t.Fatalf("healthy folder must keep its contents, got %+v", rows)
	}
	if byDirectory[bad].Error != "lock unreadable" || len(byDirectory[bad].Packages) != 0 {
		t.Fatalf("failing folder must carry only its error, got %+v", byDirectory[bad])
	}
}

func TestForgetRecentWithoutHarnessOrModeRemovesWholeFolder(t *testing.T) {
	home := projectsHome(t)
	directory := t.TempDir()
	other := t.TempDir()
	for _, launch := range []SessionInfo{
		{Directory: directory, Harness: "claude", Mode: "default"},
		{Directory: directory + string(filepath.Separator), Harness: "codex", Mode: "review"},
		{Directory: other, Harness: "claude", Mode: "default"},
	} {
		if err := remember(home, launch); err != nil {
			t.Fatal(err)
		}
	}
	if err := ForgetRecent(home, SessionInfo{Directory: directory}); err != nil {
		t.Fatal(err)
	}
	remaining, err := Recent(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].Directory != other {
		t.Fatalf("want only the other folder to remain, got %+v", remaining)
	}
}
