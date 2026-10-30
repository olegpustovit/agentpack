package network

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// ProjectContents is one folder's pack.lock as a node reports it.
type ProjectContents struct {
	Packages []ProjectPackage `json:"packages"`
	MCP      []ProjectMCP     `json:"mcp"`
}

type ProjectPackage struct {
	Module      string `json:"module"`
	Commit      string `json:"commit"`
	ContentHash string `json:"content_hash"`
	CacheKey    string `json:"cache_key"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Direct      bool   `json:"direct"`
}

type ProjectMCP struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	Launcher  string `json:"launcher"`
	Status    string `json:"status"`
	Package   string `json:"package"`
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
	Host      string `json:"host"`
	Reason    string `json:"reason"`
}

type ProjectLaunch struct {
	Harness  string    `json:"harness"`
	Mode     string    `json:"mode"`
	LastUsed time.Time `json:"last_used"`
}

// ProjectRow is one folder. Launches are unique per harness and mode, newest first.
type ProjectRow struct {
	Directory         string           `json:"directory"`
	Name              string           `json:"name"`
	FolderID          string           `json:"folder_id"`
	Available         bool             `json:"available"`
	UnavailableReason string           `json:"unavailable_reason"`
	LastUsed          time.Time        `json:"last_used"`
	Launches          []ProjectLaunch  `json:"launches"`
	Packages          []ProjectPackage `json:"packages"`
	MCP               []ProjectMCP     `json:"mcp"`
	Error             string           `json:"error"`
}

const projectReadConcurrency = 4

// Projects groups recent launches by cleaned directory. Contents are read only for
// available folders, at most projectReadConcurrency at a time; a failed read sets
// Error on that row alone.
func Projects(ctx context.Context, home string, contents func(context.Context, string) (ProjectContents, error)) ([]ProjectRow, error) {
	folders, err := RecentFolders(home)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(folders, func(a, b RecentFolder) int { return b.LastUsed.Compare(a.LastUsed) })
	rows := []ProjectRow{}
	index := map[string]int{}
	for _, folder := range folders {
		directory := filepath.Clean(folder.Directory)
		i, ok := index[directory]
		if !ok {
			i = len(rows)
			index[directory] = i
			// The newest launch describes the folder's current identity and availability.
			rows = append(rows, ProjectRow{
				Directory:         directory,
				Name:              filepath.Base(directory),
				FolderID:          folder.FolderID,
				Available:         folder.Available,
				UnavailableReason: folder.UnavailableReason,
				LastUsed:          folder.LastUsed,
				Launches:          []ProjectLaunch{},
				Packages:          []ProjectPackage{},
				MCP:               []ProjectMCP{},
			})
		}
		row := &rows[i]
		if !slices.ContainsFunc(row.Launches, func(launch ProjectLaunch) bool {
			return launch.Harness == folder.Harness && launch.Mode == folder.Mode
		}) {
			row.Launches = append(row.Launches, ProjectLaunch{Harness: folder.Harness, Mode: folder.Mode, LastUsed: folder.LastUsed})
		}
	}

	slots := make(chan struct{}, projectReadConcurrency)
	var wg sync.WaitGroup
	for i := range rows {
		if !rows[i].Available {
			continue
		}
		wg.Add(1)
		go func(row *ProjectRow) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			read, err := contents(ctx, row.Directory)
			if err != nil {
				row.Error = err.Error()
				return
			}
			row.Packages = append([]ProjectPackage{}, read.Packages...)
			row.MCP = append([]ProjectMCP{}, read.MCP...)
		}(&rows[i])
	}
	wg.Wait()

	slices.SortFunc(rows, func(a, b ProjectRow) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Directory, b.Directory)
	})
	return rows, nil
}
