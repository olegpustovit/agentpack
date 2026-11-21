package network

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestChatShellUsesWorktreeAndStaysOutOfSessionList(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires the Unix login shell fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	home := t.TempDir()
	main := t.TempDir()
	worktree := filepath.Join(home, "worktrees", "repo", "feature")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	folderID, err := FolderIdentity(worktree)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	g, origin := identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) {
		ready := make(chan string, 1)
		go func() {
			_, err := RunWorker(ctx, home, "test", WorkerLaunch{
				Session: info, Transport: info.Transport, Directory: info.Directory, Program: "/bin/sh",
				Arguments: []string{"-i"}, Rows: 24, Columns: 80,
			}, func(id string) error { ready <- id; return nil })
			done <- err
		}()
		select {
		case id := <-ready:
			return id, nil
		case err := <-done:
			return "", errors.Join(errors.New("worker exited before ready"), err)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	parent := SessionInfo{ID: "session-parent", Transport: "acp", Harness: "codex", Directory: worktree, WorktreeOf: main, FolderID: folderID, StartedBy: new(browserActor(testBrowserEmail))}
	if err := upsertSession(ctx, g.auth.store.DB, parent); err != nil {
		t.Fatal(err)
	}
	testBrowserSession(t, g.auth, testBrowserEmail, "shell-owner")
	handler := g.Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, origin.String()+path, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Origin", origin.String())
		r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: "shell-owner"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	created := request("POST", "/api/sessions", `{"transport":"shell","parent_id":"session-parent","directory":"/ignored"}`)
	var shell SessionInfo
	if err := json.Unmarshal(created.Body.Bytes(), &shell); err != nil || shell.Directory != worktree || shell.WorktreeOf != main || shell.ParentID != parent.ID {
		t.Fatalf("shell: %+v, %v", shell, err)
	}
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("worker did not stop")
		}
	}()
	record, err := workerRecord(home, shell.ID)
	if err != nil {
		t.Fatal(err)
	}
	socket, err := attachWorker(ctx, record, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if err := socket.WriteJSON(terminalControl{Action: "resize", Rows: 15, Columns: 100}); err != nil {
		t.Fatal(err)
	}
	readWorker(t, socket, func(kind int, data []byte) bool {
		var notice terminalNotice
		return kind == websocket.TextMessage && json.Unmarshal(data, &notice) == nil && notice.State.Rows == 15 && notice.State.Columns == 100
	})
	if err := socket.WriteMessage(websocket.BinaryMessage, []byte("pwd\r")); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	readWorker(t, socket, func(kind int, data []byte) bool {
		if kind == websocket.BinaryMessage {
			output.Write(data)
		}
		return strings.Contains(output.String(), worktree)
	})
	if listed := request("GET", "/api/sessions", ""); strings.Contains(listed.Body.String(), shell.ID) {
		t.Fatalf("chat shell leaked into the list: %s", listed.Body.String())
	}
	if shells := request("GET", "/api/sessions/"+parent.ID+"/shells", ""); !strings.Contains(shells.Body.String(), shell.ID) {
		t.Fatalf("chat should find its own shell: %s", shells.Body.String())
	}
	request("POST", "/api/sessions/"+shell.ID+"/detach-shell", "")
	if listed := request("GET", "/api/sessions", ""); !strings.Contains(listed.Body.String(), shell.ID) {
		t.Fatalf("promoted shell missing from session list: %s", listed.Body.String())
	}
}
