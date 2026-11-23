package session

import (
	"context"
	"errors"
	"net/mail"
	"os"
	"os/user"
	"strings"

	"github.com/olegpustovit/agentpack/internal/obs"
)

// Actor is who started or called an operation. It comes from the transport:
// the local OS user, the gateway's browser account, or a verified tailnet whois.
// Caller-provided identities have no authority; forwarded accounts require verified mesh membership.
type Actor struct {
	NativeUser       string `json:"-"`
	ForwardedBrowser bool   `json:"-"`
	Kind             string `json:"kind"`
	Account          bool   `json:"account,omitempty"`
	User             string `json:"user,omitempty"`
	Node             string `json:"node,omitempty"`
	StableNodeID     string `json:"stable_node_id,omitempty"` // Verified tailnet identity; Node is the display name.
	// Agent is the harness of the chat that acted for this user through agentpack tools.
	Agent string `json:"agent,omitempty"`
	Chat  string `json:"-"` // the calling chat's session ID; never serialized
}

type actorKey struct{}

func WithActor(ctx context.Context, actor Actor) context.Context {
	if event := obs.From(ctx); event != nil {
		event.Set("user", actor.User)
	}
	return context.WithValue(ctx, actorKey{}, actor)
}

func ActorFrom(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(actorKey{}).(Actor)
	return actor, ok
}

// LocalActor is the OS user running this process on this machine.
func LocalActor(kind string) Actor {
	actor := Actor{Kind: kind, Node: LocalHostname()}
	if current, err := user.Current(); err == nil {
		actor.User = current.Username
	}
	return actor
}

func BrowserActor(email string) Actor {
	return Actor{Kind: "browser", User: email, Node: LocalHostname()}
}

// CanStartNewFolder requires a browser account verified by this gateway or its enrolled peer.
func (actor Actor) CanStartNewFolder() bool {
	return actor.Kind == "browser" || actor.Kind == "peer" && actor.Account && actor.ForwardedBrowser
}

// CanWriteSession separates peer ownership from same-host local transports.
func (actor Actor) CanWriteSession(session SessionInfo) bool {
	owner := session.StartedBy
	if owner == nil {
		return false
	}
	if actor.Kind == "local" && actor.Account && !owner.Account && (owner.Kind == "local" || owner.Kind == "cli") {
		return actor.NativeUser != "" && actor.NativeUser == owner.User && actor.Node == owner.Node
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

func LocalHostname() string {
	name, _ := os.Hostname()
	return name
}

func NormalizeEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 254 {
		return "", errors.New("email must contain 1 to 254 bytes")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || len(address.Address) > 254 {
		return "", errors.New("enter an email address without a display name")
	}
	return strings.ToLower(address.Address), nil
}
