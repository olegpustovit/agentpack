package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
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

// signInRevision names one published sign-in; a newer one starts without the old one's failure.
type signInRevision struct {
	key string
	rev int64
}

func agentCredentialKey(agent registry.Entry, path string) string {
	return string(agent.Target) + "/" + filepath.Base(path)
}

// runAgentCredentials shares agent sign-ins across the mesh: an agent signed in on any machine is
// signed in on all of them.
func (g *Gateway) runAgentCredentials(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		g.noticeWake()
		g.syncAgentCredentials(ctx, "")
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// syncAgentCredentials runs one round for every agent. signedIn names the agent just signed in
// through the web UI, the only way to resume sharing after a web UI sign-out.
func (g *Gateway) syncAgentCredentials(ctx context.Context, signedIn string) {
	g.credentialsMu.Lock()
	defer g.credentialsMu.Unlock()
	node, err := g.store.NodeID(ctx)
	if err != nil {
		_, event := obs.Start(ctx, "gateway", "agent_credentials")
		event.Fail(err)
		event.End()
		return
	}
	for _, agent := range network.Agents {
		if agent.Login == nil || agent.Login.Credential == nil {
			continue
		}
		path := agent.Login.Credential()
		if path == "" || string(agent.Target) != signedIn && paths.RequiresUserAccess(path) {
			continue
		}
		installed, err := g.syncAgentCredential(ctx, agent, path, node, string(agent.Target) == signedIn)
		if err != nil && ctx.Err() == nil {
			_, event := obs.Start(ctx, "gateway", "agent_credentials")
			event.Set("harness", string(agent.Target))
			event.Fail(err)
			event.End()
		}
		if installed {
			g.agents.forget()
			// A sign-in made on another machine arrived: the chats here that waited for it continue.
			obs.Go("gateway.reload_signed_out_chats", func() { g.reloadSignedOutChats(context.WithoutCancel(ctx), string(agent.Target)) })
		}
	}
}

// syncAgentCredential publishes the local file when it changed since this machine last installed or
// published it, installs the shared one when its rev is higher and the local file is unchanged, and
// renews the shared one ahead of the agent when this machine published it. After a web UI sign-out
// nothing is published until signedIn. It reports whether it replaced the local file.
func (g *Gateway) syncAgentCredential(ctx context.Context, agent registry.Entry, path, node string, signedIn bool) (bool, error) {
	key := agentCredentialKey(agent, path)
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
	localUsable, sharedUsable := local != nil, shared.Data != nil
	if valid := agent.Login.ValidCredential; valid != nil {
		localUsable = localUsable && valid(local)
		sharedUsable = sharedUsable && valid(shared.Data)
	}
	localDigest, sharedDigest := sha256.Sum256(local), sha256.Sum256(shared.Data)
	// The row moved since this machine last touched it: a higher rev, or a concurrent publish of the same rev that won.
	localChanged, sharedNewer := !bytes.Equal(localDigest[:], record.Hash), shared.Rev != record.Rev || !bytes.Equal(sharedDigest[:], record.Hash)
	signedOut := shared.Data == nil && shared.Rev > 0
	if !known {
		// First sight of this key here: a shared sign-in wins (file times from different clocks cannot
		// order the copies), and the local file is published only when nothing is shared.
		localChanged, sharedNewer = !sharedUsable && !signedOut, sharedUsable
	}
	localChanged = localChanged || signedIn
	changed := localChanged // for .bak
	// A changed file whose token is older than the shared one is stale (restored, or left from before
	// a sleep): install the shared one instead. Token age never promotes a file.
	if age := agent.Login.Freshness; age != nil && localChanged && !signedIn && !signedOut && localUsable && sharedUsable {
		if mine, theirs := age(local), age(shared.Data); !mine.IsZero() && !theirs.IsZero() && mine.Before(theirs) {
			localChanged, sharedNewer = false, true
		}
	}
	same := bytes.Equal(local, shared.Data)
	switch {
	case localUsable && localChanged && !same && (!signedOut || signedIn):
		return false, g.publishAgentCredential(ctx, key, sharedAgentCredential{Data: local, Rev: shared.Rev + 1, Modified: time.Now().UnixMilli(), Node: node})
	case sharedUsable && (!localUsable || sharedNewer && !localChanged) && !same:
		if (!known || changed) && local != nil {
			if err := os.WriteFile(path+".bak", local, 0o600); err != nil {
				return false, err
			}
		}
		if installed, err := installAgentCredential(path, local, shared); !installed || err != nil {
			return false, err
		}
		return true, g.recordAgentCredential(ctx, key, shared)
	case shared.Node == "" && localUsable && same:
		// A row published before rows named their publisher: a holder adopts it, hours before
		// any renewal is due, so the replicated choice settles on one machine first.
		shared.Node = node
		return false, g.publishAgentCredential(ctx, key, shared)
	case localUsable && same && (!known || localChanged || sharedNewer):
		return false, g.recordAgentCredential(ctx, key, shared)
	case agent.Login.Refresh != nil && shared.Node == node && localUsable && same && g.caughtUp():
		// Once sent, the server may rotate the token whatever happens here: a shutdown must not drop the answer.
		ctx := context.WithoutCancel(ctx)
		renewed, err := agent.Login.Refresh(ctx, local)
		// shortcut: only the renewing gateway reports it; elsewhere expires_at shows the expiry
		if err != nil {
			g.refreshFailed.LoadOrStore(signInRevision{key, shared.Rev}, time.Now())
		} else {
			g.refreshFailed.Delete(signInRevision{key, shared.Rev})
		}
		if renewed == nil || err != nil {
			return false, err
		}
		// The old refresh token is spent: keep the new one locally even if publishing fails, and
		// in the table even if the file write fails; the next round reconciles either. The file goes
		// first, so a crash between the two never publishes the spent token as a new sign-in.
		credential := sharedAgentCredential{Data: renewed, Rev: shared.Rev + 1, Modified: time.Now().UnixMilli(), Node: node}
		installed, installErr := installAgentCredential(path, local, credential)
		publishErr := g.publishAgentCredential(ctx, key, credential)
		if installErr != nil {
			// The spent file stays recorded as unchanged, so the next round installs the renewal instead of publishing it.
			return false, errors.Join(publishErr, installErr, g.recordAgentCredential(ctx, key, sharedAgentCredential{Data: local, Rev: credential.Rev}))
		}
		return installed, publishErr
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
// the sign-in it still holds, or a later rotation of it, until a web UI sign-in.
func (g *Gateway) stopSharingAgentCredential(ctx context.Context, agent registry.Entry) error {
	if agent.Login.Credential == nil || agent.Login.Credential() == "" {
		return nil
	}
	g.credentialsMu.Lock()
	defer g.credentialsMu.Unlock()
	key := agentCredentialKey(agent, agent.Login.Credential())
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

// installAgentCredential replaces the file atomically unless the agent rewrote it since this round
// read expected: its newer token wins, and the next round shares it.
func installAgentCredential(path string, expected []byte, credential sharedAgentCredential) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	return true, paths.WriteAtomic(path, credential.Data, 0o600)
}
