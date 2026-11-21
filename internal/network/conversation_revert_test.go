package network

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func revertFixture(t *testing.T, dir, path string, oldText string, created bool) *WorkerHTTP {
	t.Helper()
	log := acp.NewEventLog(0)
	log.Append("prompt", 1, []byte(`{"type":"text","text":"go"}`))
	old := `"oldText":"` + oldText + `",`
	if created {
		old = ""
	}
	log.Append("tool_call", 2, []byte(`{"toolCallId":"t1","content":[{"type":"diff","path":`+strconv.Quote(path)+`,`+old+`"newText":"new"}]}`))
	log.Append("tool_call_update", 3, []byte(`{"toolCallId":"t1","status":"completed"}`))
	log.Append("turn_end", 4, []byte(`{}`))
	conv := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	conv.workspace = dir
	return &WorkerHTTP{conversation: conv}
}

func postRevert(worker *WorkerHTTP) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	worker.registerConversation(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/conversation/revert", strings.NewReader(`{"turn":1}`)))
	return w
}

func TestRevertTurn(t *testing.T) {
	dir := t.TempDir()
	edited, created := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	for _, p := range []string{edited, created} {
		if err := os.WriteFile(p, []byte("new"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if w := postRevert(revertFixture(t, dir, "a.txt", "old", false)); w.Code != 200 {
		t.Fatalf("edit revert: %d %s", w.Code, w.Body)
	}
	if got, _ := os.ReadFile(edited); string(got) != "old" {
		t.Fatalf("content = %q", got)
	}
	if info, _ := os.Stat(edited); runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", info.Mode())
	}
	if w := postRevert(revertFixture(t, dir, "b.txt", "", true)); w.Code != 200 {
		t.Fatalf("create revert: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatal("created file survived")
	}
}

func TestRevertTurnRefusesChangedFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("edited later"), 0o644)
	w := postRevert(revertFixture(t, dir, "a.txt", "old", false))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "a.txt changed since this turn") {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if got, _ := os.ReadFile(p); string(got) != "edited later" {
		t.Fatal("file was modified")
	}
}

func TestRevertTurnRefusesOutsidePath(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	p := filepath.Join(outside, "x.txt")
	os.WriteFile(p, []byte("new"), 0o644)
	if w := postRevert(revertFixture(t, dir, p, "old", false)); w.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if got, _ := os.ReadFile(p); string(got) != "new" {
		t.Fatal("outside file was modified")
	}
}

func TestRevertTurnRestoresCompactToolDiffsOnlyWithTheirSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	update := []byte(`{"toolCallId":"tool","content":[{"type":"diff","path":"a.txt","oldText":"old","newTextFromInput":true}]}`)
	for _, includeSource := range []bool{true, false} {
		log := acp.NewEventLog(0)
		log.Append("prompt", 1, []byte(`{"type":"text","text":"go"}`))
		wantStatus := http.StatusConflict
		content, want := "", ""
		if includeSource {
			log.Append("tool_call", 2, []byte(`{"toolCallId":"tool","rawInput":{"content":"new"}}`))
			wantStatus, content, want = http.StatusOK, "new", "old"
		}
		log.Append("tool_call_update", 3, update)
		log.Append("turn_end", 4, []byte(`{}`))
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		conv := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
		conv.workspace = dir
		if response := postRevert(&WorkerHTTP{conversation: conv}); response.Code != wantStatus {
			t.Fatalf("source available=%v, status=%d: %s", includeSource, response.Code, response.Body.String())
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("source available=%v, restored file=%q err=%v", includeSource, got, err)
		}
	}
}
