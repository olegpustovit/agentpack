package network

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/agy"
	"github.com/olegpustovit/agentpack/internal/harness/claude"
	"github.com/olegpustovit/agentpack/internal/harness/codex"
	"github.com/olegpustovit/agentpack/internal/harness/cursor"
	"github.com/olegpustovit/agentpack/internal/harness/grok"
	"github.com/olegpustovit/agentpack/internal/harness/omp"
	"github.com/olegpustovit/agentpack/internal/harness/opencode"
	"github.com/olegpustovit/agentpack/internal/obs"
	"golang.org/x/sync/singleflight"
)

// agentLogin reads and signs in one agent on this machine. Names match session harnesses.
type agentLogin struct {
	harness string
	methods []harness.LoginMethod
	account func(context.Context) harness.Account
	login   func(method string) (harness.LoginFlow, error)
	logout  func(context.Context) error // nil when the agent has no way to sign out without a terminal
	// credential is the file holding the agent's sign-in, shared with every node; nil when its
	// sign-in is not a file another machine can use (Cursor's keychain, OMP's database).
	credential func() string
}

var agentLogins = []agentLogin{
	{"claude", claude.LoginMethods, claude.Account, claude.Login, claude.Logout, claude.CredentialFile},
	{"codex", codex.LoginMethods, codex.Account, codex.Login, codex.Logout, codex.CredentialFile},
	{"omp", omp.LoginMethods, omp.Account, omp.Login, nil, nil},
	{"agy", agy.LoginMethods, agy.Account, agy.Login, nil, agy.CredentialFile},
	{"cursor", cursor.LoginMethods, cursor.Account, cursor.Login, cursor.Logout, nil},
	{"grok", grok.LoginMethods, grok.Account, grok.Login, grok.Logout, grok.CredentialFile},
	{"opencode", opencode.LoginMethods, opencode.Account, opencode.Login, nil, opencode.CredentialFile},
}

// loginMethods are an agent's ways to sign in, known without asking its CLI.
func loginMethods(name string) []harness.LoginMethod {
	for _, agent := range agentLogins {
		if agent.harness == name {
			return agent.methods
		}
	}
	return nil
}

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
		accounts := make([]agentAccount, len(agentLogins))
		var wg sync.WaitGroup
		for i, agent := range agentLogins {
			wg.Go(func() {
				account := agent.account(ctx)
				if account.Installed {
					account.Methods = agent.methods
					account.SignOut = account.SignedIn && agent.logout != nil
				}
				accounts[i] = agentAccount{Account: account, Harness: agent.harness}
			})
		}
		wg.Wait()
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

func (g *Gateway) registerAgents(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"agents": g.agents.read(r.Context(), r.URL.Query().Get("fresh") == "1")})
	})
	mux.HandleFunc("POST /api/agents/{harness}/login", func(w http.ResponseWriter, r *http.Request) {
		if !g.ownerOnly(w, r) {
			return
		}
		var input struct {
			Method string `json:"method"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		name := r.PathValue("harness")
		index := -1
		for i, agent := range agentLogins {
			if agent.harness == name {
				index = i
			}
		}
		if index < 0 {
			http.Error(w, "unknown agent", http.StatusBadRequest)
			return
		}
		event := obs.From(r.Context())
		if event != nil {
			event.Set("harness", name)
		}
		flow, err := agentLogins[index].login(input.Method)
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
			respond(w, nil, err)
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
				g.syncAgentCredentials(context.Background()) // share the new sign-in now, not at the next tick
			}
		})
		step, err := attempt.waitLink(r.Context())
		respond(w, step, err)
	})
	mux.HandleFunc("POST /api/agents/{harness}/logout", func(w http.ResponseWriter, r *http.Request) {
		if !g.ownerOnly(w, r) {
			return
		}
		name := r.PathValue("harness")
		index := slices.IndexFunc(agentLogins, func(agent agentLogin) bool { return agent.harness == name && agent.logout != nil })
		if index < 0 {
			http.Error(w, "this agent can't be signed out from here", http.StatusBadRequest)
			return
		}
		if event := obs.From(r.Context()); event != nil {
			event.Set("harness", name)
		}
		err := agentLogins[index].logout(r.Context())
		if err == nil {
			err = g.stopSharingAgentCredential(r.Context(), agentLogins[index])
		}
		g.agents.forget()
		respond(w, map[string]bool{"signed_out": err == nil}, err)
	})
	mux.HandleFunc("GET /api/agents/{harness}/login/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !g.ownerOnly(w, r) {
			return
		}
		if attempt := g.agents.attempt(r.PathValue("harness"), r.PathValue("id")); attempt != nil {
			writeJSON(w, attempt.current())
			return
		}
		http.Error(w, "sign-in not found", http.StatusGone)
	})
	mux.HandleFunc("POST /api/agents/{harness}/login/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !g.ownerOnly(w, r) {
			return
		}
		var input struct {
			Input string `json:"input"`
		}
		if !readJSON(w, r, &input) {
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
		writeJSON(w, attempt.current())
	})
	mux.HandleFunc("DELETE /api/agents/{harness}/login/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !g.ownerOnly(w, r) {
			return
		}
		if attempt := g.agents.attempt(r.PathValue("harness"), r.PathValue("id")); attempt != nil {
			attempt.Cancel()
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// ownerOnly admits the owner: their browser, the CLI bound to them, or their request forwarded
// by an approved peer. Agent sign-ins and agent settings change what every chat runs as.
func (g *Gateway) ownerOnly(w http.ResponseWriter, r *http.Request) bool {
	actor, ok := actorFrom(r.Context())
	allowed := false
	if ok && actor.User != "" && (actor.Kind == "browser" || actor.Kind == "local" || actor.Kind == "peer" && actor.Account) {
		err := g.auth.store.Reader.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM users WHERE email=? AND role='owner' AND disabled=0)", actor.User).Scan(&allowed)
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
