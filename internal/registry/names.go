package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const nameObjectType = "agentpack-name"

type nameRevision struct {
	ID         string    `json:"id"`
	ObjectType string    `json:"object_type"`
	Schema     int       `json:"schema"`
	Agentpack  string    `json:"agentpack_id"`
	Name       string    `json:"name"`
	Node       string    `json:"node"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Store) writeName(agentpackID, name, node string, at time.Time) (nameRevision, error) {
	name = strings.TrimSpace(name)
	if !validID(agentpackID) || name == "" || credentialShaped(name) {
		return nameRevision{}, fmt.Errorf("invalid agentpack name")
	}
	if credentialShaped(node) {
		node = "unknown"
	}
	if strings.TrimSpace(node) == "" {
		node, _ = os.Hostname()
	}
	if strings.TrimSpace(node) == "" {
		node = "unknown"
	}
	value := nameRevision{ObjectType: nameObjectType, Schema: objectSchema, Agentpack: agentpackID, Name: name, Node: node, CreatedAt: at.UTC()}
	id, err := s.writeObject(value)
	if err != nil {
		return nameRevision{}, err
	}
	value.ID = id
	return value, nil
}

func (s *Store) readName(id string) (nameRevision, error) {
	var value nameRevision
	if err := s.readObject(id, &value); err != nil {
		return value, err
	}
	if !validID(value.Agentpack) || value.ObjectType != nameObjectType || value.Schema != objectSchema || strings.TrimSpace(value.Name) == "" || credentialShaped(value.Name) || value.Node == "" || credentialShaped(value.Node) || value.CreatedAt.IsZero() {
		return value, fmt.Errorf("invalid agentpack name object")
	}
	path, _ := s.objectPath(id)
	data, err := readRegular(path)
	if err != nil || !canonicalObject(data, value) {
		return value, fmt.Errorf("agentpack name object is not canonical")
	}
	return value, nil
}

func nameNewer(a, b nameRevision) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	if a.Node != b.Node {
		return a.Node > b.Node
	}
	return a.ID > b.ID
}

func (s *Store) nameObjects() (map[string]nameRevision, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, objectsDir))
	if err != nil {
		return nil, err
	}
	latest := map[string]nameRevision{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		var shape map[string]json.RawMessage
		data, err := readRegular(filepath.Join(s.root, objectsDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &shape); err != nil {
			return nil, fmt.Errorf("invalid registry object JSON")
		}
		if _, ok := shape["object_type"]; !ok {
			continue
		}
		name, err := s.readName(id)
		if err != nil {
			return nil, err
		}
		if current, ok := latest[name.Agentpack]; !ok || nameNewer(name, current) {
			latest[name.Agentpack] = name
		}
	}
	return latest, nil
}

func hasOwnerRevision(revisions map[string]Revision, owner string) bool {
	for _, revision := range revisions {
		if revision.AgentpackID == owner {
			return true
		}
	}
	return false
}
