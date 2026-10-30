package network

import (
	"context"
	"net/http"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"time"
)

// Actor is who started or called an operation. It comes from the transport:
// the local OS user, the gateway's browser account, or a verified tailnet whois.
// It is never read from request headers or bodies.
type Actor struct {
	Kind string `json:"kind"`
	User string `json:"user,omitempty"`
	Node string `json:"node,omitempty"`
}

// browserUser is the single gateway account browsers sign in as, so browsers on it are indistinguishable.
const browserUser = "root"

// identityCacheTTL bounds how often identity runs `tailscale status`.
const identityCacheTTL = 60 * time.Second

type actorKey struct{}

func withActor(ctx context.Context, actor Actor) context.Context {
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

func browserActor() Actor {
	return Actor{Kind: "browser", User: browserUser, Node: localHostname()}
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
	writeJSON(w, identityResponse{
		Node:          identityNode{ID: self.ID, Name: localHostname(), OS: runtime.GOOS, Version: g.options.Version},
		Auth:          browserUser,
		TailscaleUser: self.User,
	})
}
