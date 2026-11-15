package cache

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/olegpustovit/agentpack/internal/database"
	"github.com/olegpustovit/agentpack/internal/lockfile"
)

type EntryRecord struct {
	Kind          lockfile.PackageKind `json:"kind"`
	SourceURL     string               `json:"source_url"`
	Owner         string               `json:"owner"`
	Repo          string               `json:"repo"`
	Path          string               `json:"path"`
	Commit        string               `json:"commit"`
	FetchedAtUnix int64                `json:"fetched_at_unix"`
}

func UpsertEntry(cacheKey string, record EntryRecord, aliases []string) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("serialize cache entry: %w", err)
	}
	database, err := database.OpenCache(false)
	if err != nil {
		return err
	}
	tx, err := database.Begin()
	if err != nil {
		return errors.Join(err, database.Close())
	}
	defer tx.Rollback()
	_, updateErr := tx.Exec(`INSERT INTO cache_entries(cache_key,body) VALUES (?,?)
 ON CONFLICT(cache_key) DO UPDATE SET body=excluded.body`, cacheKey, encoded)
	for _, alias := range aliases {
		if updateErr != nil {
			break
		}
		key := strings.ToLower(strings.TrimSpace(alias))
		if key == "" {
			continue
		}
		_, updateErr = tx.Exec(`INSERT INTO aliases(alias,cache_key) VALUES (?,?)
 ON CONFLICT(alias) DO UPDATE SET cache_key=excluded.cache_key`, key, cacheKey)
	}
	if updateErr == nil {
		updateErr = tx.Commit()
	} else {
		updateErr = errors.Join(updateErr, tx.Rollback())
	}
	closeErr := database.Close()
	if updateErr != nil {
		if closeErr != nil {
			return errors.Join(fmt.Errorf("update cache database: %w", updateErr), fmt.Errorf("close cache database: %w", closeErr))
		}
		return fmt.Errorf("update cache database: %w", updateErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close cache database: %w", closeErr)
	}
	return nil
}

func LookupAlias(alias string) (string, bool, error) {
	var key string
	err := viewDatabase(func(db *sql.DB) error {
		err := db.QueryRow("SELECT cache_key FROM aliases WHERE alias=?", strings.ToLower(strings.TrimSpace(alias))).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return key, key != "", err
}

func GetEntry(cacheKey string) (EntryRecord, bool, error) {
	var record EntryRecord
	var found bool
	err := viewDatabase(func(db *sql.DB) error {
		var body []byte
		err := db.QueryRow("SELECT body FROM cache_entries WHERE cache_key=?", cacheKey).Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(body, &record); err != nil {
			return fmt.Errorf("deserialize cache entry %q: %w", cacheKey, err)
		}
		found = true
		return nil
	})
	return record, found, err
}

func ListKeys() ([]string, error) {
	var keys []string
	err := viewDatabase(func(db *sql.DB) error {
		rows, err := db.Query("SELECT cache_key FROM cache_entries ORDER BY cache_key")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			keys = append(keys, key)
		}
		return rows.Err()
	})
	return keys, err
}

func AliasesForGitHubEntry(owner, repo, inRepoPath, packageName string) []string {
	owner = strings.ToLower(owner)
	repo = strings.ToLower(repo)
	path := strings.Trim(inRepoPath, "/")
	aliases := make([]string, 0, 2)
	if path == "" {
		aliases = append(aliases, owner+"/"+repo)
	} else {
		aliases = append(aliases, owner+"/"+repo+"/"+path)
	}
	if name := strings.ToLower(strings.TrimSpace(packageName)); name != "" {
		aliases = append(aliases, name)
	}
	return aliases
}

func viewDatabase(view func(*sql.DB) error) error {
	database, err := database.OpenCache(true)
	if err != nil {
		return err
	}
	if database == nil {
		return nil
	}
	viewErr := view(database)
	closeErr := database.Close()
	if viewErr != nil {
		if closeErr != nil {
			return errors.Join(fmt.Errorf("read cache database: %w", viewErr), fmt.Errorf("close cache database: %w", closeErr))
		}
		return fmt.Errorf("read cache database: %w", viewErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close cache database: %w", closeErr)
	}
	return nil
}
