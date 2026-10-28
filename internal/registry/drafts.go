package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/lockfile"
)

type PackagePin struct {
	Commit      string `json:"commit"`
	ContentHash string `json:"content_hash,omitempty"`
	CacheKey    string `json:"cache_key"`
}

type Draft struct {
	ID             string                `json:"id"`
	Item           string                `json:"item"`
	Revision       string                `json:"revision,omitempty"`
	Node           string                `json:"node"`
	Directory      string                `json:"directory"`
	Reason         string                `json:"reason"`
	PackID         string                `json:"pack_id,omitempty"`
	FolderIdentity string                `json:"folder_identity"`
	ExpectedPins   map[string]PackagePin `json:"expected_pins"`
	NeededHolder   string                `json:"needed_holder,omitempty"`
	CreatedAt      time.Time             `json:"created_at"`
}

type DraftInput struct {
	Item           string                `json:"item"`
	Revision       string                `json:"revision,omitempty"`
	Node           string                `json:"node"`
	Directory      string                `json:"directory"`
	Reason         string                `json:"reason"`
	PackID         string                `json:"pack_id,omitempty"`
	FolderIdentity string                `json:"folder_identity"`
	ExpectedPins   map[string]PackagePin `json:"expected_pins"`
	NeededHolder   string                `json:"needed_holder,omitempty"`
}

type DraftState struct {
	Reachable      bool                  `json:"reachable"`
	FolderIdentity string                `json:"folder_identity"`
	Pins           map[string]PackagePin `json:"pins"`
	CurrentDiff    *DiffResult           `json:"current_diff,omitempty"`
	MissingObjects []string              `json:"missing_objects,omitempty"`
	NeededHolder   string                `json:"needed_holder,omitempty"`
}

type DraftReview struct {
	Draft       Draft       `json:"draft"`
	Ready       bool        `json:"ready"`
	PinsMatch   bool        `json:"pins_match"`
	FolderMatch bool        `json:"folder_match"`
	Reason      string      `json:"reason"`
	Diff        *DiffResult `json:"diff,omitempty"`
}

func (s *Store) SaveDraft(input DraftInput) (Draft, error) {
	if strings.TrimSpace(input.Item) == "" || strings.TrimSpace(input.Node) == "" || strings.TrimSpace(input.Directory) == "" || strings.TrimSpace(input.Reason) == "" || strings.TrimSpace(input.FolderIdentity) == "" {
		return Draft{}, fmt.Errorf("draft item, node, directory, reason and folder identity are required")
	}
	if input.Revision != "" && !validDigest(input.Revision) || input.PackID != "" && !validID(input.PackID) {
		return Draft{}, fmt.Errorf("invalid draft reference")
	}
	for module, pin := range input.ExpectedPins {
		if module == "" || pin.Commit == "" || !validCacheKey(pin.CacheKey) || pin.ContentHash != "" && !lockfile.ValidContentHash(pin.ContentHash) {
			return Draft{}, fmt.Errorf("invalid draft package pin")
		}
	}
	id, err := randomID()
	if err != nil {
		return Draft{}, err
	}
	draft := Draft{ID: id, Item: input.Item, Revision: input.Revision, Node: input.Node, Directory: input.Directory, Reason: input.Reason, PackID: input.PackID, FolderIdentity: input.FolderIdentity, ExpectedPins: input.ExpectedPins, NeededHolder: input.NeededHolder, CreatedAt: time.Now().UTC()}
	if draft.ExpectedPins == nil {
		draft.ExpectedPins = map[string]PackagePin{}
	}
	data, err := json.Marshal(draft)
	if err != nil {
		return Draft{}, err
	}
	if err := writeAtomic(filepath.Join(s.root, draftsDir, id+".json"), data, 0o600); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func (s *Store) ListDrafts() ([]Draft, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, draftsDir))
	if err != nil {
		return nil, err
	}
	drafts := make([]Draft, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		draft, err := s.readDraft(id)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, draft)
	}
	sort.Slice(drafts, func(i, j int) bool {
		if drafts[i].CreatedAt.Equal(drafts[j].CreatedAt) {
			return drafts[i].ID < drafts[j].ID
		}
		return drafts[i].CreatedAt.Before(drafts[j].CreatedAt)
	})
	return drafts, nil
}

func (s *Store) GetDraft(id string) (Draft, error) { return s.readDraft(id) }

func (s *Store) RevalidateDraft(id string, state DraftState) (DraftReview, error) {
	draft, err := s.readDraft(id)
	if err != nil {
		return DraftReview{}, err
	}
	review := DraftReview{Draft: draft, Diff: state.CurrentDiff}
	switch {
	case !state.Reachable:
		if draft.NeededHolder != "" {
			review.Reason = "machine is offline; exact package bytes are available on " + draft.NeededHolder
		} else {
			review.Reason = "machine is offline"
		}
	case state.FolderIdentity == "" || state.FolderIdentity != draft.FolderIdentity:
		review.Reason = "destination folder changed"
	case state.NeededHolder != "":
		review.Reason = "exact package bytes are available only on " + state.NeededHolder
	case len(state.MissingObjects) != 0:
		review.Reason = "exact package objects are unavailable"
	case state.CurrentDiff == nil:
		review.Reason = "current diff is unavailable"
	default:
		review.Ready = true
		review.FolderMatch = true
		review.PinsMatch = equalPins(draft.ExpectedPins, state.Pins)
		if review.PinsMatch {
			review.Reason = "ready for review"
		} else {
			review.Reason = "destination pins changed; review the refreshed diff"
		}
	}
	return review, nil
}

func equalPins(expected, actual map[string]PackagePin) bool {
	if len(expected) != len(actual) {
		return false
	}
	for module, pin := range expected {
		if actual[module] != pin {
			return false
		}
	}
	return true
}

func (s *Store) DeleteDraft(id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid draft id")
	}
	if _, err := s.readDraft(id); err != nil {
		return err
	}
	return removeDurable(filepath.Join(s.root, draftsDir, id+".json"))
}

func (s *Store) readDraft(id string) (Draft, error) {
	var draft Draft
	if !validID(id) {
		return draft, fmt.Errorf("invalid draft id")
	}
	data, err := readRegular(filepath.Join(s.root, draftsDir, id+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return draft, ErrNotFound
		}
		return draft, err
	}
	if err := json.Unmarshal(data, &draft); err != nil || draft.ID != id || draft.Item == "" || draft.Node == "" || draft.Directory == "" || draft.Reason == "" || draft.FolderIdentity == "" || draft.CreatedAt.IsZero() {
		return Draft{}, fmt.Errorf("invalid registry draft")
	}
	for module, pin := range draft.ExpectedPins {
		if module == "" || pin.Commit == "" || !validCacheKey(pin.CacheKey) || pin.ContentHash != "" && !lockfile.ValidContentHash(pin.ContentHash) {
			return Draft{}, fmt.Errorf("invalid registry draft package pin")
		}
	}
	return draft, nil
}
