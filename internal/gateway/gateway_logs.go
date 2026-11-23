package gateway

import (
	"errors"
	"net/http"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
)

type issueResolution struct {
	Version string `json:"version" jsonschema:"Version containing the fix; required."`
	Note    string `json:"note,omitempty" jsonschema:"Optional explanation of the fix."`
}

func (g *Gateway) registerDiagnostics(mux routeRegistrar) {
	mux.HandleFunc("POST /api/logs/ingest", g.peerOnly(g.ingestTraces))
	mux.HandleFunc("GET /api/logs/trace/{trace}", g.peerOnly(g.localTrace))
	mux.HandleFunc("GET /api/logs/issues", func(w http.ResponseWriter, r *http.Request) {
		status := "open"
		if r.URL.Query().Has("status") {
			status = r.URL.Query().Get("status")
		}
		issues, err := obs.ListIssues(r.Context(), g.options.Home, status)
		writeDiagnostics(w, r, issues, err)
	})
	mux.HandleFunc("GET /api/logs/issues/{fingerprint}", func(w http.ResponseWriter, r *http.Request) {
		issue, err := obs.GetIssue(r.Context(), g.options.Home, r.PathValue("fingerprint"))
		writeDiagnostics(w, r, issue, err)
	})
	mux.HandleFunc("GET /api/logs/traces/{trace}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			page, err := g.tracePage(r.Context(), r.PathValue("trace"), r.URL.Query().Get("after"), r.URL.Query().Get("all_nodes") == "1", g.tailnetTarget)
			writeDiagnostics(w, r, page, err)
			return
		}
		events, err := g.traceEvents(r.Context(), r.PathValue("trace"), r.URL.Query().Get("all_nodes") == "1", g.tailnetTarget)
		writeDiagnostics(w, r, events, err)
	})
	for _, action := range []string{"resolve", "reopen", "ignore"} {
		mux.HandleFunc("POST /api/logs/issues/{fingerprint}/"+action, func(w http.ResponseWriter, r *http.Request) {
			var input issueResolution
			if action == "resolve" && !network.ReadJSON(w, r, &input) {
				return
			}
			status := map[string]string{"resolve": "resolved", "reopen": "open", "ignore": "ignored"}[action]
			actor, _ := session.ActorFrom(r.Context())
			issue, err := obs.ChangeIssue(r.Context(), g.store, g.options.Home, r.PathValue("fingerprint"), status, input.Version, input.Note, actor.User)
			writeDiagnostics(w, r, issue, err)
		})
	}
}

func writeDiagnostics(w http.ResponseWriter, r *http.Request, value any, err error) {
	if err == nil {
		network.WriteJSON(w, value)
		return
	}
	if event := obs.From(r.Context()); event != nil {
		event.Fail(err)
	}
	status := http.StatusBadRequest
	if errors.Is(err, obs.ErrIssueNotFound) {
		status = http.StatusNotFound
	}
	http.Error(w, err.Error(), status)
}
