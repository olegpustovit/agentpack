package state

import (
	"strings"
	"sync"
)

// Replicator owns the replication boundary of a store: change pages, cursors, validation hooks,
// quarantine, and the signal of which tables peer pages changed. internal/network only moves pages.
type Replicator struct {
	s *Store

	mu      sync.Mutex
	changed map[string]bool
	wake    chan struct{}
}

type validationHook struct {
	prefix string
	check  func(*Store, Change) (reject string, err error)
}

// hooks are registered from package init, so every store validates the tables of every linked package.
var (
	hooksMu sync.Mutex
	hooks   []validationHook
)

func (s *Store) Replicator() *Replicator { return s.replicator }

func newReplicator(s *Store) *Replicator {
	return &Replicator{s: s, changed: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// RegisterValidation runs check on every incoming change to a table starting with prefix. A
// non-empty reject quarantines the change; an error aborts the page so it is retried.
func RegisterValidation(prefix string, check func(*Store, Change) (reject string, err error)) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	hooks = append(hooks, validationHook{prefix, check})
}

func (r *Replicator) validate(change Change) (string, error) {
	hooksMu.Lock()
	registered := hooks
	hooksMu.Unlock()
	for _, hook := range registered {
		if strings.HasPrefix(change.Table, hook.prefix) {
			if reject, err := hook.check(r.s, change); reject != "" || err != nil {
				return reject, err
			}
		}
	}
	return "", nil
}

func (r *Replicator) signal(applied []Change) {
	if len(applied) == 0 {
		return
	}
	r.mu.Lock()
	for _, change := range applied {
		r.changed[change.Table] = true
	}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Changed wakes after peer pages changed tables; TakeChanged then returns them. Signals coalesce,
// so one consumer loop sees every table even when it falls behind.
func (r *Replicator) Changed() <-chan struct{} { return r.wake }

// TakeChanged returns the tables peer pages changed since the last call.
func (r *Replicator) TakeChanged() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := r.changed
	r.changed = map[string]bool{}
	return changed
}
