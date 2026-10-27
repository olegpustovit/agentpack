package network

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gofrs/flock"
)

func Recent(home string) ([]SessionInfo, error) {
	data, err := os.ReadFile(filepath.Join(home, "network", "recent.json"))
	if os.IsNotExist(err) {
		return []SessionInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	var recent []SessionInfo
	err = json.Unmarshal(data, &recent)
	return recent, err
}

type RecentFolder struct {
	SessionInfo
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// Availability is current filesystem state, never a persisted authorization grant.
func RecentFolders(home string) ([]RecentFolder, error) {
	recent, err := Recent(home)
	if err != nil {
		return nil, err
	}
	folders := make([]RecentFolder, 0, len(recent))
	for _, entry := range recent {
		folder := RecentFolder{SessionInfo: entry}
		identity, err := FolderIdentity(entry.Directory)
		switch {
		case err != nil:
			folder.UnavailableReason = "Folder is missing or inaccessible"
		case entry.FolderID == "" || identity != entry.FolderID:
			folder.UnavailableReason = "Folder changed since its last local launch"
		default:
			folder.Available = true
		}
		folders = append(folders, folder)
	}
	return folders, nil
}

func remember(home string, info SessionInfo) error { return updateRecent(home, info, false) }

func ForgetRecent(home string, info SessionInfo) error { return updateRecent(home, info, true) }

func updateRecent(home string, info SessionInfo, forget bool) error {
	if !forget {
		var err error
		info.LastUsed = time.Now().UTC()
		info.FolderID, err = FolderIdentity(info.Directory)
		if err != nil {
			return err
		}
	}
	lock := flock.New(filepath.Join(home, "network", "recent.lock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	recent, err := Recent(home)
	if err != nil {
		return err
	}
	recent = slices.DeleteFunc(recent, func(old SessionInfo) bool {
		return old.Directory == info.Directory && old.Harness == info.Harness && old.Mode == info.Mode
	})
	info.ID = ""
	info.State = TerminalState{}
	if !forget {
		recent = append([]SessionInfo{info}, recent...)
	}
	if len(recent) > 100 {
		recent = recent[:100]
	}
	data, err := json.Marshal(recent)
	if err != nil {
		return err
	}
	name := filepath.Join(home, "network", "recent.tmp")
	if err = os.WriteFile(name, data, 0600); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(home, "network", "recent.json"))
}
