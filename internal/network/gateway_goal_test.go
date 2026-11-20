package network

import (
	"context"
	"encoding/json"
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
			http.Error(w, errNoGoal.Error(), http.StatusNotFound)
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
	owner := &Actor{Kind: "browser", Account: true, User: "owner@example.com"}
	id := "session-goal"
	if err := upsertSession(context.Background(), g.auth.store.DB, SessionInfo{ID: id, Directory: seedFolder(t, home), Harness: "claude", Transport: "acp", StartedBy: owner}); err != nil {
		t.Fatal(err)
	}
	persistSessionJSON(context.Background(), g.auth.store.DB, id, "goal", &Goal{Objective: "ship", State: "active", Engine: "native"}, false)
	dir := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(WorkerRecord{ID: id, Address: strings.TrimPrefix(worker.URL, "http://"), Secret: strings.Repeat("s", 64)})
	if err := os.WriteFile(filepath.Join(dir, "worker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+id+"/conversation/goal", nil)
	request.SetPathValue("id", id)
	request = request.WithContext(withActor(request.Context(), *owner))
	response := httptest.NewRecorder()
	g.clearGoal(response, request)
	if response.Code != http.StatusOK || prompted != "/goal clear" {
		t.Fatalf("response %d %q, prompted %q", response.Code, response.Body.String(), prompted)
	}
	stored, err := storedSession(context.Background(), g.auth.store.Reader, id)
	if err != nil || stored.Goal != nil {
		t.Fatalf("goal kept: %+v, %v", stored.Goal, err)
	}
	// With no goal on the row either, the worker's answer stands.
	response = httptest.NewRecorder()
	g.clearGoal(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), errNoGoal.Error()) {
		t.Fatalf("second clear %d %q", response.Code, response.Body.String())
	}
}
