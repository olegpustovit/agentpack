package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Machines edit an agentpack independently, so replication can leave it with several heads. Two heads
// merge three-way against their common ancestor. A clean merge is a pure function of its parents, so
// every machine that performs it writes the identical object and they converge without coordinating.
// When both sides changed one value differently, nothing is guessed: the heads stay, the conflict is
// reported, and a person's Resolve writes the merge.
const (
	mergedLabel   = "Merged automatically"
	resolvedLabel = "Resolved conflict"
	snapshotPath  = "snapshot:"
)

// ConflictField is one value both heads changed differently. Values maps a head ID to its display text.
type ConflictField struct {
	Path   string            `json:"path"`
	Name   string            `json:"name"`
	Base   string            `json:"base"`
	Values map[string]string `json:"values"`
}

type Conflict struct {
	Heads  []string        `json:"heads"`
	Fields []ConflictField `json:"fields"`
}

type mergeDocument struct {
	manifest, lock map[string]any
	mode           string
}

func loadMergeDocument(revision Revision) (mergeDocument, error) {
	document := mergeDocument{mode: revision.Mode}
	if err := toml.Unmarshal([]byte(revision.Manifest), &document.manifest); err != nil {
		return document, err
	}
	if err := toml.Unmarshal([]byte(revision.Lock), &document.lock); err != nil {
		return document, err
	}
	for table, key := range lockKeys {
		if list, ok := document.lock[table].([]any); ok {
			keyed := make(map[string]any, len(list))
			for _, item := range list {
				entry, _ := item.(map[string]any)
				name, _ := entry[key].(string)
				if name == "" {
					return document, fmt.Errorf("lock %s entry has no %s", table, key)
				}
				keyed[name] = item
			}
			document.lock[table] = keyed
		}
	}
	return document, nil
}

// Lock lists are sets keyed by one field, so entries merge independently of their order.
var lockKeys = map[string]string{"packages": "module", "mcp_servers": "name"}

func encodeLock(lock map[string]any) map[string]any {
	out := make(map[string]any, len(lock))
	for name, value := range lock {
		out[name] = value
		if _, listed := lockKeys[name]; !listed {
			continue
		}
		keyed, ok := value.(map[string]any)
		if !ok {
			continue
		}
		keys := make([]string, 0, len(keyed))
		for key := range keyed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		list := make([]any, 0, len(keys))
		for _, key := range keys {
			list = append(list, keyed[key])
		}
		out[name] = list
	}
	return out
}

type merger struct {
	a, b    string
	choices map[string]string
	fields  []ConflictField
}

// Top-level tables merge by entry. An entry is atomic: mixing a dependency's branch from one side with its
// tag from the other would pin something neither side asked for.
func mergesByEntry(path []string) bool {
	return len(path) <= 1 || len(path) == 2 && path[0] == "mcp" && path[1] == "servers"
}

// nil means absent, so a removal merges like any other change.
func (m *merger) value(document string, path []string, base, a, b any) any {
	if reflect.DeepEqual(a, b) {
		return a
	}
	if reflect.DeepEqual(base, a) {
		return b
	}
	if reflect.DeepEqual(base, b) {
		return a
	}
	aMap, aOK := a.(map[string]any)
	bMap, bOK := b.(map[string]any)
	baseMap, baseOK := base.(map[string]any)
	// A table one side no longer has is an empty one: its other entries still merge.
	if (aOK || a == nil) && (bOK || b == nil) && (baseOK || base == nil) && mergesByEntry(path) {
		keys := map[string]bool{}
		for _, side := range []map[string]any{baseMap, aMap, bMap} {
			for key := range side {
				keys[key] = true
			}
		}
		out := make(map[string]any, len(keys))
		for key := range keys {
			if value := m.value(document, append(slices.Clip(path), key), baseMap[key], aMap[key], bMap[key]); value != nil {
				out[key] = value
			}
		}
		if len(out) == 0 && (a == nil || b == nil) {
			return nil
		}
		return out
	}
	id := fieldPath(document, path)
	switch m.choices[id] {
	case m.a:
		return a
	case m.b:
		return b
	}
	m.fields = append(m.fields, ConflictField{
		Path:   id,
		Name:   fieldName(document, path),
		Base:   displayValue(base),
		Values: map[string]string{m.a: displayValue(a), m.b: displayValue(b)},
	})
	return nil
}

func fieldPath(document string, path []string) string {
	parts := make([]string, len(path))
	for i, part := range path {
		parts[i] = escapePointer(part)
	}
	return document + ":" + strings.Join(parts, "/")
}

func fieldName(document string, path []string) string {
	if document == "mode" {
		return "Mode"
	}
	return path[len(path)-1]
}

func displayValue(value any) string {
	switch current := value.(type) {
	case nil:
		return "Removed"
	case string:
		return current
	case map[string]any:
		if commit, ok := current["commit"].(string); ok {
			return commit[:min(len(commit), 7)]
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "Changed"
	}
	return string(data)
}

func (s *Store) ancestors(id string) map[string]bool {
	seen := map[string]bool{}
	pending := []string{id}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[current] {
			continue
		}
		seen[current] = true
		var revision Revision
		// Collection may have removed an old parent; the history above it is then unknown.
		if s.readObject(current, &revision) == nil {
			pending = append(pending, revision.ParentIDs...)
		}
	}
	return seen
}

// commonAncestor picks the newest shared revision, breaking ties by ID so that every machine picks the
// same base. Nil means the heads share no history, and everything counts as added by both.
func (s *Store) commonAncestor(a, b string) *Revision {
	theirs := s.ancestors(b)
	var best *Revision
	for id := range s.ancestors(a) {
		if !theirs[id] {
			continue
		}
		revision, err := s.readRevision(id)
		if err != nil {
			continue
		}
		if best == nil || revision.CreatedAt.After(best.CreatedAt) || revision.CreatedAt.Equal(best.CreatedAt) && revision.ID < best.ID {
			copied := revision
			best = &copied
		}
	}
	return best
}

// mergeRevisions returns the merge of two heads, or the fields a person must decide. a.ID must sort first.
func (s *Store) mergeRevisions(a, b Revision, choices map[string]string) (Revision, []ConflictField, error) {
	merged := Revision{Schema: objectSchema, AgentpackID: a.AgentpackID, ParentIDs: []string{a.ID, b.ID}, SourceNode: a.SourceNode, Project: a.Project, Label: mergedLabel, CreatedAt: a.CreatedAt}
	if b.CreatedAt.After(a.CreatedAt) {
		merged.CreatedAt = b.CreatedAt
	}
	if sameTOML([]byte(a.Manifest), []byte(b.Manifest)) && sameTOML([]byte(a.Lock), []byte(b.Lock)) && a.Mode == b.Mode {
		// Reuses the bytes as stored, so machines on different go-toml releases still write one object.
		merged.Manifest, merged.Lock, merged.Mode, merged.NeedsLocalConfiguration = a.Manifest, a.Lock, a.Mode, a.NeedsLocalConfiguration
		return s.withMergedPackages(merged, a, b)
	}
	if chosen := choices[snapshotPath]; chosen == a.ID || chosen == b.ID {
		source := a
		if chosen == b.ID {
			source = b
		}
		merged.Manifest, merged.Lock, merged.Mode, merged.NeedsLocalConfiguration = source.Manifest, source.Lock, source.Mode, source.NeedsLocalConfiguration
		return s.withMergedPackages(merged, a, b)
	}
	base := mergeDocument{}
	if ancestor := s.commonAncestor(a.ID, b.ID); ancestor != nil {
		var err error
		if base, err = loadMergeDocument(*ancestor); err != nil {
			return merged, nil, err
		}
	}
	left, err := loadMergeDocument(a)
	if err != nil {
		return merged, nil, err
	}
	right, err := loadMergeDocument(b)
	if err != nil {
		return merged, nil, err
	}
	m := &merger{a: a.ID, b: b.ID, choices: choices}
	manifest, _ := m.value("manifest", nil, anyMap(base.manifest), anyMap(left.manifest), anyMap(right.manifest)).(map[string]any)
	lock, _ := m.value("lock", nil, anyMap(base.lock), anyMap(left.lock), anyMap(right.lock)).(map[string]any)
	mode, _ := m.value("mode", []string{"mode"}, base.mode, left.mode, right.mode).(string)
	if len(m.fields) > 0 {
		sort.Slice(m.fields, func(i, j int) bool { return m.fields[i].Path < m.fields[j].Path })
		return merged, m.fields, nil
	}
	manifestData, err := toml.Marshal(manifest)
	if err != nil {
		return merged, nil, err
	}
	lockData, err := toml.Marshal(encodeLock(lock))
	if err != nil {
		return merged, nil, err
	}
	cleanManifest, manifestSecrets, err := normalizeTOML(manifestData)
	if err != nil {
		return merged, nil, err
	}
	cleanLock, lockSecrets, err := normalizeTOML(lockData)
	if err != nil {
		return merged, nil, err
	}
	if _, err := validateSnapshot(cleanManifest, cleanLock); err != nil {
		// Each value merged, but the result is not a usable project. A person picks a whole side.
		return merged, []ConflictField{{Path: snapshotPath, Name: "Whole project", Values: map[string]string{a.ID: displayValue(a.Project), b.ID: displayValue(b.Project)}}}, nil
	}
	merged.Manifest, merged.Lock, merged.Mode = string(cleanManifest), string(cleanLock), mode
	merged.NeedsLocalConfiguration = len(manifestSecrets)+len(lockSecrets) > 0
	return s.withMergedPackages(merged, a, b)
}

func anyMap(value map[string]any) any {
	if value == nil {
		return nil
	}
	return value
}

// One package can have several records, one per machine that saved it. The smallest ID keeps the merge deterministic.
func (s *Store) withMergedPackages(merged, a, b Revision) (Revision, []ConflictField, error) {
	lock, err := decodeLock([]byte(merged.Lock))
	if err != nil {
		return merged, nil, err
	}
	wanted := map[string]bool{}
	for _, entry := range lock.Packages {
		wanted[entry.Module+"@"+entry.Commit] = true
	}
	records := map[string]string{}
	for _, id := range append(slices.Clone(a.PackageIDs), b.PackageIDs...) {
		record, err := s.readPackage(id)
		if err != nil {
			return merged, nil, err
		}
		key := record.Module + "@" + record.Commit
		if current, ok := records[key]; wanted[key] && (!ok || id < current) {
			records[key] = id
		}
	}
	merged.PackageIDs = make([]string, 0, len(records))
	for _, id := range records {
		merged.PackageIDs = append(merged.PackageIDs, id)
	}
	sort.Strings(merged.PackageIDs)
	return merged, nil, nil
}

// commitMerge publishes a merge and carries over local secrets that still apply to it.
func (s *Store) commitMerge(merged, a, b Revision) error {
	id, err := s.writeObject(merged)
	if err != nil {
		return err
	}
	if err := s.mergeBindings(id, merged, a.ID, b.ID); err != nil {
		return err
	}
	idx, err := s.rebuildIndex()
	if err != nil {
		return err
	}
	for _, pack := range idx.Agentpacks {
		if err := s.writeMetadata(pack); err != nil {
			return err
		}
	}
	return s.writeIndexUnlocked(idx)
}

func (s *Store) mergeBindings(id string, merged Revision, parents ...string) error {
	values := map[string]string{}
	for _, parent := range parents {
		data, exists, _, err := readRegularOptional(s.bindingPath(parent))
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		var parentBindings bindings
		if err := json.Unmarshal(data, &parentBindings); err != nil {
			return fmt.Errorf("parse local registry bindings")
		}
		for path, value := range parentBindings.Values {
			document, pointer := merged.Manifest, strings.TrimPrefix(path, "manifest:")
			if strings.HasPrefix(path, "lock:") {
				document, pointer = merged.Lock, strings.TrimPrefix(path, "lock:")
			}
			var parsed any
			if toml.Unmarshal([]byte(document), &parsed) == nil && setPath(parsed, pointer, value) == nil {
				values[path] = value
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	return s.writeBindings(id, bindings{Values: values})
}

func (s *Store) bindingPath(id string) string {
	return filepath.Join(s.root, bindingsDir, id+".json")
}

func (s *Store) headPair(heads []string, i, j int) (Revision, Revision, error) {
	a, err := s.readRevision(heads[i])
	if err != nil {
		return a, Revision{}, err
	}
	b, err := s.readRevision(heads[j])
	return a, b, err
}

// mergeHeadsUnlocked merges every pair of heads that merges cleanly. The caller holds the registry lock.
func (s *Store) mergeHeadsUnlocked(id string) error {
	for {
		idx, err := s.readIndexUnlocked()
		if err != nil {
			return err
		}
		heads := sorted(idx.Agentpacks[id].Heads)
		progressed := false
	pairs:
		for i := range heads {
			for j := i + 1; j < len(heads); j++ {
				a, b, err := s.headPair(heads, i, j)
				if err != nil {
					return err
				}
				merged, fields, err := s.mergeRevisions(a, b, nil)
				if err != nil {
					return err
				}
				if len(fields) > 0 {
					continue
				}
				if err := s.commitMerge(merged, a, b); err != nil {
					return err
				}
				progressed = true
				break pairs
			}
		}
		if !progressed {
			return nil
		}
	}
}

// MergeHeads settles every agentpack that replication left with several heads, as far as it can without a person.
func (s *Store) MergeHeads() error {
	return s.withLock(func() error {
		idx, err := s.readIndexUnlocked()
		if err != nil {
			return err
		}
		var failures error
		for id, pack := range idx.Agentpacks {
			if len(pack.Heads) > 1 {
				failures = errors.Join(failures, s.mergeHeadsUnlocked(id))
			}
		}
		return failures
	})
}

// Conflict reports the first pair of heads that needs a person, or empty when there is none.
func (s *Store) Conflict(id string) (Conflict, error) {
	idx, err := s.readIndex()
	if err != nil {
		return Conflict{}, err
	}
	pack, ok := idx.Agentpacks[id]
	if !ok {
		return Conflict{}, ErrNotFound
	}
	heads := sorted(pack.Heads)
	for i := range heads {
		for j := i + 1; j < len(heads); j++ {
			a, b, err := s.headPair(heads, i, j)
			if err != nil {
				return Conflict{}, err
			}
			_, fields, err := s.mergeRevisions(a, b, nil)
			if err != nil {
				return Conflict{}, err
			}
			if len(fields) > 0 {
				return Conflict{Heads: []string{a.ID, b.ID}, Fields: fields}, nil
			}
		}
	}
	return Conflict{Heads: []string{}, Fields: []ConflictField{}}, nil
}

// Resolve merges two heads using a person's choice for every conflicting field: path to the head that wins.
func (s *Store) Resolve(id string, heads []string, choices map[string]string, node string) (Revision, error) {
	var merged Revision
	err := s.withLock(func() error {
		idx, err := s.readIndexUnlocked()
		if err != nil {
			return err
		}
		pack, ok := idx.Agentpacks[id]
		if !ok {
			return ErrNotFound
		}
		ordered := sorted(heads)
		if len(ordered) != 2 || !slices.Contains(pack.Heads, ordered[0]) || !slices.Contains(pack.Heads, ordered[1]) {
			return fmt.Errorf("these versions were already merged; review the conflict again")
		}
		a, b, err := s.headPair(ordered, 0, 1)
		if err != nil {
			return err
		}
		var fields []ConflictField
		merged, fields, err = s.mergeRevisions(a, b, choices)
		if err != nil {
			return err
		}
		if len(fields) > 0 {
			return fmt.Errorf("choose a version for %s", fields[0].Name)
		}
		merged.Label, merged.CreatedAt, merged.SourceNode = resolvedLabel, time.Now().UTC(), redactMetadata(node)
		if err := s.commitMerge(merged, a, b); err != nil {
			return err
		}
		return s.mergeHeadsUnlocked(id)
	})
	return merged, err
}

// captureParent picks which head a new snapshot continues while a conflict is open: this project's own
// line of work, so the other side stays visible until someone resolves it.
func (s *Store) captureParent(heads []string, node, project string) (string, error) {
	var newest, own Revision
	for _, id := range heads {
		head, err := s.readRevision(id)
		if err != nil {
			return "", err
		}
		if newest.ID == "" || head.CreatedAt.After(newest.CreatedAt) {
			newest = head
		}
		if head.SourceNode == node && head.Project == project && (own.ID == "" || head.CreatedAt.After(own.CreatedAt)) {
			own = head
		}
	}
	if own.ID != "" {
		return own.ID, nil
	}
	return newest.ID, nil
}
