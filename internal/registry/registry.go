// Package registry stores immutable, content-addressed package records and
// agentpack revisions under the user's agentpack home.
package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/lockfile"
)

const (
	objectsDir      = "objects"
	bindingsDir     = "bindings"
	metadataDir     = "metadata"
	capturesDir     = "captures"
	transactionsDir = "transactions"
	peersDir        = "peers"
	draftsDir       = "drafts"
	tombstonesDir   = "tombstones"
	indexName       = "index.json"
	schema          = 1
	objectSchema    = 1
)

var ErrNotFound = errors.New("registry item not found")

type Store struct{ root string }

// New opens $AGENTPACK_HOME/registry. home is the already-resolved agentpack home.
func New(home string) (*Store, error) {
	if strings.TrimSpace(home) == "" {
		return nil, fmt.Errorf("agentpack home is required")
	}
	root, err := filepath.Abs(filepath.Join(home, "registry"))
	if err != nil {
		return nil, err
	}
	return open(root)
}

func open(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create registry: %w", err)
	}
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("registry root must be a real directory")
	}
	for _, name := range []string{objectsDir, bindingsDir, metadataDir, capturesDir, transactionsDir, peersDir, draftsDir, tombstonesDir} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			return nil, fmt.Errorf("create registry storage: %w", err)
		}
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect registry: %w", err)
	}
	for _, name := range []string{objectsDir, bindingsDir, metadataDir, capturesDir, transactionsDir, peersDir, draftsDir, tombstonesDir} {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("registry storage directory is invalid")
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return nil, err
		}
	}
	return &Store{root: root}, nil
}

type Package struct {
	ID          string `json:"id"`
	Schema      int    `json:"schema,omitempty"`
	Module      string `json:"module"`
	Direct      bool   `json:"direct,omitempty"`
	Commit      string `json:"commit"`
	ContentHash string `json:"content_hash,omitempty"`
	Kind        string `json:"kind"`
	URL         string `json:"url"`
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	Path        string `json:"path,omitempty"`
	CacheKey    string `json:"cache_key"`
	Name        string `json:"name,omitempty"`
	SourceNode  string `json:"source_node,omitempty"`
	Project     string `json:"project,omitempty"`
}

type Revision struct {
	ID                      string    `json:"id"`
	Schema                  int       `json:"schema,omitempty"`
	AgentpackID             string    `json:"agentpack_id"`
	ParentIDs               []string  `json:"parent_ids,omitempty"`
	Manifest                string    `json:"manifest"`
	Lock                    string    `json:"lock"`
	PackageIDs              []string  `json:"package_ids,omitempty"`
	SourceNode              string    `json:"source_node,omitempty"`
	Project                 string    `json:"project,omitempty"`
	CreatedAt               time.Time `json:"created_at"`
	Label                   string    `json:"label,omitempty"`
	Mode                    string    `json:"mode,omitempty"`
	NeedsLocalConfiguration bool      `json:"needs_local_configuration,omitempty"`
}

type Agentpack struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Revisions []string `json:"revisions"`
	Heads     []string `json:"heads"`
}

type CaptureInput struct {
	ID         string
	Name       string
	Label      string
	Mode       string
	SourceNode string
	Project    string
	Manifest   []byte
	Lock       []byte
	CreatedAt  time.Time
	ParentIDs  []string
}

type DiffResult struct {
	Add    []string `json:"add"`
	Update []string `json:"update"`
	Remove []string `json:"remove"`
	Mode   string   `json:"mode"`
}

type bindings struct {
	Values map[string]string `json:"values"`
}

// Capture appends a content-addressed revision, creating a stable agentpack ID when absent.
func (s *Store) Capture(input CaptureInput) (Agentpack, Revision, error) {
	var pack Agentpack
	var revision Revision
	err := s.withLock(func() error {
		var err error
		pack, revision, err = s.captureUnlocked(input)
		return err
	})
	return pack, revision, err
}

func (s *Store) captureUnlocked(input CaptureInput) (Agentpack, Revision, error) {
	var pack Agentpack
	var revision Revision
	if strings.TrimSpace(input.Project) == "" {
		return pack, revision, fmt.Errorf("project is required")
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = time.Now().UTC()
	}
	cleanManifest, manifestBindings, err := normalizeTOML(input.Manifest)
	if err != nil {
		return pack, revision, fmt.Errorf("normalize agentpack.toml: %w", safeParseError{cause: err})
	}
	cleanLock, lockBindings, err := normalizeTOML(input.Lock)
	if err != nil {
		return pack, revision, fmt.Errorf("normalize pack.lock: %w", safeParseError{cause: err})
	}
	lockValue, err := validateSnapshot(cleanManifest, cleanLock)
	if err != nil {
		return pack, revision, err
	}

	idx, err := s.readIndexUnlocked()
	if err != nil {
		return pack, revision, err
	}
	if input.ID == "" {
		input.ID, err = randomID()
		if err != nil {
			return pack, revision, err
		}
	}
	if !validID(input.ID) {
		return pack, revision, fmt.Errorf("invalid agentpack id")
	}
	if _, deleted, err := s.tombstoneFor(input.ID); err != nil {
		return pack, revision, err
	} else if deleted {
		return pack, revision, fmt.Errorf("agentpack was deleted and cannot be captured")
	}
	pack, exists := idx.Agentpacks[input.ID]
	if !exists {
		pack = Agentpack{ID: input.ID, Name: strings.TrimSpace(input.Name)}
		if pack.Name == "" {
			pack.Name = filepath.Base(input.Project)
		}
		if pack.Name == "" || pack.Name == "." {
			pack.Name = "agentpack"
		}
		if credentialShaped(pack.Name) {
			return pack, revision, fmt.Errorf("agentpack name contains credential-shaped data")
		}
		pack.Revisions = []string{}
	}
	if len(input.ParentIDs) == 0 && len(pack.Revisions) > 0 {
		if len(pack.Heads) > 1 {
			return pack, revision, fmt.Errorf("agentpack has multiple heads; choose a parent revision")
		}
		if len(pack.Heads) == 1 {
			input.ParentIDs = []string{pack.Heads[0]}
		} else {
			input.ParentIDs = []string{pack.Revisions[len(pack.Revisions)-1]}
		}
	}
	for _, parent := range input.ParentIDs {
		parentRevision, err := s.readRevision(parent)
		if err != nil {
			return pack, revision, fmt.Errorf("read parent revision: %w", err)
		}
		if parentRevision.AgentpackID != input.ID {
			return pack, revision, fmt.Errorf("parent revision belongs to another agentpack")
		}
	}
	if len(pack.Revisions) > 0 {
		for _, head := range pack.Heads {
			latest, err := s.readRevision(head)
			if err != nil {
				return pack, revision, err
			}
			if latest.Manifest == string(cleanManifest) && latest.Lock == string(cleanLock) && latest.Mode == input.Mode {
				allBindings := bindings{Values: mergeBindings(manifestBindings, lockBindings, "manifest:", "lock:")}
				if len(allBindings.Values) > 0 {
					if err := s.writeBindings(latest.ID, allBindings); err != nil {
						return pack, revision, err
					}
				}
				return pack, latest, nil
			}
		}
	}
	packageIDs := make([]string, 0, len(lockValue.Packages))
	for _, p := range lockValue.Packages {
		item := Package{Schema: objectSchema, Module: p.Module, Direct: p.Direct, Commit: p.Commit, ContentHash: p.ContentHash, Kind: string(p.Kind), URL: p.URL, Owner: p.Owner, Repo: p.Repo, Path: p.Path, CacheKey: p.CacheKey, Name: p.Name, SourceNode: redactMetadata(input.SourceNode), Project: redactMetadata(input.Project)}
		id, err := s.writeObject(item)
		if err != nil {
			return pack, revision, err
		}
		packageIDs = append(packageIDs, id)
	}
	sort.Strings(packageIDs)
	revision = Revision{Schema: objectSchema, AgentpackID: input.ID, ParentIDs: sorted(input.ParentIDs), Manifest: string(cleanManifest), Lock: string(cleanLock), PackageIDs: packageIDs, SourceNode: redactMetadata(input.SourceNode), Project: redactMetadata(input.Project), CreatedAt: input.CreatedAt.UTC(), Label: redactMetadata(input.Label), Mode: input.Mode, NeedsLocalConfiguration: len(manifestBindings)+len(lockBindings) > 0}
	revision.ID, err = s.writeObject(revision)
	if err != nil {
		return pack, revision, err
	}
	// Persist local values only after public object IDs are known. They never enter object content or its digest.
	allBindings := bindings{Values: mergeBindings(manifestBindings, lockBindings, "manifest:", "lock:")}
	if len(allBindings.Values) > 0 {
		if err := s.writeBindings(revision.ID, allBindings); err != nil {
			return pack, revision, err
		}
	}
	if !exists {
		if _, err := s.writeName(input.ID, pack.Name, input.SourceNode, input.CreatedAt); err != nil {
			return pack, revision, err
		}
	}
	for _, id := range packageIDs {
		idx.Packages = appendUnique(idx.Packages, id)
	}
	pack.Revisions = append(pack.Revisions, revision.ID)
	pack.Heads, err = s.revisionHeads(pack.Revisions)
	if err != nil {
		return pack, revision, err
	}
	idx.Agentpacks[input.ID] = pack
	if err := s.writeMetadata(pack); err != nil {
		return pack, revision, err
	}
	if err := s.writeIndexUnlocked(idx); err != nil {
		return pack, revision, err
	}
	return pack, revision, nil
}

// ListAgentpacks returns named packs from the rebuildable index.
func (s *Store) ListAgentpacks() ([]Agentpack, error) {
	idx, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	out := make([]Agentpack, 0, len(idx.Agentpacks))
	for _, p := range idx.Agentpacks {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListPackages loads and verifies every indexed package object.
func (s *Store) ListPackages() ([]Package, error) {
	idx, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	out := make([]Package, 0, len(idx.Packages))
	for _, id := range idx.Packages {
		p, err := s.readPackage(id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Module == out[j].Module {
			return out[i].Commit < out[j].Commit
		}
		return out[i].Module < out[j].Module
	})
	return out, nil
}

func (s *Store) Get(id string) (Agentpack, []Revision, error) {
	idx, err := s.readIndex()
	if err != nil {
		return Agentpack{}, nil, err
	}
	p, ok := idx.Agentpacks[id]
	if !ok {
		return Agentpack{}, nil, ErrNotFound
	}
	revisions := make([]Revision, 0, len(p.Revisions))
	for _, rid := range p.Revisions {
		r, err := s.readRevision(rid)
		if err != nil {
			return p, nil, err
		}
		revisions = append(revisions, r)
	}
	return p, revisions, nil
}

func (s *Store) Rename(id, name string) (Agentpack, error) {
	name = strings.TrimSpace(name)
	if name == "" || credentialShaped(name) {
		return Agentpack{}, fmt.Errorf("invalid agentpack name")
	}
	var result Agentpack
	err := s.withLock(func() error {
		idx, err := s.readIndexUnlocked()
		if err != nil {
			return err
		}
		p, ok := idx.Agentpacks[id]
		if !ok {
			return ErrNotFound
		}
		if p.Name == name {
			result = p
			return nil
		}
		node, _ := os.Hostname()
		if _, err := s.writeName(id, name, node, time.Now().UTC()); err != nil {
			return err
		}
		p.Name = name
		if err := s.writeMetadata(p); err != nil {
			return err
		}
		idx.Agentpacks[id] = p
		if err := s.writeIndexUnlocked(idx); err != nil {
			return err
		}
		result = p
		return nil
	})
	return result, err
}

// Diff compares package pins and selected mode from two verified revisions.
func (s *Store) Diff(fromID, toID string) (DiffResult, error) {
	from, err := s.readRevision(fromID)
	if err != nil {
		return DiffResult{}, err
	}
	to, err := s.readRevision(toID)
	if err != nil {
		return DiffResult{}, err
	}
	a, err := s.revisionPackages(from)
	if err != nil {
		return DiffResult{}, err
	}
	b, err := s.revisionPackages(to)
	if err != nil {
		return DiffResult{}, err
	}
	result := DiffResult{Add: []string{}, Update: []string{}, Remove: []string{}, Mode: to.Mode}
	for module, p := range b {
		old, ok := a[module]
		if !ok {
			result.Add = append(result.Add, module)
		} else if packagePinChanged(old, p) {
			result.Update = append(result.Update, module)
		}
	}
	for module := range a {
		if _, ok := b[module]; !ok {
			result.Remove = append(result.Remove, module)
		}
	}
	sort.Strings(result.Add)
	sort.Strings(result.Update)
	sort.Strings(result.Remove)
	return result, nil
}

func packagePinChanged(a, b Package) bool {
	return a.Module != b.Module || a.Direct != b.Direct || a.Commit != b.Commit || a.ContentHash != b.ContentHash || a.Kind != b.Kind || a.URL != b.URL || a.Owner != b.Owner || a.Repo != b.Repo || a.Path != b.Path || a.CacheKey != b.CacheKey || a.Name != b.Name
}

// Preview exposes the redacted target snapshot; bindings are private to this machine.
func (s *Store) Preview(id, revisionID, directory string) (Revision, DiffResult, error) {
	_, revs, err := s.Get(id)
	if err != nil {
		return Revision{}, DiffResult{}, err
	}
	var target *Revision
	for i := range revs {
		if revs[i].ID == revisionID {
			target = &revs[i]
			break
		}
	}
	if target == nil {
		return Revision{}, DiffResult{}, ErrNotFound
	}
	diff := DiffResult{Add: []string{}, Update: []string{}, Remove: []string{}, Mode: target.Mode}
	current, err := captureProject(directory)
	if err == nil {
		currentLock, e := decodeLock(current.Lock)
		if e != nil {
			return Revision{}, DiffResult{}, e
		}
		targetLock, e := decodeLock([]byte(target.Lock))
		if e != nil {
			return Revision{}, DiffResult{}, e
		}
		diff = diffLocks(currentLock, targetLock, target.Mode)
	} else if errors.Is(err, os.ErrNotExist) {
		targetLock, e := decodeLock([]byte(target.Lock))
		if e != nil {
			return Revision{}, DiffResult{}, e
		}
		diff = diffLocks(lockfile.PackLock{}, targetLock, target.Mode)
	} else {
		return Revision{}, DiffResult{}, err
	}
	return *target, diff, nil
}
