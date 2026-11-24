package registry

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
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
	if !validID(agentpackID) || name == "" || obs.CredentialShaped(name) {
		return nameRevision{}, fmt.Errorf("invalid agentpack name")
	}
	if obs.CredentialShaped(node) {
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

func nameNewer(a, b nameRevision) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	if a.Node != b.Node {
		return a.Node > b.Node
	}
	return a.ID > b.ID
}
