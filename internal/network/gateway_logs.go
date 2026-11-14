package network

import (
	"errors"
	"net/http"

	"github.com/olegpustovit/agentpack/internal/obs"
)

type issueResolution struct {
	Version string `json:"version" jsonschema:"Version containing the fix; required."`
	Note    string `json:"note,omitempty" jsonschema:"Optional explanation of the fix."`
}

func (g *Gateway) registerDiagnostics(mux *http.ServeMux) {
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
		events, err := obs.TraceEvents(r.Context(), g.options.Home, r.PathValue("trace"))
		writeDiagnostics(w, r, events, err)
	})
	for _, action := range []string{"resolve", "reopen", "ignore"} {
		mux.HandleFunc("POST /api/logs/issues/{fingerprint}/"+action, func(w http.ResponseWriter, r *http.Request) {
			var input issueResolution
			if action == "resolve" && !readJSON(w, r, &input) {
				return
			}
			status := map[string]string{"resolve": "resolved", "reopen": "open", "ignore": "ignored"}[action]
			actor, _ := actorFrom(r.Context())
			issue, err := obs.ChangeIssue(r.Context(), g.options.Home, r.PathValue("fingerprint"), status, input.Version, input.Note, actor.User)
			writeDiagnostics(w, r, issue, err)
		})
	}
}

func writeDiagnostics(w http.ResponseWriter, r *http.Request, value any, err error) {
	if err == nil {
		writeJSON(w, value)
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
