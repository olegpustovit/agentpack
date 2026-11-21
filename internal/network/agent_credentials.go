package network

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	Modified int64  `json:"modified"`       // the file's modification time, Unix ms, where it was published
	Node     string `json:"node,omitempty"` // the publisher, which alone renews a rotating sign-in
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
	node, err := g.auth.store.NodeID(ctx)
	if err != nil {
		_, event := obs.Start(ctx, "gateway", "agent_credentials")
		event.Fail(err)
		event.End()
		return
	}
	for _, agent := range agentLogins {
		if agent.credential == nil {
			continue
		}
		path := agent.credential()
		if path == "" {
			continue
		}
		installed, err := g.syncAgentCredential(ctx, agent, path, node)
		if err != nil && ctx.Err() == nil {
			_, event := obs.Start(ctx, "gateway", "agent_credentials")
			event.Set("harness", agent.harness)
			event.Fail(err)
			event.End()
		}
		if installed {
			g.agents.forget()
		}
	}
}

// syncAgentCredential publishes the local file when it is newer than the shared one, installs the
// shared one when that is newer, and renews the shared one ahead of the agent when this machine
// published it. It reports whether it replaced the local file.
func (g *Gateway) syncAgentCredential(ctx context.Context, agent agentLogin, path, node string) (bool, error) {
	key := agent.harness + "/" + filepath.Base(path)
	shared, err := g.sharedAgentCredential(ctx, key)
	if err != nil {
		return false, err
	}
	local, modified, err := readAgentCredential(path)
	if err != nil || modified < 0 {
		return false, err
	}
	switch {
	case local != nil && modified > shared.Modified && !bytes.Equal(local, shared.Data):
		return false, g.publishAgentCredential(ctx, key, sharedAgentCredential{Data: local, Modified: modified, Node: node})
	case shared.Data != nil && shared.Modified > modified && !bytes.Equal(local, shared.Data):
		return true, installAgentCredential(path, shared)
	case shared.Node == "" && local != nil && bytes.Equal(local, shared.Data):
		// A row published before rows named their publisher: a holder adopts it, hours before
		// any renewal is due, so the replicated choice settles on one machine first.
		shared.Node = node
		return false, g.publishAgentCredential(ctx, key, shared)
	case agent.refresh != nil && shared.Node == node && local != nil && bytes.Equal(local, shared.Data):
		renewed, err := agent.refresh(ctx, local)
		if renewed == nil || err != nil {
			return false, err
		}
		// The old refresh token is spent: keep the new one locally even if publishing fails, and
		// in the table even if the file write fails; the next round reconciles either.
		credential := sharedAgentCredential{Data: renewed, Modified: time.Now().UnixMilli(), Node: node}
		return true, errors.Join(g.publishAgentCredential(ctx, key, credential), installAgentCredential(path, credential))
	}
	return false, nil
}

func (g *Gateway) sharedAgentCredential(ctx context.Context, key string) (sharedAgentCredential, error) {
	var value string
	var shared sharedAgentCredential
	err := g.auth.store.Reader.QueryRowContext(ctx, "SELECT value FROM agent_credentials WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return shared, nil
	}
	if err != nil {
		return shared, err
	}
	return shared, json.Unmarshal([]byte(value), &shared)
}

func (g *Gateway) publishAgentCredential(ctx context.Context, key string, credential sharedAgentCredential) error {
	value, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	_, err = g.auth.store.DB.ExecContext(ctx, "INSERT INTO agent_credentials(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, string(value))
	return err
}

// stopSharingAgentCredential follows a sign-out with a newer empty row, so no machine republishes
// the sign-in it still holds.
func (g *Gateway) stopSharingAgentCredential(ctx context.Context, agent agentLogin) error {
	if agent.credential == nil || agent.credential() == "" {
		return nil
	}
	g.credentialsMu.Lock()
	defer g.credentialsMu.Unlock()
	return g.publishAgentCredential(ctx, agent.harness+"/"+filepath.Base(agent.credential()), sharedAgentCredential{Modified: time.Now().UnixMilli()})
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

// installAgentCredential replaces the file atomically and gives it the publisher's time, so this
// machine does not publish it back.
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
	modified := time.UnixMilli(credential.Modified)
	if err := os.Chtimes(temporary.Name(), modified, modified); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
