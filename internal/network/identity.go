package network

import (
	"context"
	"database/sql"
	"errors"
	"github.com/olegpustovit/agentpack/internal/session"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type tailnetSelf struct{ ID, User, Name string }

func (self tailnetSelf) displayName() string {
	if self.Name != "" {
		return self.Name
	}
	return session.LocalHostname()
}

// selfIdentity returns this node's Tailscale identity, or zero values while Tailscale is down.
// shortcut: one cached lookup per gateway, not per request, so a Tailscale change is visible within identityCacheTTL.
func (g *Gateway) selfIdentity(ctx context.Context) tailnetSelf {
	g.identityMu.Lock()
	defer g.identityMu.Unlock()
	if !g.identityAt.IsZero() && time.Since(g.identityAt) < identityCacheTTL {
		return g.identitySelf
	}
	var self tailnetSelf
	if status, err := tailnetSnapshot(ctx); err == nil {
		self.ID = status.Self.ID
		self.Name = status.Self.HostName
		self.User = status.User[strconv.FormatInt(status.Self.UserID, 10)].LoginName
	}
	if ctx.Err() == nil {
		g.identitySelf, g.identityAt = self, time.Now()
	}
	return self
}

type identityNode struct {
	Main    bool   `json:"main"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	OS      string `json:"os"`
	Version string `json:"version"`
	Schema  int    `json:"schema_version"`
}

type identityResponse struct {
	Node          identityNode `json:"node"`
	Auth          string       `json:"auth"`
	TailscaleUser string       `json:"tailscale_user"`
}

func (g *Gateway) identity(w http.ResponseWriter, r *http.Request) {
	self := g.selfIdentity(r.Context())
	if self.ID == "" {
		self.ID, _ = g.store.NodeID(r.Context())
	}
	roles, err := g.store.Main(r.Context())
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	writeJSON(w, identityResponse{
		Node:          identityNode{Main: roles.MainNode != "" && roles.MainNode == self.ID, ID: self.ID, Name: self.displayName(), OS: runtime.GOOS, Version: g.options.Version, Schema: replicationVersion},
		Auth:          g.auth.email(r),
		TailscaleUser: self.User,
	})
}

// An account assertion has authority only after both machines explicitly approved mesh membership.
func (g *Gateway) forwardedAccount(ctx context.Context, actor session.Actor, email string) (session.Actor, error) {
	if email == "" || !g.peer(actor.StableNodeID).Mesh {
		return actor, nil
	}
	normalized, err := session.NormalizeEmail(email)
	if err != nil {
		return session.Actor{}, errors.New("invalid forwarded account")
	}
	var enabled bool
	if err := g.store.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE email=? AND disabled=0)", normalized).Scan(&enabled); err != nil {
		return session.Actor{}, err
	}
	if !enabled {
		return session.Actor{}, errors.New("forwarded account is unavailable")
	}
	actor.User = normalized
	actor.Account = true
	return actor, nil
}

var errLocalAccountUnavailable = errors.New("local gateway account is unavailable; use gateway whoami to bind an enabled account")

// Local transports keep OS authority but attribute account-owned work to the bound account or owner.
func (g *Gateway) localActor(ctx context.Context) (session.Actor, error) {
	actor := session.LocalActor("local")
	var bound string
	if err := g.store.Reader.QueryRowContext(ctx, "SELECT account_email FROM local_node WHERE id=1").Scan(&bound); err != nil {
		return session.Actor{}, err
	}
	var email string
	query := "SELECT email FROM users WHERE disabled=0 AND role='owner' ORDER BY created_at,email LIMIT 1"
	var args []any
	if bound != "" {
		query = "SELECT email FROM users WHERE disabled=0 AND email=?"
		args = append(args, bound)
	}
	err := g.store.Reader.QueryRowContext(ctx, query, args...).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) && bound == "" {
		var hasAccounts bool
		if err := g.store.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users)").Scan(&hasAccounts); err != nil {
			return session.Actor{}, err
		}
		if !hasAccounts {
			return actor, nil
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return session.Actor{}, errLocalAccountUnavailable
	}
	if err != nil {
		return session.Actor{}, err
	}
	actor.NativeUser = actor.User
	actor.User = email
	actor.Account = true
	return actor, nil
}

func (g *Gateway) nodeID(ctx context.Context) (string, error) {
	if id, ok := g.nodeIDCache.Load().(string); ok {
		return id, nil
	}
	return g.store.NodeID(ctx)
}

// bindTailnetNode publishes the stable identity after the one-time store migration.
func (g *Gateway) bindTailnetNode(ctx context.Context, stable string) error {
	id, err := g.nodeID(ctx)
	if err != nil || id == stable {
		return err
	}
	if !strings.HasPrefix(id, "site:") {
		return errors.New("state belongs to a different stable node identity")
	}
	if err := g.store.BindNode(ctx, stable); err != nil {
		return err
	}
	g.nodeIDCache.Store(stable)
	return nil
}

// identityCacheTTL bounds how often identity asks Tailscale for status.
const identityCacheTTL = 60 * time.Second
