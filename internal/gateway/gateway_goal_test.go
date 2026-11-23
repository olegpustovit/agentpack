package gateway

import (
	"context"
	"encoding/json"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A worker that lost the goal answers "no goal is set" while the row still shows it: the gateway
// clears the row and tells the agent to stop its own goal.
func TestClearGoalReconcilesAWorkerThatLostIt(t *testing.T) {
	home := t.TempDir()
	var prompted string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversation/goal":
			http.Error(w, network.ErrNoGoal.Error(), http.StatusNotFound)
		case "/conversation/prompt":
			body, _ := io.ReadAll(r.Body)
			var input struct{ Text string }
			_ = json.Unmarshal(body, &input)
			prompted = input.Text
			_, _ = w.Write([]byte(`{"accepted":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer worker.Close()
	g, _ := identityGateway(t, home, nil)
	owner := &session.Actor{Kind: "browser", Account: true, User: "owner@example.com"}
	id := "session-goal"
	if err := session.UpsertSession(context.Background(), g.store.DB, session.SessionInfo{ID: id, Directory: seedFolder(t, home), Harness: "claude", Transport: "acp", StartedBy: owner}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.store.DB.Exec("UPDATE sessions SET goal=? WHERE id=?", string(network.MustRawJSON(&session.Goal{Objective: "ship", State: "active", Engine: "native"})), id); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(network.WorkerRecord{ID: id, Address: strings.TrimPrefix(worker.URL, "http://"), Secret: strings.Repeat("s", 64)})
	if err := os.WriteFile(filepath.Join(dir, "worker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+id+"/conversation/goal", nil)
	request.SetPathValue("id", id)
	request = request.WithContext(session.WithActor(request.Context(), *owner))
	response := httptest.NewRecorder()
	g.clearGoal(response, request)
	if response.Code != http.StatusOK || prompted != "/goal clear" {
		t.Fatalf("response %d %q, prompted %q", response.Code, response.Body.String(), prompted)
	}
	stored, err := session.StoredSession(context.Background(), g.store.Reader, id)
	if err != nil || stored.Goal != nil {
		t.Fatalf("goal kept: %+v, %v", stored.Goal, err)
	}
	// With no goal on the row either, the worker's answer stands.
	response = httptest.NewRecorder()
	g.clearGoal(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), network.ErrNoGoal.Error()) {
		t.Fatalf("second clear %d %q", response.Code, response.Body.String())
	}
}
