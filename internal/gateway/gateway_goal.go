package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
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
	if !session.AuthorizeWrite(w, r, record.Session) {
		return
	}
	answer := httptest.NewRecorder()
	network.ProxyWorkerConversation(answer, r, record, "/goal")
	goal := record.Session.Goal
	if answer.Code != http.StatusNotFound || strings.TrimSpace(answer.Body.String()) != network.ErrNoGoal.Error() || goal == nil || goal.State == "cleared" {
		for key, values := range answer.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(answer.Code)
		_, _ = w.Write(answer.Body.Bytes())
		return
	}
	if goal.Engine == "native" {
		if err := network.ConversationPrompt(r.Context(), g.options.Home, id, "/goal clear"); err != nil {
			fail(w, r, err)
			return
		}
	}
	// That worker's log has no fold to clear it (a current worker's sink owns the column).
	if _, err := g.store.DB.ExecContext(r.Context(), "UPDATE sessions SET goal='' WHERE id=? AND archived_at=0 AND node_id="+state.LocalNodeSQL, id); err != nil {
		fail(w, r, err)
		return
	}
	network.WriteJSON(w, map[string]bool{"accepted": true})
}
