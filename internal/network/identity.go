package network

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
)

// Actor is who started or called an operation. It comes from the transport:
// the local OS user, the gateway's browser account, or a verified tailnet whois.
// Caller-provided identities have no authority; forwarded accounts require verified mesh membership.
type Actor struct {
	nativeUser   string
	Kind         string `json:"kind"`
	Account      bool   `json:"account,omitempty"`
	User         string `json:"user,omitempty"`
	Node         string `json:"node,omitempty"`
	StableNodeID string `json:"stable_node_id,omitempty"` // Verified tailnet identity; Node is the display name.
	// Agent is the harness of the chat that acted for this user through agentpack tools.
	Agent string `json:"agent,omitempty"`
	chat  string // the calling chat's session ID; never serialized
}

// identityCacheTTL bounds how often identity asks Tailscale for status.
const identityCacheTTL = 60 * time.Second

type actorKey struct{}

func withActor(ctx context.Context, actor Actor) context.Context {
	if event := obs.From(ctx); event != nil {
		event.Set("user", actor.User)
	}
	return context.WithValue(ctx, actorKey{}, actor)
}

func actorFrom(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(actorKey{}).(Actor)
	return actor, ok
}

// LocalActor is the OS user running this process on this machine.
func LocalActor(kind string) Actor {
	actor := Actor{Kind: kind, Node: localHostname()}
	if current, err := user.Current(); err == nil {
		actor.User = current.Username
	}
	return actor
}

func browserActor(email string) Actor {
	return Actor{Kind: "browser", User: email, Node: localHostname()}
}

// canWriteSession separates peer ownership from same-host local transports.
func (actor Actor) canWriteSession(session SessionInfo) bool {
	owner := session.StartedBy
	if owner == nil {
		return false
	}
	if actor.Kind == "local" && actor.Account && !owner.Account && (owner.Kind == "local" || owner.Kind == "cli") {
		return actor.nativeUser != "" && actor.nativeUser == owner.User && actor.Node == owner.Node
	}
	if actor.Account || owner.Account {
		account := func(a Actor) bool { return a.Account || a.Kind == "browser" || a.Kind == "mcp" }
		return account(actor) && account(*owner) && actor.User != "" && actor.User == owner.User
	}
	if owner.Kind == "peer" || actor.Kind == "peer" {
		return owner.Kind == "peer" && actor.Kind == "peer" &&
			owner.User != "" && actor.User == owner.User &&
			owner.StableNodeID != "" && actor.StableNodeID != "" &&
			actor.StableNodeID == owner.StableNodeID
	}
	if owner.Kind == "browser" || actor.Kind == "browser" {
		return owner.User != "" && actor.User == owner.User
	}
	return owner.User != "" && actor.User == owner.User &&
		owner.Node != "" && actor.Node == owner.Node
}

func localHostname() string {
	name, _ := os.Hostname()
	return name
}

type tailnetSelf struct{ ID, User string }

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
}

type identityResponse struct {
	Node          identityNode `json:"node"`
	Auth          string       `json:"auth"`
	TailscaleUser string       `json:"tailscale_user"`
}

func (g *Gateway) identity(w http.ResponseWriter, r *http.Request) {
	self := g.selfIdentity(r.Context())
	if self.ID == "" {
		self.ID, _ = g.auth.store.NodeID(r.Context())
	}
	roles, err := g.auth.store.Main(r.Context())
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	writeJSON(w, identityResponse{
		Node:          identityNode{Main: roles.MainNode != "" && roles.MainNode == self.ID, ID: self.ID, Name: localHostname(), OS: runtime.GOOS, Version: g.options.Version},
		Auth:          g.auth.email(r),
		TailscaleUser: self.User,
	})
}

// An account assertion has authority only after both machines explicitly approved mesh membership.
func (g *Gateway) forwardedAccount(ctx context.Context, actor Actor, email string) (Actor, error) {
	if email == "" || !g.meshPeer(ctx, actor.StableNodeID) {
		return actor, nil
	}
	normalized, err := normalizeEmail(email)
	if err != nil {
		return Actor{}, errors.New("invalid forwarded account")
	}
	var enabled bool
	if err := g.auth.store.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE email=? AND disabled=0)", normalized).Scan(&enabled); err != nil {
		return Actor{}, err
	}
	if !enabled {
		return Actor{}, errors.New("forwarded account is unavailable")
	}
	actor.User = normalized
	actor.Account = true
	return actor, nil
}

var errLocalAccountUnavailable = errors.New("local gateway account is unavailable; use gateway whoami to bind an enabled account")

// Local transports keep OS authority but attribute account-owned work to the bound account or owner.
func (g *Gateway) localActor(ctx context.Context) (Actor, error) {
	actor := LocalActor("local")
	var bound string
	if err := g.auth.store.Reader.QueryRowContext(ctx, "SELECT account_email FROM local_node WHERE id=1").Scan(&bound); err != nil {
		return Actor{}, err
	}
	var email string
	query := "SELECT email FROM users WHERE disabled=0 AND role='owner' ORDER BY created_at,email LIMIT 1"
	var args []any
	if bound != "" {
		query = "SELECT email FROM users WHERE disabled=0 AND email=?"
		args = append(args, bound)
	}
	err := g.auth.store.Reader.QueryRowContext(ctx, query, args...).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) && bound == "" {
		var hasAccounts bool
		if err := g.auth.store.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users)").Scan(&hasAccounts); err != nil {
			return Actor{}, err
		}
		if !hasAccounts {
			return actor, nil
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Actor{}, errLocalAccountUnavailable
	}
	if err != nil {
		return Actor{}, err
	}
	actor.nativeUser = actor.User
	actor.User = email
	actor.Account = true
	return actor, nil
}
