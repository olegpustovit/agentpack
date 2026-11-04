package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/olegpustovit/agentpack/internal/network"
)

func TestPaletteMouseActivatesClickedRowWithinBounds(t *testing.T) {
	for _, size := range [][2]int{{120, 36}, {72, 26}, {46, 30}} {
		t.Run(number(size[0])+"x"+number(size[1]), func(t *testing.T) {
			h := testHome()
			h.width, h.height, h.panel = size[0], size[1], panelPalette
			if cmd := h.handleMouseClick(size[0]-1, 8); cmd != nil {
				t.Fatal("activating Help returned an unexpected command")
			}
			if h.panel != panelHelp || h.paletteCursor != 6 {
				t.Fatalf("clicked row selected panel=%d cursor=%d, want help and row 6", h.panel, h.paletteCursor)
			}

			h.panel, h.paletteCursor = panelPalette, 0
			h.handleMouseClick(size[0], 8)
			if h.panel != panelPalette || h.paletteCursor != 0 {
				t.Fatal("out-of-bounds click activated a palette row")
			}
		})
	}
}

func TestNewSessionMouseFocusesAndCyclesChoicesWithinBounds(t *testing.T) {
	for _, size := range [][2]int{{120, 36}, {72, 26}, {46, 30}} {
		t.Run(number(size[0])+"x"+number(size[1]), func(t *testing.T) {
			h := testHome()
			h.width, h.height, h.panel = size[0], size[1], panelNew
			h.newSession = newSessionForm{directories: []string{"/work/agentpack", "/work/demo"}, field: 2}

			h.handleMouseClick(4, 3)
			if h.newSession.field != 0 {
				t.Fatalf("Agent click focused field %d, want 0", h.newSession.field)
			}
			h.handleMouseClick(11, 3)
			if h.newSession.harness != 1 || h.newSession.field != 0 {
				t.Fatalf("Agent choice: harness=%d field=%d, want 1 and 0", h.newSession.harness, h.newSession.field)
			}
			h.handleMouseClick(13, 4)
			if h.newSession.project != 1 || h.newSession.field != 1 {
				t.Fatalf("Project choice: project=%d field=%d, want 1 and 1", h.newSession.project, h.newSession.field)
			}
			h.handleMouseClick(10, 6)
			if h.newSession.view != 1 || h.newSession.field != 3 {
				t.Fatalf("View choice: view=%d field=%d, want 1 and 3", h.newSession.view, h.newSession.field)
			}

			h.handleMouseClick(size[0], 3)
			if h.newSession.harness != 1 || h.newSession.field != 3 {
				t.Fatal("out-of-bounds click changed the new-session form")
			}
		})
	}
}
func TestSessionsMouseTabsRowsAndBounds(t *testing.T) {
	for _, size := range [][2]int{{120, 36}, {72, 26}, {46, 30}} {
		t.Run(number(size[0])+"x"+number(size[1]), func(t *testing.T) {
			h := testHome()
			h.width, h.height, h.panel = size[0], size[1], panelList
			rows := strings.Split(ansi.Strip(h.viewSessions()), "\n")
			if len(rows) != size[1] {
				t.Fatalf("screen has %d rows, want %d", len(rows), size[1])
			}
			for i, line := range rows {
				if width := ansi.StringWidth(line); width > size[0] {
					t.Fatalf("row %d is %d cells wide, screen is %d", i+1, width, size[0])
				}
			}
			screen := strings.Join(rows, "\n")
			for _, expected := range []string{"[All]", "Needs you", "Running", "History", "Agent", "Project", "Machine", "View", "Age"} {
				if !strings.Contains(screen, expected) {
					t.Errorf("screen is missing %q", expected)
				}
			}
			if size[0] >= 100 && (!strings.Contains(screen, "[ Connect ]") || !strings.Contains(screen, "[ Stop ]")) {
				t.Error("wide screen is missing Connect/Stop actions")
			}

			_, bounds := sessionTabLine(h.sessionTab)
			h.handleMouseClick(bounds[sessionTabNeedsYou][0], 2)
			if h.sessionTab != sessionTabNeedsYou || h.selectedSession() == nil || h.selectedSession().info.ID != "session-bbb" {
				t.Fatalf("Needs you tab selection: tab=%d cursor=%v", h.sessionTab, h.selectedSession())
			}
			h.viewSessions()
			if cmd := h.handleMouseClick(1, 6); cmd == nil || h.panel != panelPreview {
				t.Fatalf("clicking visible session row did not connect: panel=%d cmd=%v", h.panel, cmd != nil)
			}

			h.panel = panelList
			h.cursor = 0
			h.handleMouseClick(size[0], 6)
			if h.panel != panelList || h.cursor != 0 {
				t.Fatal("out-of-bounds click selected a session")
			}
		})
	}
}

func TestSessionsFilterAndActions(t *testing.T) {
	h := testHome()
	h.panel, h.width, h.height = panelList, 120, 36
	h.handleMouseClick(5, 3)
	for _, char := range "codex" {
		h.Update(tea.KeyPressMsg{Code: char, Text: string(char)})
	}
	if !h.sessionFilterFocused || h.sessionFilter != "codex" || len(h.visibleSessionIndices()) != 1 {
		t.Fatalf("filter state: focused=%v filter=%q visible=%v", h.sessionFilterFocused, h.sessionFilter, h.visibleSessionIndices())
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.sessionFilterFocused || h.sessionFilter != "" || h.panel != panelList {
		t.Fatalf("escape did not clear the filter: focused=%v filter=%q panel=%d", h.sessionFilterFocused, h.sessionFilter, h.panel)
	}

	h.sessionFilterFocused = false
	h.sessionTab = sessionTabAll
	if !h.selectSessionID("session-aaa") {
		t.Fatal("running fixture session is missing")
	}
	h.viewSessions()
	rightX := sessionListWidth(h.width) + 2
	if cmd := h.handleMouseClick(rightX, h.height-2); cmd == nil || h.panel != panelPreview {
		t.Fatalf("Connect action did not open the selected session: panel=%d cmd=%v", h.panel, cmd != nil)
	}

	h.panel = panelList
	h.selectSessionID("session-aaa")
	h.viewSessions()
	if cmd := h.handleMouseClick(rightX+15, h.height-2); cmd == nil || !h.sessionActionPending || h.sessionActionID != "session-aaa" {
		t.Fatalf("Stop action was not dispatched: pending=%v id=%q cmd=%v", h.sessionActionPending, h.sessionActionID, cmd != nil)
	}
}

func TestStopSessionPathRoutesPeersThroughNodeQuery(t *testing.T) {
	local := listedSession{machine: "local", info: network.SessionInfo{ID: "session-local"}}
	if got := stopSessionPath(local); got != "/api/sessions/session-local/stop" {
		t.Fatalf("local stop path = %q", got)
	}
	peer := listedSession{machine: "node-a", info: network.SessionInfo{ID: "session-peer"}}
	if got := stopSessionPath(peer); got != "/api/sessions/session-peer/stop?node=node-a" {
		t.Fatalf("peer stop path = %q", got)
	}
}
func TestStopSessionCmdUsesLocalGatewayForPeer(t *testing.T) {
	const secret = "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"
	requests := make(chan struct {
		method, path, node, authorization string
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct {
			method, path, node, authorization string
		}{r.Method, r.URL.Path, r.URL.Query().Get("node"), r.Header.Get("Authorization")}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, "network"), 0700); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(struct {
		Address string `json:"address"`
		Secret  string `json:"secret"`
	}{server.Listener.Addr().String(), secret})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, "network", "node.json"), record, 0600); err != nil {
		t.Fatal(err)
	}

	h := &home{agentpackHome: homeDir}
	result, ok := h.stopSessionCmd(listedSession{machine: "node-a", info: network.SessionInfo{ID: "session-peer"}})().(sessionActionResultMsg)
	if !ok || result.err != nil || result.sessionID != "session-peer" {
		t.Fatalf("peer stop result = %#v", result)
	}
	select {
	case request := <-requests:
		if request.method != http.MethodPost || request.path != "/api/sessions/session-peer/stop" || request.node != "node-a" || request.authorization != "Bearer "+secret {
			t.Fatalf("peer stop request = %#v", request)
		}
	default:
		t.Fatal("peer stop did not reach the local gateway")
	}
}

func TestPreferDirectoryPutsTheOpenedProjectFirst(t *testing.T) {
	dirs := []string{"/w/scratch", "/w/repo", "/w/repo/sub"}
	for _, test := range []struct{ cwd, want string }{
		{"/w/repo", "/w/repo"},
		{"/w/repo/sub/pkg", "/w/repo/sub"},
		{"/w/new", "/w/new"},
	} {
		got := preferDirectory(dirs, test.cwd)
		if got[0] != test.want || len(got) != len(dirs)+map[bool]int{true: 1, false: 0}[test.want == "/w/new"] {
			t.Errorf("preferDirectory(%s) = %v, want %s first", test.cwd, got, test.want)
		}
	}
}

func TestPathBaseHandlesWindowsPaths(t *testing.T) {
	for in, want := range map[string]string{`C:\Users\me\projects\rays`: "rays", "/home/me/repo/": "repo", "": ""} {
		if got := pathBase(in); got != want {
			t.Errorf("pathBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHomePromptTakesLettersAndCtrlXLeads(t *testing.T) {
	h := testHome()
	h.panel = panelHome
	agent := h.newSession.harness
	for _, r := range "hello" {
		h.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if h.homeDraft != "hello" || h.newSession.harness != agent {
		t.Fatalf("draft = %q, harness %d → %d", h.homeDraft, agent, h.newSession.harness)
	}
	h.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	h.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if h.panel != panelList || h.homeDraft != "hello" {
		t.Fatalf("ctrl+x l: panel %d, draft %q", h.panel, h.homeDraft)
	}
}
