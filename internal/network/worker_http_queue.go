package network

import (
	"errors"
	"github.com/olegpustovit/agentpack/internal/session"
	"net/http"
	"strings"
)

// registerQueueAndGoal serves the prompt queue and goal routes; the gateway proxies them with prompt auth.
func (worker *WorkerHTTP) registerQueueAndGoal(mux *http.ServeMux) {
	mux.HandleFunc("PATCH /conversation/queue/{queueId}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Text string `json:"text"`
		}
		if !ReadJSON(w, r, &input) {
			return
		}
		writeQueueResult(w, worker.conversationSession().EditQueued(r.PathValue("queueId"), strings.TrimSpace(input.Text)))
	})
	mux.HandleFunc("DELETE /conversation/queue/{queueId}", func(w http.ResponseWriter, r *http.Request) {
		writeQueueResult(w, worker.conversationSession().RemoveQueued(r.PathValue("queueId")))
	})
	mux.HandleFunc("POST /conversation/queue/{queueId}/steer", func(w http.ResponseWriter, r *http.Request) {
		writeQueueResult(w, worker.conversationSession().SteerQueued(r.Context(), r.PathValue("queueId")))
	})
	mux.HandleFunc("POST /conversation/goal", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Objective string              `json:"objective"`
			Checks    string              `json:"checks"`
			Limits    *session.GoalLimits `json:"limits"`
		}
		if !ReadJSON(w, r, &input) {
			return
		}
		objective := strings.TrimSpace(input.Objective)
		if objective == "" {
			http.Error(w, "objective is required", http.StatusBadRequest)
			return
		}
		writeQueueResult(w, worker.conversationSession().SetGoal(r.Context(), objective, strings.TrimSpace(input.Checks), input.Limits))
	})
	mux.HandleFunc("PATCH /conversation/goal", func(w http.ResponseWriter, r *http.Request) {
		var change GoalChange
		if !ReadJSON(w, r, &change) {
			return
		}
		if change.Action != "" && change.Action != "pause" && change.Action != "resume" {
			http.Error(w, "action must be pause or resume", http.StatusBadRequest)
			return
		}
		if change.Objective != nil {
			if *change.Objective = strings.TrimSpace(*change.Objective); *change.Objective == "" {
				http.Error(w, "objective is required", http.StatusBadRequest)
				return
			}
		}
		writeQueueResult(w, worker.conversationSession().UpdateGoal(r.Context(), change))
	})
	mux.HandleFunc("DELETE /conversation/goal", func(w http.ResponseWriter, r *http.Request) {
		writeQueueResult(w, worker.conversationSession().ClearGoal(r.Context()))
	})
}

func writeQueueResult(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		WriteJSON(w, map[string]bool{"accepted": true})
	case errors.Is(err, errNotQueued), errors.Is(err, ErrNoGoal):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrEmptyPrompt):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, session.ErrSessionArchived):
		http.Error(w, "archived", http.StatusConflict)
	default:
		http.Error(w, "session unavailable", http.StatusConflict)
	}
}
