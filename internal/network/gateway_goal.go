package network

import (
	"net/http"
	"net/http/httptest"
	"strings"
)

// clearGoal asks the worker to clear the chat's goal. A worker that lost the goal (one that predates
// restoring it from the session row) answers "no goal is set" while the row still shows it; the
// row is then cleared here, and an agent-run goal gets "/goal clear" so the agent stops it too.
func (g *Gateway) clearGoal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	record, err := g.workerRecord(r.Context(), id)
	if err != nil {
		http.Error(w, "session unavailable", http.StatusNotFound)
		return
	}
	if !authorizeACPWrite(w, r, record.Session) {
		return
	}
	answer := httptest.NewRecorder()
	proxyWorkerConversation(answer, r, record, "/goal")
	goal := record.Session.Goal
	if answer.Code != http.StatusNotFound || strings.TrimSpace(answer.Body.String()) != errNoGoal.Error() || goal == nil || goal.State == "cleared" {
		for key, values := range answer.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(answer.Code)
		_, _ = w.Write(answer.Body.Bytes())
		return
	}
	if goal.Engine == "native" {
		if err := ConversationPrompt(r.Context(), g.options.Home, id, "/goal clear"); err != nil {
			fail(w, r, err)
			return
		}
	}
	persistSessionJSON(r.Context(), g.auth.store.DB, id, "goal", nil, true)
	writeJSON(w, map[string]bool{"accepted": true})
}
