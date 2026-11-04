package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
)

func fixtureSessions() []network.SessionInfo {
	return []network.SessionInfo{
		{
			ID: "session-aaa", Name: "Refactor hub", Harness: "cursor", Mode: "focused",
			Directory: "/work/agentpack", ConversationState: "working",
			State: network.TerminalState{Running: true},
		},
		{
			ID: "session-bbb", Harness: "codex", Mode: "default",
			Directory: "/work/demo", ConversationState: "needs_you",
			State: network.TerminalState{Running: true},
		},
		{
			ID: "session-ccc", Name: "Docs pass", Harness: "claude",
			Directory: "/work/agentpack", ConversationState: "idle",
			State: network.TerminalState{Running: true},
		},
		{
			ID: "session-ddd", Harness: "omp",
			Directory: "/work/old",
			State:     network.TerminalState{Running: false},
		},
	}
}

func testHome() *home {
	h := newHome("", func(_ context.Context, _ string) ([]listedSession, error) {
		return listedFromInfos("local", "devbox", fixtureSessions()), nil
	})
	h.machineName = "devbox"
	h.gatewayLine = "http://127.0.0.1:7421"
	h.applySessions(listedFromInfos("local", "devbox", fixtureSessions()))
	h.width, h.height = 120, 36
	return h
}

func TestListScreenGolden(t *testing.T) {
	testScreenGoldens(t, func(h *home) { h.panel = panelList })
}

func TestHomeScreenLayout(t *testing.T) {
	for _, size := range [][2]int{{120, 36}, {72, 26}, {46, 30}} {
		h := testHome()
		h.width, h.height = size[0], size[1]
		h.newSession.directories = []string{"/work/agentpack", "/work/demo"}
		screen := ansi.Strip(h.viewHome())
		for _, want := range []string{"Describe a task", "Agent: Claude", "Project: agentpack", "Machine: devbox", "View: Chat"} {
			if !strings.Contains(screen, want) {
				t.Errorf("%dx%d home screen missing %q", size[0], size[1], want)
			}
		}
		for i, row := range strings.Split(screen, "\n") {
			if width := ansi.StringWidth(row); width > size[0] {
				t.Errorf("%dx%d row %d is %d cells wide", size[0], size[1], i+1, width)
			}
		}
	}
}
func TestHomeScreenGolden(t *testing.T) {
	testScreenGoldens(t, func(h *home) {
		h.panel = panelHome
		h.newSession = newSessionForm{directories: []string{"/work/agentpack", "/work/demo"}, machine: "local"}
	})
}

func TestNewSessionScreenGolden(t *testing.T) {
	testScreenGoldens(t, func(h *home) {
		h.panel = panelNew
		h.newSession = newSessionForm{directories: []string{"/work/agentpack", "/work/demo"}, project: 0}
	})
}

func TestPreviewScreenGolden(t *testing.T) {
	testScreenGoldens(t, func(h *home) {
		h.panel = panelPreview
		h.selectedSession().info.Transport = "acp"
		h.previewLines = []string{"◆ Needs you", "Allow claude to run this command?", "git push origin fix/reattach-offset"}
	})
}

func TestPreviewComposerSwitchesBetweenMessageAndImagePath(t *testing.T) {
	h := testHome()
	h.panel, h.previewFrom = panelPreview, panelList
	h.selectedSession().info.Transport = "acp"
	h.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	if !h.previewCompose || h.previewAttach {
		t.Fatalf("message composer state: compose=%v attach=%v", h.previewCompose, h.previewAttach)
	}
	h.Update(tea.KeyPressMsg{Code: ']', Text: "]", Mod: tea.ModCtrl})
	if h.panel != panelList || h.previewCompose {
		t.Fatalf("ctrl+] did not leave the composer: panel=%d compose=%v", h.panel, h.previewCompose)
	}
	h.panel = panelPreview
	h.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	h.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	h.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !h.previewCompose || h.previewAttach || h.previewImagePath != "p" || h.previewDraft != "x" {
		t.Fatalf("composer state: compose=%v attach=%v image=%q draft=%q", h.previewCompose, h.previewAttach, h.previewImagePath, h.previewDraft)
	}
	screen := ansi.Strip(h.viewPreview())
	for _, want := range []string{"Image: p", "> x"} {
		if !strings.Contains(screen, want) {
			t.Errorf("composer screen missing %q:\n%s", want, screen)
		}
	}
	h.openPreview(panelList)
	if h.previewCompose || h.previewAttach || h.previewDraft != "" || h.previewImagePath != "" {
		t.Fatalf("opening another session kept composer state: compose=%v attach=%v image=%q draft=%q", h.previewCompose, h.previewAttach, h.previewImagePath, h.previewDraft)
	}
}
func TestChatImagePlaceholderCanBeOpenedByClickOrKey(t *testing.T) {
	state := readFullChatFixture(t)
	h := testHome()
	h.selectedSession().info.Transport = "acp"
	h.panel, h.previewState, h.previewBodyTop = panelPreview, &state, 3
	line := -1
	for i, text := range formatChatThread(state, h.chatTextWidth(), nil) {
		if strings.Contains(text, "Image attached") {
			line = i
			break
		}
	}
	if line < 0 {
		t.Fatal("fixture image is not rendered")
	}
	if cmd := h.handleMouseClick(5, h.previewBodyTop+line); cmd == nil {
		t.Fatal("clicking the image placeholder did not open it")
	}
	h.previewHoverLine = line
	if _, cmd := h.Update(tea.KeyPressMsg{Code: 'o', Text: "o"}); cmd == nil {
		t.Fatal("open-image key did not open the hovered image")
	}
}
func TestPreviewSlashMenuCompletesAndRunsHubCommands(t *testing.T) {
	h := testHome()
	h.panel, h.previewFrom = panelPreview, panelList
	h.selectedSession().info.Transport = "acp"
	h.previewCompose, h.previewDraft = true, "/"
	if screen := ansi.Strip(h.viewPreview()); !strings.Contains(screen, "/image · Attach an image path") {
		t.Fatalf("slash menu missing image command:\n%s", screen)
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyDown, Text: "down"})
	h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if h.previewDraft != "/image" {
		t.Fatalf("tab completed %q, want /image", h.previewDraft)
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !h.previewCompose || !h.previewAttach {
		t.Fatalf("/image state: compose=%v attach=%v", h.previewCompose, h.previewAttach)
	}
	h.previewAttach, h.previewDraft = false, "/sessions"
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.panel != panelList || h.previewCompose {
		t.Fatalf("/sessions state: panel=%d compose=%v", h.panel, h.previewCompose)
	}
	h.panel, h.previewCompose, h.previewDraft = panelPreview, true, "/help"
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.panel != panelHelp {
		t.Fatalf("/help panel = %d, want help", h.panel)
	}
}
func TestMouseHoverChangesOnlyWhenTargetChanges(t *testing.T) {
	h := testHome()
	h.panel, h.previewBodyTop = panelPreview, 3
	if !h.handleMouseMotion(4, 4) {
		t.Fatal("first hover did not change the target")
	}
	if h.handleMouseMotion(5, 4) {
		t.Fatal("motion within one line changed the target")
	}
	if !h.handleMouseMotion(5, 5) {
		t.Fatal("moving to another line did not change the target")
	}
}
func TestPaletteScreenGolden(t *testing.T) {
	testScreenGoldens(t, func(h *home) { h.panel = panelPalette })
}
func TestHelpScreenKeyboardPaths(t *testing.T) {
	for _, size := range [][2]int{{120, 36}, {72, 26}, {46, 30}} {
		h := testHome()
		h.width, h.height = size[0], size[1]
		screen := ansi.Strip(h.viewHelp())
		for _, want := range []string{"Home: type a prompt", "ctrl+x a/m/e", "ctrl+c", "Images use text placeholders"} {
			if !strings.Contains(screen, want) {
				t.Errorf("%dx%d help missing %q", size[0], size[1], want)
			}
		}
		for i, row := range strings.Split(screen, "\n") {
			if width := ansi.StringWidth(row); width > size[0] {
				t.Errorf("%dx%d row %d is %d cells wide", size[0], size[1], i+1, width)
			}
		}
	}
}
func TestHelpScreenGolden(t *testing.T) {
	testScreenGoldens(t, func(h *home) {
		h.panel, h.helpFrom = panelHelp, panelHome
	})
}

func testScreenGoldens(t *testing.T, setup func(*home)) {
	t.Helper()
	for _, size := range []struct{ width, height int }{{120, 36}, {72, 26}, {46, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			h := testHome()
			h.width, h.height = size.width, size.height
			setup(h)
			screen := ansi.Strip(h.View().Content)
			for i, row := range strings.Split(screen, "\n") {
				if width := ansi.StringWidth(row); width > size.width {
					t.Errorf("row %d has width %d; screen width is %d: %q", i+1, width, size.width, row)
				}
			}
			golden.RequireEqual(t, screen)
		})
	}
}
func TestPaletteRefreshRunsReload(t *testing.T) {
	h := testHome()
	h.panel = panelPalette
	_, cmd := h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected reload command")
	}
	if _, ok := cmd().(sessionsLoadedMsg); !ok {
		t.Fatalf("cmd = %T", cmd())
	}
	if h.panel != panelList {
		t.Fatalf("panel = %d", h.panel)
	}
}

func TestHomeQuitsOnCtrlC(t *testing.T) {
	h := testHome()
	_, cmd := h.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("expected quit command")
	}
	if cmd() != tea.Quit() {
		t.Fatalf("cmd = %T", cmd())
	}
}

func TestHomePromptKeyboardSelectorChanges(t *testing.T) {
	h := testHome()
	h.newSession.directories = []string{"/work/agentpack", "/work/demo"}
	h.machineChoices = []hubMachine{{id: "peer-7", name: "workstation"}}
	h.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	h.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	h.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if h.homeDraft != "x" || h.homeField != 2 || h.newSession.machine != "peer-7" {
		t.Fatalf("home prompt/selectors: draft=%q field=%d machine=%q", h.homeDraft, h.homeField, h.newSession.machine)
	}
}

func TestHomePromptStartsSelectedChatAndSendsPrompt(t *testing.T) {
	var createBody map[string]string
	var promptBody struct {
		Text string `json:"text"`
	}
	h := conversationTestHome(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("node") != "peer-7" {
			t.Errorf("request node = %q", r.URL.Query().Get("node"))
			http.Error(w, "missing peer route", http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/api/sessions":
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(network.SessionInfo{ID: "session-new", Transport: "acp"})
		case "/api/sessions/session-new/conversation/prompt":
			if err := json.NewDecoder(r.Body).Decode(&promptBody); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	h.newSession.directories = []string{"/work/agentpack"}
	h.newSession.machine = "peer-7"
	h.machineChoices = []hubMachine{{id: "peer-7", name: "workstation"}}
	h.homeDraft = "Fix the reconnect path"
	started := h.startHomePrompt()().(sessionStartedMsg)
	if started.err != nil {
		t.Fatal(started.err)
	}
	if createBody["directory"] != "/work/agentpack" || createBody["transport"] != "acp" {
		t.Fatalf("session creation = %+v", createBody)
	}
	result := h.sendInitialPromptCmd(started.info.ID, started.machine, started.prompt)().(initialPromptSentMsg)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if promptBody.Text != "Fix the reconnect path" {
		t.Fatalf("initial prompt = %q", promptBody.Text)
	}
}

func TestSessionListShowsLoadedACPTranscriptExcerpt(t *testing.T) {
	h := conversationTestHome(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversation" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"seq":1,"kind":"agent_message_chunk","payload":{"text":"Latest agent response"}}`+"\n")
	}))
	h.panel = panelList
	msg := h.loadSessionExcerptCmd()().(sessionExcerptMsg)
	if msg.err != nil || !strings.Contains(strings.Join(msg.lines, "\n"), "Latest agent response") {
		t.Fatalf("excerpt = %+v", msg)
	}
	h.Update(msg)
	screen := ansi.Strip(h.viewSessions())
	if !strings.Contains(screen, "Recent conversation") || !strings.Contains(screen, "Latest agent response") {
		t.Fatalf("session preview missing ACP excerpt:\n%s", screen)
	}
}
func TestHoveredMessageShowsRealCopyAndForkActions(t *testing.T) {
	h := testHome()
	h.panel = panelPreview
	h.previewState = &acp.ThreadState{Items: []acp.ThreadItem{{
		Kind: "agent_message_chunk", Data: json.RawMessage(`{"text":"Full message to copy"}`),
	}}}
	session := h.selectedSession()
	session.info.Transport = "acp"
	session.info.AgentSessionID = "native-session"
	session.info.AgentCapabilities = &network.AgentCapabilities{
		SessionCapabilities: &network.AgentSessionCapabilities{Fork: &struct{}{}},
	}
	h.previewHoverLine = 0
	screen := ansi.Strip(h.viewPreview())
	if !strings.Contains(screen, "[Copy] [Fork]") || h.previewActionLine < 0 {
		t.Fatalf("hover actions missing:\n%s", screen)
	}
	copyCmd := h.handleMouseClick(h.previewActionRanges[0][0], h.previewActionLine)
	if copyCmd == nil || !strings.Contains(h.previewActionStatus, "OSC 52") {
		t.Fatalf("Copy did not dispatch clipboard command: cmd=%v status=%q", copyCmd != nil, h.previewActionStatus)
	}
}

func TestHomeListNavigationAndPanels(t *testing.T) {
	h := testHome()
	h.Update(tea.KeyPressMsg{Code: tea.KeyDown, Text: "down"})
	if h.cursor != 1 {
		t.Fatalf("cursor = %d", h.cursor)
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.panel != panelPreview {
		t.Fatalf("panel = %d", h.panel)
	}
	h.Update(tea.KeyPressMsg{Code: ']', Text: "]", Mod: tea.ModCtrl})
	if h.panel != panelHome {
		t.Fatalf("panel = %d", h.panel)
	}
	h.Update(tea.KeyPressMsg{Code: 'p', Text: "p", Mod: tea.ModCtrl})
	if h.panel != panelPalette {
		t.Fatalf("panel = %d", h.panel)
	}
}

func TestDotState(t *testing.T) {
	cases := map[string]network.SessionInfo{
		"needs_you": {ConversationState: "needs_you", State: network.TerminalState{Running: true}},
		"idle":      {ConversationState: "idle", State: network.TerminalState{Running: true}},
		"running":   {ConversationState: "working", State: network.TerminalState{Running: true}},
		"stopped":   {State: network.TerminalState{Running: false}},
	}
	for want, info := range cases {
		if got := dotState(info); got != want {
			t.Fatalf("%s: got %q", want, got)
		}
	}
}

func TestSessionsLoadedFromLoader(t *testing.T) {
	h := newHome("", func(_ context.Context, _ string) ([]listedSession, error) {
		return listedFromInfos("local", "local", []network.SessionInfo{{ID: "session-x", Harness: "codex", Directory: "/tmp/p", State: network.TerminalState{Running: true}}}), nil
	})
	msg := h.reloadCmd()().(sessionsLoadedMsg)
	if msg.err != nil || len(msg.sessions) != 1 {
		t.Fatalf("msg = %#v", msg)
	}
}

func TestGatewayLinePrefersPublicURL(t *testing.T) {
	t.Setenv("AGENTPACK_PUBLIC_URL", "https://agentpack.example")
	if got := gatewayLine(t.TempDir()); got != "https://agentpack.example" {
		t.Fatalf("gatewayLine = %q", got)
	}
}

func TestListViewContainsGroupedSessions(t *testing.T) {
	h := testHome()
	h.panel = panelList
	view := ansi.Strip(h.View().Content)
	for _, expected := range []string{"devbox", "agentpack", "Refactor hub", "Needs you", "Stopped"} {
		if !strings.Contains(view, expected) {
			t.Errorf("view missing %q:\n%s", expected, view)
		}
	}
}

func BenchmarkHubFirstFrame(b *testing.B) {
	h := testHome()
	_ = h.View().Content
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.View().Content
	}
}

func TestReloadUsesTimeout(t *testing.T) {
	h := newHome("", func(ctx context.Context, home string) ([]listedSession, error) {
		_ = home
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("session loader context has no deadline")
		} else if remaining := time.Until(deadline); remaining <= 0 || remaining > 5*time.Second {
			t.Errorf("session loader deadline remaining = %v", remaining)
		}
		return nil, context.DeadlineExceeded
	})
	msg := h.reloadCmd()().(sessionsLoadedMsg)
	if msg.err == nil {
		t.Fatal("expected load error")
	}
}

func TestHubConversationStreamAppliesIncrementalEvents(t *testing.T) {
	h := testHome()
	h.panel = panelPreview
	h.previewStreamGeneration = 1
	events := make(chan network.ConversationUpdate, 1)
	h.previewStreamEvents = events
	var reducer acp.ThreadReducer
	state := reducer.Apply(acp.LogEntry{Seq: 1, Kind: "prompt", Payload: json.RawMessage(`{"text":"hello"}`)})
	h.previewReducer = reducer
	h.previewState = &state
	h.previewLastSeq = 1

	update := conversationUpdateMsg{generation: 1, update: network.ConversationUpdate{Entry: acp.LogEntry{
		Seq: 2, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"reply"}}`),
	}}}
	if _, cmd := h.Update(update); cmd == nil {
		t.Fatal("stream update did not wait for the next event")
	} else {
		events <- network.ConversationUpdate{Entry: acp.LogEntry{Seq: 3, Kind: "turn_end"}}
		h.Update(cmd())
	}
	if h.previewLastSeq != 3 || h.previewState == nil || h.previewState.Working {
		t.Fatalf("state after turn_end = %+v seq=%d", h.previewState, h.previewLastSeq)
	}
	if len(h.previewState.Items) != 1 || chunkText(h.previewState.Items[0].Data) != "reply" {
		t.Fatalf("thread items = %+v", h.previewState.Items)
	}
}

func TestCancelPreviewStreamInvalidatesGeneration(t *testing.T) {
	h := newHome("", nil)
	ctx, cancel := context.WithCancel(context.Background())
	h.previewStreamCancel = cancel
	h.previewStreamEvents = make(chan network.ConversationUpdate)
	h.previewStreamGeneration = 4
	h.cancelPreviewStream()
	if h.previewStreamGeneration != 5 || h.previewStreamCancel != nil || h.previewStreamEvents != nil {
		t.Fatalf("stream state = generation %d, cancel=%v, events=%v", h.previewStreamGeneration, h.previewStreamCancel, h.previewStreamEvents)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("stream cancellation was not delivered")
	}
}
func TestHubMultiChoiceElicitationSubmitsOptionIDs(t *testing.T) {
	const secret = "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"
	posted := make(chan map[string]string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/session-chat/conversation/elicitation" ||
			r.URL.Query().Get("node") != "" || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("request = %s %s?%s authorization=%q", r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"))
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		posted <- payload
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer server.Close()

	homeDir := t.TempDir()
	networkDir := filepath.Join(homeDir, "network")
	if err := os.MkdirAll(networkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	node, err := json.Marshal(map[string]string{"address": strings.TrimPrefix(server.URL, "http://"), "secret": secret})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(networkDir, "node.json"), node, 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHome(homeDir, nil)
	h.applySessions(listedFromInfos("local", "local", []network.SessionInfo{{
		ID: "session-chat", Directory: "/work", Harness: "cursor", Transport: "acp",
	}}))
	h.panel = panelPreview
	h.previewElicit = pendingElicitation{
		id: "question-1", choices: []string{"Local", "Remote"},
		optionIDs: []string{"local-id", "remote-id"}, allowMultiple: true,
	}
	h.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	h.Update(tea.KeyPressMsg{Code: '2', Text: "2"})
	h.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if len(h.previewElicitSelected) != 1 || h.previewElicitSelected[0] != "remote-id" {
		t.Fatalf("selected option IDs = %v", h.previewElicitSelected)
	}
	_, cmd := h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not submit the selected options")
	}
	msg := cmd().(conversationSentMsg)
	h.Update(msg)
	body := <-posted
	if body["elicitation_id"] != "question-1" || body["value"] != `["remote-id"]` {
		t.Fatalf("elicitation body = %v", body)
	}
	if len(h.previewElicitSelected) != 0 {
		t.Fatalf("selection was not cleared after answer: %v", h.previewElicitSelected)
	}
}

func TestAvailableRecentDirectoriesDeduplicatesAndSkipsUnavailable(t *testing.T) {
	directory := t.TempDir()
	got := availableRecentDirectories([]network.RecentFolder{
		{SessionInfo: network.SessionInfo{Directory: directory}, Available: true},
		{SessionInfo: network.SessionInfo{Directory: filepath.Join(directory, ".")}, Available: true},
		{SessionInfo: network.SessionInfo{Directory: filepath.Join(directory, "missing")}, Available: false},
	})
	if len(got) != 1 || got[0] != directory {
		t.Fatalf("available folders = %v, want only %q", got, directory)
	}
}

func TestNewSessionPostsSelectedWorkspaceAndTransport(t *testing.T) {
	const secret = "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions" || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("request = %s %s authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(network.SessionInfo{
			ID: "session-new", Directory: body["directory"], Harness: body["harness"],
			Mode: body["mode"], Transport: body["transport"],
		})
	}))
	defer server.Close()
	home := t.TempDir()
	networkDir := filepath.Join(home, "network")
	if err := network.EnsurePrivateDirectory(networkDir); err != nil {
		t.Fatal(err)
	}
	node, err := json.Marshal(map[string]string{
		"address": strings.TrimPrefix(server.URL, "http://"),
		"secret":  secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(networkDir, "node.json"), node, 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHome(home, nil)
	h.newSession = newSessionForm{directories: []string{"/work/project"}, harness: 0, project: 0, view: 0}
	result := h.startNewSessionCmd()().(sessionStartedMsg)
	if result.err != nil || result.info.ID != "session-new" {
		t.Fatalf("start result = %+v", result)
	}
	want := map[string]string{
		"directory": "/work/project", "harness": "claude", "mode": "default", "transport": "acp",
	}
	if len(body) != len(want) {
		t.Fatalf("start body = %#v, want %#v", body, want)
	}
	for key, value := range want {
		if body[key] != value {
			t.Errorf("start body[%q] = %q, want %q", key, body[key], value)
		}
	}
}

func TestHubMouseOpensSessionsAndExpandsToolGroups(t *testing.T) {
	state := readFullChatFixture(t)
	h := testHome()
	h.panel = panelHome
	h.homeListStart = 0
	if cmd := h.handleMouseClick(3, 2); cmd == nil || h.panel != panelPreview {
		t.Fatalf("home click did not open selected row: panel=%d cmd=%v", h.panel, cmd != nil)
	}
	h.detachTerminal()
	if session := h.selectedSession(); session != nil {
		session.info.Transport = "acp"
	}
	h.panel = panelPreview
	h.previewState = &state
	h.previewStart, h.previewBodyTop = 0, 3
	lines := formatChatThread(state, 120, nil)
	groupLine := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "▸ Explored ") {
			groupLine = i
			break
		}
	}
	if groupLine < 0 {
		t.Fatal("fixture exploratory group missing")
	}
	h.Update(tea.MouseClickMsg{X: 4, Y: h.previewBodyTop + groupLine, Button: tea.MouseLeft})
	if !h.expandedTools[chatToolGroupKey(2)] {
		t.Fatal("mouse click did not expand the selected tool group")
	}
}
