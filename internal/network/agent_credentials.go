package network

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"os"
	"path/filepath"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
)

// Sign-in files are small; anything larger is not one.
const agentCredentialLimit = 1 << 20

// sharedAgentCredential is one agent_credentials row. Empty Data is a sign-out made in the web UI:
// it stops sharing that sign-in without deleting other machines' files.
type sharedAgentCredential struct {
	Data     []byte `json:"data,omitempty"`
	Rev      int64  `json:"rev,omitempty"`  // raised by every publish; the higher rev wins, never a clock
	Modified int64  `json:"modified"`       // publish time, Unix ms; orders rows only before a machine has a rev record
	Node     string `json:"node,omitempty"` // the publisher, which alone renews a rotating sign-in
}

// installedCredential is what this machine last installed or published for a key (local, not replicated).
type installedCredential struct {
	Hash []byte
	Rev  int64
}

// runAgentCredentials shares agent sign-ins across the mesh: an agent signed in on any machine is
// signed in on all of them.
func (g *Gateway) runAgentCredentials(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		g.syncAgentCredentials(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (g *Gateway) syncAgentCredentials(ctx context.Context) {
	g.credentialsMu.Lock()
	defer g.credentialsMu.Unlock()
	node, err := g.store.NodeID(ctx)
	if err != nil {
		_, event := obs.Start(ctx, "gateway", "agent_credentials")
		event.Fail(err)
		event.End()
		return
	}
	for _, agent := range agents {
		if agent.Login == nil || agent.Login.Credential == nil {
			continue
		}
		path := agent.Login.Credential()
		if path == "" {
			continue
		}
		installed, err := g.syncAgentCredential(ctx, agent, path, node)
		if err != nil && ctx.Err() == nil {
			_, event := obs.Start(ctx, "gateway", "agent_credentials")
			event.Set("harness", string(agent.Target))
			event.Fail(err)
			event.End()
		}
		if installed {
			g.agents.forget()
		}
	}
}

// syncAgentCredential publishes the local file when it changed since this machine last installed or
// published it, installs the shared one when its rev is higher and the local file is unchanged, and
// renews the shared one ahead of the agent when this machine published it. It reports whether it
// replaced the local file.
func (g *Gateway) syncAgentCredential(ctx context.Context, agent registry.Entry, path, node string) (bool, error) {
	key := string(agent.Target) + "/" + filepath.Base(path)
	shared, err := g.sharedAgentCredential(ctx, key)
	if err != nil {
		return false, err
	}
	local, modified, err := readAgentCredential(path)
	if err != nil || modified < 0 {
		return false, err
	}
	record, known, err := g.installedAgentCredential(ctx, key)
	if err != nil {
		return false, err
	}
	localDigest, sharedDigest := sha256.Sum256(local), sha256.Sum256(shared.Data)
	// The row moved since this machine last touched it: a higher rev, or a concurrent publish of the same rev that won.
	localChanged, sharedNewer := !bytes.Equal(localDigest[:], record.Hash), shared.Rev != record.Rev || !bytes.Equal(sharedDigest[:], record.Hash)
	if !known {
		// First sight of this key here: only file times can order the two copies, once.
		localChanged, sharedNewer = modified > shared.Modified, shared.Modified > modified
	}
	same := bytes.Equal(local, shared.Data)
	switch {
	case local != nil && localChanged && !same:
		return false, g.publishAgentCredential(ctx, key, sharedAgentCredential{Data: local, Rev: shared.Rev + 1, Modified: time.Now().UnixMilli(), Node: node})
	case shared.Data != nil && (local == nil || sharedNewer && !localChanged) && !same:
		if err := installAgentCredential(path, shared); err != nil {
			return false, err
		}
		return true, g.recordAgentCredential(ctx, key, shared)
	case shared.Node == "" && local != nil && same:
		// A row published before rows named their publisher: a holder adopts it, hours before
		// any renewal is due, so the replicated choice settles on one machine first.
		shared.Node = node
		return false, g.publishAgentCredential(ctx, key, shared)
	case local != nil && same && (!known || localChanged || sharedNewer):
		return false, g.recordAgentCredential(ctx, key, shared)
	case agent.Login.Refresh != nil && shared.Node == node && local != nil && same:
		renewed, err := agent.Login.Refresh(ctx, local)
		if renewed == nil || err != nil {
			return false, err
		}
		// The old refresh token is spent: keep the new one locally even if publishing fails, and
		// in the table even if the file write fails; the next round reconciles either.
		credential := sharedAgentCredential{Data: renewed, Rev: shared.Rev + 1, Modified: time.Now().UnixMilli(), Node: node}
		publishErr := g.publishAgentCredential(ctx, key, credential)
		if err := installAgentCredential(path, credential); err != nil {
			// The spent file stays recorded as unchanged, so the next round installs the renewal instead of publishing it.
			return false, errors.Join(publishErr, err, g.recordAgentCredential(ctx, key, sharedAgentCredential{Data: local, Rev: credential.Rev}))
		}
		return true, publishErr
	}
	return false, nil
}

func (g *Gateway) installedAgentCredential(ctx context.Context, key string) (installedCredential, bool, error) {
	var record installedCredential
	var ok bool
	var err error
	record.Hash, record.Rev, ok, err = g.store.Credentials().AgentInstall(ctx, key)
	return record, ok, err
}

func (g *Gateway) recordAgentCredential(ctx context.Context, key string, credential sharedAgentCredential) error {
	digest := sha256.Sum256(credential.Data)
	return g.store.Credentials().RecordAgentInstall(ctx, key, digest[:], credential.Rev)
}

func (g *Gateway) sharedAgentCredential(ctx context.Context, key string) (sharedAgentCredential, error) {
	var shared sharedAgentCredential
	value, err := g.store.Credentials().Agent(ctx, key)
	if err != nil || value == nil {
		return shared, err
	}
	return shared, json.Unmarshal(value, &shared)
}

func (g *Gateway) publishAgentCredential(ctx context.Context, key string, credential sharedAgentCredential) error {
	value, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	if err = g.store.Credentials().PublishAgent(ctx, key, value); err != nil || credential.Data == nil {
		// A sign-out keeps the record of the file this machine holds, so that file is not republished.
		return err
	}
	return g.recordAgentCredential(ctx, key, credential)
}

// stopSharingAgentCredential follows a sign-out with a newer empty row, so no machine republishes
// the sign-in it still holds.
func (g *Gateway) stopSharingAgentCredential(ctx context.Context, agent registry.Entry) error {
	if agent.Login.Credential == nil || agent.Login.Credential() == "" {
		return nil
	}
	g.credentialsMu.Lock()
	defer g.credentialsMu.Unlock()
	key := string(agent.Target) + "/" + filepath.Base(agent.Login.Credential())
	shared, err := g.sharedAgentCredential(ctx, key)
	if err != nil {
		return err
	}
	return g.publishAgentCredential(ctx, key, sharedAgentCredential{Rev: shared.Rev + 1, Modified: time.Now().UnixMilli()})
}

// readAgentCredential returns nil for a missing file, and a negative time for a file to leave alone
// this round: not a sign-in file, or (every sign-in file being JSON) one the agent is still writing.
func readAgentCredential(path string) ([]byte, int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, -1, err
	}
	if !info.Mode().IsRegular() || info.Size() > agentCredentialLimit {
		return nil, -1, nil
	}
	data, err := os.ReadFile(path)
	if err != nil || !json.Valid(data) {
		return nil, -1, err
	}
	return data, info.ModTime().UnixMilli(), nil
}

// installAgentCredential replaces the file atomically.
func installAgentCredential(path string, credential sharedAgentCredential) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".agentpack-credential-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(credential.Data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
