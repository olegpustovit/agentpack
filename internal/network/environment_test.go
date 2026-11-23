package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
)

// An agentpack update must never look like a package change: an environment from another binary reads as absent.
func TestReadEnvironmentIgnoresDigestsFromAnotherBinary(t *testing.T) {
	home := t.TempDir()
	id := "session-TEST"
	if err := os.MkdirAll(filepath.Join(home, "network", "sessions", id), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "network", "sessions", id, "environment")
	digest := strings.Repeat("ab", 32)
	if err := writeEnvironment(path, Environment{Digest: digest}); err != nil {
		t.Fatal(err)
	}
	if got, err := readEnvironment(path, true); err != nil || got.Digest != digest {
		t.Fatalf("same binary: got %+v, %v", got, err)
	}
	for name, content := range map[string]string{"older agentpack": digest + "\n" + BinaryTag(), "other binary": `{"digest":"` + digest + `","binary":"/old/ap:1:2"}`} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := readEnvironment(path, true); err != nil || got.Digest != "" {
			t.Fatalf("%s: got %+v, %v; want absent", name, got, err)
		}
	}
}

func testProject(t *testing.T) (home, root string) {
	t.Helper()
	home, root = t.TempDir(), t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	if err := manifest.WriteStub(root, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(root, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	return home, root
}

// The selected content is hashed again only when the launch fingerprint moved, and each
// workspace keeps its own cached environment.
func TestLaunchEnvironmentRehashesOnlyWhenItsFingerprintMoves(t *testing.T) {
	home, root := testProject(t)
	const id = "session-cached-environment"
	if err := os.MkdirAll(filepath.Join(home, "network", "sessions", id), 0700); err != nil {
		t.Fatal(err)
	}
	launch := launchEnvironment{home: home, id: id, definition: root, workspace: root, mode: "default", target: harness.Claude}
	record := func() Environment {
		t.Helper()
		_, current, err := launch.record()
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	first := record()
	// A cached digest that no content produces proves the second launch did not rehash.
	workspace := func() string {
		matches, _ := filepath.Glob(filepath.Join(home, "projects", "*", "runtime-environment-*.json"))
		if len(matches) != 1 {
			t.Fatalf("runtime environment caches: %v", matches)
		}
		return matches[0]
	}()
	planted := first
	planted.Digest = strings.Repeat("cd", 32)
	if err := writeEnvironment(workspace, planted); err != nil {
		t.Fatal(err)
	}
	if got := record(); got.Digest != planted.Digest {
		t.Fatal("unchanged launch rehashed its environment")
	}
	endpoint := "https://one.example/mcp"
	if err := manifest.AddMCPServer(root, "one", mcp.Server{URL: &endpoint}); err != nil {
		t.Fatal(err)
	}
	changed := record()
	if changed.Digest == first.Digest || changed.Digest == planted.Digest || !slices.Contains(changed.Items, "MCP one") {
		t.Fatalf("manifest change kept the environment: %+v", changed)
	}
	launch.workspace = t.TempDir()
	skill := filepath.Join(launch.workspace, ".agents", "skills", "example", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: example\ndescription: Example\n---\nWorkspace-specific skill\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := record(); got.Digest == changed.Digest || !slices.Contains(got.Items, "skill example") {
		t.Fatalf("another workspace reused the first workspace's environment: %+v", got)
	}
}

func TestReloadNoticeListsWhatChanged(t *testing.T) {
	got := reloadNotice([]string{"MCP github", "skill old"}, []string{"MCP github", "MCP github connected", "skill canvas-design"})
	if want := "Reloaded: MCP github connected, +skill canvas-design, −skill old"; got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
	if got := reloadNotice([]string{"MCP a connected"}, nil); got != "Reloaded: MCP a disconnected" {
		t.Fatalf("notice = %q", got)
	}
}

// An edit nothing announced (a skill changed in an editor) reloads the agent once, before the next
// prompt reaches it; an unchanged environment never does.
func TestPreTurnCheckReloadsAfterAnUnannouncedEdit(t *testing.T) {
	previous := promptAcceptWait
	promptAcceptWait = time.Millisecond // the turn is still running when the request returns
	t.Cleanup(func() { promptAcceptWait = previous })
	home, root := testProject(t)
	const id = "session-pre-turn"
	directory := filepath.Join(home, "network", "sessions", id)
	skill := filepath.Join(root, ".agents", "skills", "x", "SKILL.md")
	for _, dir := range []string{directory, filepath.Dir(skill)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(skill, []byte("---\nname: x\ndescription: X\n---\nfirst\n"), 0600); err != nil {
		t.Fatal(err)
	}
	environment := launchEnvironment{home: home, id: id, definition: root, workspace: root, mode: "default", target: harness.Claude}
	if _, _, err := environment.record(); err != nil {
		t.Fatal(err)
	}
	session, agent := newFakeAgentSession(t)
	var order []string
	worker := &WorkerHTTP{Conversation: session, SessionDir: directory, environment: &environment, Diagnostics: t.Context(), done: make(chan struct{})}
	worker.restartACP = func(context.Context) error {
		order = append(order, "session/load")
		_, _, err := environment.record() // what the restarted agent records at start
		return err
	}
	mux := http.NewServeMux()
	worker.RegisterConversation(mux)
	prompt := func(text string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest("POST", "/conversation/prompt", strings.NewReader(`{"text":"`+text+`"}`)))
		if recorder.Code != 200 {
			t.Fatalf("prompt %s = %d %s", text, recorder.Code, recorder.Body)
		}
		order = append(order, agent.nextPrompt(t).Text)
		agent.turns <- fakeTurn{}
		for deadline := time.Now().Add(2 * time.Second); session.ConversationState() != string(StateIdle); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the turn never ended")
			}
		}
	}
	prompt("first")
	if err := os.WriteFile(skill, []byte("---\nname: x\ndescription: X\n---\nedited\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prompt("second")
	prompt("third")
	if want := []string{"first", "session/load", "second", "third"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}
