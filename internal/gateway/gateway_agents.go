package gateway

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
	"golang.org/x/sync/singleflight"
)

// Account probes run each CLI, so their answers are kept briefly and refreshed after a sign-in.
const agentAccountsTTL = time.Minute

type agentAccount struct {
	harness.Account
	Harness string `json:"harness"`
}

type agentAccounts struct {
	mu       sync.Mutex
	at       time.Time
	accounts []agentAccount
	attempts map[string]*loginAttempt // by harness; one sign-in per agent at a time
	// generation counts finished sign-ins; a probe round that saw an older one isn't kept.
	generation int
	probing    singleflight.Group // concurrent readers share one round of probes
}

func (c *agentAccounts) read(ctx context.Context, fresh bool) []agentAccount {
	c.mu.Lock()
	if !fresh && time.Since(c.at) < agentAccountsTTL {
		defer c.mu.Unlock()
		return c.accounts
	}
	c.mu.Unlock()
	accounts, _, _ := c.probing.Do("accounts", func() (any, error) {
		c.mu.Lock()
		generation := c.generation
		c.mu.Unlock()
		// One reader leaving must not cut the probes short for the others.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		accounts := make([]agentAccount, len(network.Agents))
		var wg sync.WaitGroup
		for i, agent := range network.Agents {
			if agent.Login == nil {
				continue
			}
			wg.Go(func() {
				account := agent.Login.Account(ctx)
				if account.Installed {
					account.Methods = agent.Login.Methods
					account.SignOut = account.SignedIn && agent.Login.Logout != nil
				}
				accounts[i] = agentAccount{Account: account, Harness: string(agent.Target)}
			})
		}
		wg.Wait()
		accounts = slices.DeleteFunc(accounts, func(account agentAccount) bool { return account.Harness == "" })
		c.mu.Lock()
		defer c.mu.Unlock()
		if generation == c.generation {
			c.at, c.accounts = time.Now(), accounts
		}
		return accounts, nil
	})
	return accounts.([]agentAccount)
}

// forget drops cached accounts after a sign-in or sign-out, including a probe round still running.
func (c *agentAccounts) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = time.Time{}
	c.generation++
}

func (c *agentAccounts) attempt(name, id string) *loginAttempt {
	c.mu.Lock()
	defer c.mu.Unlock()
	if attempt := c.attempts[name]; attempt != nil && attempt.current().ID == id {
		return attempt
	}
	return nil
}

func (g *Gateway) registerAgents(mux routeRegistrar) {
	mux.HandleFunc("GET /api/agent-snapshot", g.serveAgentSnapshot)
	mux.HandleFunc("GET /api/agent-sessions", g.serveAgentSessions)
	mux.HandleFunc("GET /api/agents", func(w http.ResponseWriter, r *http.Request) {
		network.WriteJSON(w, map[string]any{"agents": g.agents.read(r.Context(), r.URL.Query().Get("fresh") == "1")})
	})
	mux.HandleFunc("POST /api/agents/{harness}/login", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Method string `json:"method"`
		}
		if !network.ReadJSON(w, r, &input) {
			return
		}
		name := r.PathValue("harness")
		agent, ok := network.AgentEntry(name)
		if !ok {
			http.Error(w, "unknown agent", http.StatusBadRequest)
			return
		}
		event := obs.From(r.Context())
		if event != nil {
			event.Set("harness", name)
		}
		flow, err := agent.Login.Login(input.Method)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// One sign-in per agent: a new one replaces the last, whose command may hold the same login.
		g.agents.mu.Lock()
		if previous := g.agents.attempts[name]; previous != nil {
			previous.Cancel()
		}
		g.agents.mu.Unlock()
		attempt, err := startLogin(flow)
		if err != nil {
			fail(w, r, err)
			return
		}
		g.agents.mu.Lock()
		if g.agents.attempts == nil {
			g.agents.attempts = map[string]*loginAttempt{}
		}
		// A sign-in started at the same moment loses: its command must not keep running unseen.
		if other := g.agents.attempts[name]; other != nil && other != attempt {
			other.Cancel()
		}
		g.agents.attempts[name] = attempt
		g.agents.mu.Unlock()
		obs.Go("gateway.agent_login", func() {
			<-attempt.done
			step := attempt.current()
			_, done := obs.Start(context.Background(), "gateway", "agent_login")
			done.Set("harness", name)
			done.Set("state", step.State)
			if step.State == "failed" {
				done.Set("error", step.Error)
			}
			done.End()
			g.agents.forget()
			if step.State == "done" {
				g.syncAgentCredentials(context.Background(), name) // share the new sign-in now, not at the next tick
				g.reloadSignedOutChats(context.Background(), name)
			}
		})
		step, err := attempt.waitLink(r.Context())
		if err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, step)
	})
	mux.HandleFunc("POST /api/agents/{harness}/logout", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("harness")
		agent, ok := network.AgentEntry(name)
		if !ok || agent.Login.Logout == nil {
			http.Error(w, "this agent can't be signed out from here", http.StatusBadRequest)
			return
		}
		if event := obs.From(r.Context()); event != nil {
			event.Set("harness", name)
		}
		err := agent.Login.Logout(r.Context())
		if err == nil {
			err = g.stopSharingAgentCredential(r.Context(), agent)
		}
		g.agents.forget()
		if err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, map[string]bool{"signed_out": true})
	})
	mux.HandleFunc("GET /api/agents/{harness}/login/{id}", func(w http.ResponseWriter, r *http.Request) {
		if attempt := g.agents.attempt(r.PathValue("harness"), r.PathValue("id")); attempt != nil {
			network.WriteJSON(w, attempt.current())
			return
		}
		http.Error(w, "sign-in not found", http.StatusGone)
	})
	mux.HandleFunc("POST /api/agents/{harness}/login/{id}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Input string `json:"input"`
		}
		if !network.ReadJSON(w, r, &input) {
			return
		}
		attempt := g.agents.attempt(r.PathValue("harness"), r.PathValue("id"))
		if attempt == nil {
			http.Error(w, "sign-in not found", http.StatusGone)
			return
		}
		if err := attempt.Submit(r.Context(), input.Input); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		network.WriteJSON(w, attempt.current())
	})
	mux.HandleFunc("DELETE /api/agents/{harness}/login/{id}", func(w http.ResponseWriter, r *http.Request) {
		if attempt := g.agents.attempt(r.PathValue("harness"), r.PathValue("id")); attempt != nil {
			attempt.Cancel()
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// requireOwner admits enabled owners through browser, CLI, MCP or a peer
// forwarding an account. Bare peer identities and unknown actors are denied.
// The local CLI retains setup and shutdown authority before the first account exists.
func (g *Gateway) requireOwner(w http.ResponseWriter, r *http.Request) bool {
	actor, ok := session.ActorFrom(r.Context())
	allowed := false
	if ok && (actor.Kind == "browser" || actor.Kind == "local" || actor.Kind == "mcp" || actor.Kind == "peer" && actor.Account) {
		err := g.store.Reader.QueryRowContext(r.Context(), `SELECT EXISTS(
			SELECT 1 FROM users WHERE email=? AND role='owner' AND disabled=0
		) OR (? AND NOT EXISTS(SELECT 1 FROM users))`, actor.User, actor.Kind == "local" && !actor.Account).Scan(&allowed)
		if err != nil {
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
		}
	}
	if !allowed {
		http.Error(w, "only the owner can change this", http.StatusForbidden)
	}
	return allowed
}

// reloadSignedOutChats restarts this node's running chats of harness that stopped for a sign-in, now
// that the agent is signed in, so they continue without anyone at the browser. Other chats keep
// running; their agents refresh their own tokens.
func (g *Gateway) reloadSignedOutChats(ctx context.Context, harness string) {
	ctx, event := obs.Start(ctx, "gateway", "reload_signed_out_chats")
	event.Set("harness", harness)
	defer event.End()
	target, err := network.HarnessTarget(harness)
	if err != nil {
		event.Fail(err)
		return
	}
	sessions, err := network.SessionsIn(ctx, g.store.Reader, g.options.Home)
	if err != nil {
		event.Fail(err)
		return
	}
	var failures []error
	for _, info := range sessions {
		if chat, err := network.HarnessTarget(info.Harness); info.Transport != "acp" || !info.State.Running || err != nil || chat != target {
			continue
		}
		// The card retires once the agent answers, so only a notice after the last answer counts.
		// Older adapters only said so in the description, which the chat's sign-in card also accepts.
		var signIn bool
		err := g.store.Reader.QueryRowContext(ctx, `SELECT IFNULL(kind='notice' AND (json_extract(body,'$.payload.code')='auth_required'
			OR json_extract(body,'$.payload.title')='The agent could not run this prompt' AND json_extract(body,'$.payload.description') LIKE '%Authentication required%'), 0)
			FROM conversation_events WHERE session_id=? AND kind IN ('notice','agent_message_chunk','tool_call') ORDER BY seq DESC LIMIT 1`, info.ID).Scan(&signIn)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !signIn {
			continue
		}
		if err == nil {
			_, err = network.RestartWorkerWhenIdle(ctx, g.options.Home, info.ID, rand.Text())
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("session %s: %w", info.ID, err))
		}
	}
	event.Fail(errors.Join(failures...))
}

func (g *Gateway) serveAgentSessions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("directory") == "" || r.URL.Query().Get("node") != "" {
		sessions, err := g.meshAgentSessions(r.Context(), r.URL.Query().Get("node"), r.URL.Query().Get("directory"), r.URL.Query().Get("harness"))
		if err != nil {
			meshReadError(w, r, err)
			return
		}
		network.WriteJSON(w, map[string]any{"sessions": sessions})
		return
	}

	directory := r.URL.Query().Get("directory")
	harness := r.URL.Query().Get("harness")
	if !g.knownDirectory(r.Context(), directory) || harness == "" {
		http.Error(w, "choose a recent folder from this node", http.StatusBadRequest)
		return
	}
	var definitionRoot, modeName string
	err := g.store.Reader.QueryRowContext(r.Context(), `SELECT definition_root,mode FROM sessions
		WHERE node_id=`+state.LocalNodeSQL+` AND hidden=0 AND transport<>'shell' AND directory=? AND harness=?
		ORDER BY last_used DESC,id DESC LIMIT 1`, filepath.Clean(directory), harness).Scan(&definitionRoot, &modeName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, r, err)
		return
	}
	sessions, err := network.ListAgentSessions(r.Context(), g.options.Home, directory, harness, definitionRoot, modeName)
	if err != nil {
		fail(w, r, err)
		return
	}
	local, err := g.nodeID(r.Context())
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	folderID, err := network.FolderIdentity(directory)
	if err != nil {
		fail(w, r, err)
		return
	}
	archived, err := network.ArchivedNativeIDs(r.Context(), g.store, folderID, harness)
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	sessions = slices.Clone(sessions)
	sessions = slices.DeleteFunc(sessions, func(session network.AgentSession) bool { return archived[session.ID] })
	for i := range sessions {
		sessions[i].NodeID = local
		sessions[i].Reachable = true
	}
	network.WriteJSON(w, map[string]any{"sessions": sessions})
}
