package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	"golang.org/x/term"
)

type sessionLoader func(context.Context, string) ([]listedSession, error)

type panel int

const (
	panelHome panel = iota
	panelList
	panelPreview
	panelNew
	panelPalette
	panelHelp
	panelConfig
)

type hubConfigChoice struct {
	Value string `json:"value"`
	Name  string `json:"name"`
}

type hubConfigOption struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Category     string            `json:"category"`
	CurrentValue string            `json:"currentValue"`
	Options      []hubConfigChoice `json:"options"`
}

type sessionExcerptMsg struct {
	sessionID string
	machine   string
	lines     []string
	err       error
}

type configSetMsg struct {
	sessionID string
	machine   string
	category  string
	key       string
	value     string
	err       error
}
type forkedSessionMsg struct {
	info    network.SessionInfo
	machine string
	err     error
}

type previewLoadedMsg struct {
	lines            []string
	state            *acp.ThreadState
	err              error
	perm             pendingPermission
	elicit           pendingElicitation
	streamEvents     <-chan network.ConversationUpdate
	streamCancel     context.CancelFunc
	streamGeneration uint64
	reducer          acp.ThreadReducer
	lastSeq          int64
	truncated        bool
	streamErr        error
	configOptions    []hubConfigOption
	configErr        error
	hasConfig        bool
	clearComposer    bool
	composeAgain     bool
	continueStream   bool
}

type conversationUpdateMsg struct {
	generation uint64
	update     network.ConversationUpdate
	closed     bool
}

type home struct {
	agentpackHome string
	load          sessionLoader
	machineName   string

	width, height            int
	panel                    panel
	cursor                   int
	rows                     []listRow
	gatewayLine              string
	loadErr                  error
	homeDraft                string
	homePromptErr            error
	homeField                int
	homePromptLine           int
	homeSelectorLines        [4]int
	homeSelectorBounds       [4][2]int
	previewLines             []string
	previewState             *acp.ThreadState
	terminal                 *terminalPane
	terminalGeneration       uint64
	previewErr               error
	previewImageErr          error
	previewPerm              pendingPermission
	previewElicit            pendingElicitation
	previewElicitSelected    []string
	previewCompose           bool
	previewDraft             string
	previewAttach            bool
	previewImagePath         string
	previewSlash             int
	previewTail              int
	previewStreamCancel      context.CancelFunc
	previewStreamEvents      <-chan network.ConversationUpdate
	previewStreamGeneration  uint64
	previewReducer           acp.ThreadReducer
	previewLastSeq           int64
	previewTruncated         bool
	previewStreamErr         error
	previewActionErr         error
	previewActionStatus      string
	previewActionLine        int
	previewActionRanges      [2][2]int
	previewSide              bool
	previewConfigOptions     []hubConfigOption
	previewConfigErr         error
	previewConfigLoaded      bool
	previewConfigLine        int
	previewConfigRanges      [3][2]int
	configCategory           string
	configCursor             int
	configFrom               panel
	configPending            bool
	previewFrom, paletteFrom panel
	helpFrom                 panel
	expandedTools            map[string]bool
	paletteCursor            int
	machineChoices           []hubMachine
	newSession               newSessionForm
	newFrom                  panel
	pendingSession           string
	leader                   bool
	homeListStart            int
	homeLines                []int // h.rows index of each home list line
	listScrollStart          int
	sessionTab               int
	sessionFilter            string
	sessionFilterFocused     bool
	sessionActionID          string
	sessionActionPending     bool
	sessionActionErr         error
	sessionExcerptID         string
	sessionExcerptMachine    string
	sessionExcerpt           []string
	sessionExcerptLoading    bool
	sessionExcerptErr        error
	previewStart             int
	previewBodyTop           int
	hoverRow                 int
	previewHoverLine         int
}

var paletteCommands = []struct{ label, hint string }{
	{"Refresh sessions", ""},
	{"Open preview", "enter"},
	{"Quit", "q"},
	{"New session", "ctrl+x n"},
	{"Sessions", "ctrl+x l"},
	{"Home", "esc"},
	{"Help", "?"},
}

type hubChatSlashCommand struct {
	name string
	help string
}

var hubChatSlashCommands = [...]hubChatSlashCommand{
	{name: "/help", help: "Show key help"},
	{name: "/image", help: "Attach an image path"},
	{name: "/reload", help: "Reload this conversation"},
	{name: "/sessions", help: "Return to sessions"},
}

func matchingHubChatSlashCommands(draft string) ([len(hubChatSlashCommands)]hubChatSlashCommand, int) {
	var matches [len(hubChatSlashCommands)]hubChatSlashCommand
	prefix := strings.TrimSpace(draft)
	if prefix == "" || prefix[0] != '/' || strings.ContainsAny(prefix, " \t\r\n") {
		return matches, 0
	}
	count := 0
	for _, command := range hubChatSlashCommands {
		if strings.HasPrefix(command.name, prefix) {
			matches[count] = command
			count++
		}
	}
	return matches, count
}

type sessionsLoadedMsg struct {
	sessions []listedSession
	err      error
	gateway  string
}

func Run() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("hub: TUI requires an interactive terminal")
	}
	homeDir, err := paths.UserAgentpackHome()
	if err != nil {
		return err
	}
	m := newMux(homeDir, loadHubSessions)
	defer m.home.cancelPreviewStream()
	_, err = tea.NewProgram(m).Run()
	m.home.detachTerminal()
	return err
}

func newHome(agentpackHome string, load sessionLoader) *home {
	name, _ := os.Hostname()
	if name == "" {
		name = "local"
	}
	return &home{
		agentpackHome:     agentpackHome,
		load:              load,
		machineName:       name,
		width:             120,
		height:            36,
		panel:             panelHome,
		expandedTools:     map[string]bool{},
		previewSide:       true,
		hoverRow:          -1,
		previewHoverLine:  -1,
		previewActionLine: -1,
		gatewayLine:       gatewayLine(agentpackHome),
		newSession:        newSessionForm{machine: "local", loading: true},
	}
}

func gatewayLine(home string) string {
	if value := strings.TrimSpace(os.Getenv("AGENTPACK_PUBLIC_URL")); value != "" {
		return value
	}
	if value, err := network.GatewayURL(home); err == nil {
		return value
	}
	return "gateway not running — run agentpack gateway start"
}

func (h *home) Init() tea.Cmd {
	return tea.Batch(h.reloadCmd(), h.loadRecentFoldersCmd())
}

func (h *home) reloadCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sessions, err := h.load(ctx, h.agentpackHome)
		return sessionsLoadedMsg{sessions: sessions, err: err, gateway: gatewayLine(h.agentpackHome)}
	}
}

func recentConversationExcerpt(entries []acp.LogEntry) []string {
	var reducer acp.ThreadReducer
	var state acp.ThreadState
	for _, entry := range entries {
		state = reducer.Apply(entry)
	}
	lines := formatChatThread(state, 64, nil)
	content := lines[:0]
	for _, line := range lines {
		if line == "◆ Needs you" || line == "… turn running" {
			continue
		}
		content = append(content, line)
	}
	if len(content) > 3 {
		content = content[len(content)-3:]
	}
	return content
}

func (h *home) loadSessionExcerptCmd() tea.Cmd {
	session := h.selectedSession()
	if session == nil || session.info.Transport != "acp" {
		h.sessionExcerptID, h.sessionExcerptMachine = "", ""
		h.sessionExcerpt, h.sessionExcerptErr = nil, nil
		h.sessionExcerptLoading = false
		return nil
	}
	if h.sessionExcerptID == session.info.ID && h.sessionExcerptMachine == session.machine &&
		(h.sessionExcerptLoading || h.sessionExcerptErr == nil) {
		return nil
	}
	h.sessionExcerptID, h.sessionExcerptMachine = session.info.ID, session.machine
	h.sessionExcerpt, h.sessionExcerptErr = nil, nil
	h.sessionExcerptLoading = true
	home, sessionID, machine := h.agentpackHome, session.info.ID, session.machine
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		entries, _, err := network.ConversationSnapshot(ctx, home, sessionID, machine, 0)
		return sessionExcerptMsg{
			sessionID: sessionID,
			machine:   machine,
			lines:     recentConversationExcerpt(entries),
			err:       err,
		}
	}
}

func (h *home) applySessions(sessions []listedSession) {
	h.rows = flattenGroups(groupSessionsMachine(sessions))
	if h.cursor >= h.sessionCount() {
		if h.sessionCount() == 0 {
			h.cursor = 0
		} else {
			h.cursor = h.sessionCount() - 1
		}
	}
}

func (h *home) sessionCount() int {
	n := 0
	for _, row := range h.rows {
		if row.kind == rowSession {
			n++
		}
	}
	return n
}

func (h *home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h.width, h.height = msg.Width, msg.Height
	case sessionActionResultMsg:
		if h.sessionActionID == msg.sessionID {
			h.sessionActionPending = false
			h.sessionActionErr = msg.err
			if msg.err == nil {
				return h, h.reloadCmd()
			}
		}
	case terminalConnectedMsg:
		return h, h.installTerminal(msg)
	case terminalFrameMsg:
		return h, h.handleTerminalFrame(msg)
	case terminalInputMsg:
		return h, h.handleTerminalInput(msg)
	case terminalWriteMsg:
		return h, h.handleTerminalWrite(msg)
	case terminalRetryMsg:
		if h.terminal != nil && h.terminal.generation == msg.generation && h.terminal.conn == nil {
			return h, h.reconnectTerminalCmd()
		}
	case sessionsLoadedMsg:
		h.loadErr = msg.err
		h.gatewayLine = msg.gateway
		if msg.err == nil {
			h.applySessions(msg.sessions)
		}
		if h.pendingSession != "" {
			if msg.err != nil {
				h.newSession.err = fmt.Errorf("session started, but the list could not be refreshed: %w", msg.err)
				h.pendingSession = ""
				return h, nil
			}
			if h.selectSessionID(h.pendingSession) {
				h.pendingSession = ""
				return h, h.openPreview(panelList)
			}
			h.newSession.err = fmt.Errorf("the gateway started the session, but it is not in the local list")
			h.pendingSession = ""
		}
		if h.panel == panelList {
			return h, h.loadSessionExcerptCmd()
		}
	case recentFoldersLoadedMsg:
		h.newSession.loading = false
		h.newSession.directories = msg.directories
		h.machineChoices = msg.machines
		h.newSession.err = msg.err
		if h.newSession.project >= len(msg.directories) {
			h.newSession.project = 0
		}
	case sessionExcerptMsg:
		if h.sessionExcerptID == msg.sessionID && h.sessionExcerptMachine == msg.machine {
			h.sessionExcerptLoading = false
			h.sessionExcerpt, h.sessionExcerptErr = msg.lines, msg.err
		}
	case initialPromptSentMsg:
		if msg.err != nil {
			h.sessionActionID, h.sessionActionErr = msg.sessionID, msg.err
		}
	case sessionStartedMsg:
		h.newSession.starting = false
		if msg.err != nil {
			if h.newFrom == panelHome {
				h.homePromptErr = msg.err
			} else {
				h.newSession.err = msg.err
			}
			return h, nil
		}
		if msg.info.ID == "" {
			h.newSession.err = fmt.Errorf("gateway returned an empty session ID")
			return h, nil
		}
		if msg.prompt != "" {
			h.homeDraft = ""
			h.homePromptErr = nil
		}
		h.pendingSession = msg.info.ID
		h.panel = panelList
		if msg.prompt != "" && msg.transport == "acp" {
			return h, tea.Batch(h.reloadCmd(), h.sendInitialPromptCmd(msg.info.ID, msg.machine, msg.prompt))
		}
		return h, h.reloadCmd()
	case previewLoadedMsg:
		if msg.streamGeneration != 0 && msg.streamGeneration != h.previewStreamGeneration {
			if msg.streamCancel != nil {
				msg.streamCancel()
			}
			return h, nil
		}
		h.previewLines = msg.lines
		h.previewState = msg.state
		h.previewErr = msg.err
		h.previewStreamErr = msg.streamErr
		h.previewTruncated = msg.truncated
		h.previewActionErr = nil
		h.previewImageErr = nil
		h.previewPerm = msg.perm
		if msg.hasConfig {
			h.previewConfigOptions = msg.configOptions
			h.previewConfigErr = msg.configErr
			h.previewConfigLoaded = true
		}
		if h.previewElicit.id != msg.elicit.id {
			h.previewElicitSelected = nil
		}
		h.previewElicit = msg.elicit
		h.previewTail = 0
		if msg.clearComposer {
			h.previewCompose, h.previewAttach = false, false
			h.previewDraft, h.previewImagePath = "", ""
		} else if msg.composeAgain {
			h.previewCompose = true
		}
		if msg.streamCancel != nil {
			h.previewStreamCancel = msg.streamCancel
			h.previewStreamEvents = msg.streamEvents
			h.previewStreamGeneration = msg.streamGeneration
			h.previewReducer = msg.reducer
			h.previewLastSeq = msg.lastSeq
			return h, h.nextConversationUpdateCmd(msg.streamGeneration, msg.streamEvents)
		}
		if msg.continueStream {
			h.previewReducer = msg.reducer
			h.previewLastSeq = msg.lastSeq
			return h, h.nextConversationUpdateCmd(h.previewStreamGeneration, h.previewStreamEvents)
		}
	case conversationUpdateMsg:
		if msg.generation != h.previewStreamGeneration {
			return h, nil
		}
		if msg.closed {
			h.previewStreamErr = fmt.Errorf("conversation stream closed")
			h.cancelPreviewStream()
			return h, nil
		}
		if msg.update.Err != nil {
			h.previewStreamErr = msg.update.Err
			h.cancelPreviewStream()
			return h, nil
		}
		if msg.update.Truncated {
			h.previewTruncated = true
			return h, h.refreshPreviewSnapshotCmd()
		}
		if msg.update.Entry.Seq > h.previewLastSeq+1 {
			h.previewTruncated = true
			return h, h.refreshPreviewSnapshotCmd()
		}
		if msg.update.Entry.Seq > h.previewLastSeq {
			state := h.previewReducer.Apply(msg.update.Entry)
			h.previewState = &state
			h.previewLastSeq = msg.update.Entry.Seq
			perm, elicit := previewPending(state)
			if h.previewElicit.id != elicit.id {
				h.previewElicitSelected = nil
			}
			h.previewPerm, h.previewElicit = perm, elicit
		}
		h.previewStreamErr = nil
		return h, h.nextConversationUpdateCmd(msg.generation, h.previewStreamEvents)
	case conversationSentMsg:
		if msg.sessionID != "" {
			session := h.selectedSession()
			if session == nil || session.info.ID != msg.sessionID || session.machine != msg.machine {
				return h, nil
			}
		}
		h.previewActionErr = msg.err
		if msg.err == nil {
			h.previewElicitSelected = nil
		}
		if msg.clearComposer {
			h.previewCompose, h.previewAttach = false, false
			h.previewDraft, h.previewImagePath = "", ""
		} else if msg.composeAgain {
			h.previewCompose = true
		}
		return h, nil
	case configSetMsg:
		h.configPending = false
		session := h.selectedSession()
		if session == nil || session.info.ID != msg.sessionID || session.machine != msg.machine {
			return h, nil
		}
		if msg.err != nil {
			h.previewActionErr = msg.err
			return h, nil
		}
		for i := range h.previewConfigOptions {
			if h.previewConfigOptions[i].ID == msg.key && h.previewConfigOptions[i].Category == msg.category {
				h.previewConfigOptions[i].CurrentValue = msg.value
			}
		}
		h.panel = h.configFrom
	case forkedSessionMsg:
		if msg.err != nil {
			h.previewActionStatus = ""
			h.previewActionErr = msg.err
			return h, nil
		}
		h.previewActionStatus = ""
		h.pendingSession = msg.info.ID
		h.panel = panelList
		return h, h.reloadCmd()
	case chatImageActionMsg:
		h.previewImageErr = msg.err
	case tea.MouseClickMsg:
		if h.terminalActive() {
			h.sendTerminalMouseAt(msg)
			return h, nil
		}
		if msg.Button == tea.MouseLeft {
			return h, h.handleMouseClick(msg.X, msg.Y)
		}
	case tea.MouseReleaseMsg:
		if h.terminalActive() {
			h.sendTerminalMouseAt(msg)
		}
	case tea.MouseWheelMsg:
		if h.terminalActive() {
			h.sendTerminalMouseAt(msg)
		} else {
			h.handleMouseWheel(msg)
			if h.panel == panelList {
				return h, h.loadSessionExcerptCmd()
			}
		}
	case tea.MouseMotionMsg:
		if h.terminalActive() {
			h.sendTerminalMouseAt(msg)
		} else if !h.handleMouseMotion(msg.X, msg.Y) {
			return h, nil
		}
	case tea.PasteMsg:
		if h.terminalActive() {
			h.sendTerminalPaste(msg.Content)
		} else if h.panel == panelPreview && h.previewCompose {
			if h.previewAttach {
				h.previewImagePath += msg.Content
			} else {
				h.previewDraft += msg.Content
			}
			h.previewSlash = 0
		} else if h.panel == panelHome {
			h.homeDraft += acp.StripControls(msg.Content)
			h.homePromptErr = nil
		} else if h.panel == panelList && h.sessionFilterFocused {
			h.sessionFilter += cleanSessionFilter(msg.Content)
			h.listScrollStart = 0
			h.ensureSessionCursor()
			return h, h.loadSessionExcerptCmd()
		}
	case tea.FocusMsg:
		h.sendTerminalFocus(true)
	case tea.BlurMsg:
		h.sendTerminalFocus(false)
	case tea.KeyPressMsg:
		key := msg.String()
		if msg.Mod&tea.ModCtrl != 0 {
			switch msg.Code {
			case 'c':
				key = "ctrl+c"
			case 'p':
				key = "ctrl+p"
			case 'r':
				key = "ctrl+r"
			case 'x':
				key = "ctrl+x"
			case ']':
				key = "ctrl+]"
			}
		}
		if h.terminalActive() {
			switch key {
			case "ctrl+]":
				h.detachTerminal()
				h.panel = h.previewFrom
				return h, nil
			case "ctrl+r":
				return h, h.reconnectTerminalCmd()
			case "alt+left":
				h.panTerminal(-4, 0)
				return h, nil
			case "alt+right":
				h.panTerminal(4, 0)
				return h, nil
			case "alt+up":
				h.panTerminal(0, -2)
				return h, nil
			case "alt+down":
				h.panTerminal(0, 2)
				return h, nil
			}
			h.sendTerminalKey(msg)
			return h, nil
		}
		if h.leader {
			h.leader = false
			switch key {
			case "l":
				h.cancelPreviewStream()
				h.panel = panelList
				return h, h.loadSessionExcerptCmd()
			case "n":
				return h, h.openNewSession()
			case "h":
				h.cancelPreviewStream()
				h.panel = panelHome
			case "p":
				h.paletteFrom = h.panel
				h.panel, h.paletteCursor = panelPalette, 0
			case "m":
				if h.panel == panelPreview {
					return h, h.openConfigPicker("model")
				}
			case "a":
				if h.panel == panelPreview {
					return h, h.openConfigPicker("mode")
				}
			case "e":
				if h.panel == panelPreview {
					return h, h.openConfigPicker("thought_level")
				}
			case "?":
				h.helpFrom, h.panel = h.panel, panelHelp
			case "q":
				return h, tea.Quit
			case "b":
				h.previewSide = !h.previewSide
			}
			return h, nil
		}
		switch h.panel {
		case panelPreview:
			if h.previewCompose {
				switch key {
				case "esc":
					h.previewCompose, h.previewAttach = false, false
				case "ctrl+]":
					h.previewCompose, h.previewAttach = false, false
					h.panel = h.previewFrom
				case "enter":
					if !h.previewAttach {
						matches, count := matchingHubChatSlashCommands(h.previewDraft)
						if count > 0 {
							return h.runHubChatSlashCommand(matches[min(h.previewSlash, count-1)].name)
						}
					}
					text := strings.TrimSpace(h.previewDraft)
					imagePath := strings.TrimSpace(h.previewImagePath)
					if text != "" || imagePath != "" {
						h.previewCompose = false
						return h, h.sendPreviewPromptCmd(text, imagePath)
					}
				case "tab":
					if !h.previewAttach && strings.HasPrefix(strings.TrimSpace(h.previewDraft), "/") {
						matches, count := matchingHubChatSlashCommands(h.previewDraft)
						if count > 0 {
							h.previewDraft = matches[min(h.previewSlash, count-1)].name
							h.previewSlash = 0
						}
					} else {
						h.previewAttach = !h.previewAttach
						h.previewSlash = 0
					}
				case "up", "down":
					if !h.previewAttach {
						_, count := matchingHubChatSlashCommands(h.previewDraft)
						if count > 0 {
							if key == "up" && h.previewSlash > 0 {
								h.previewSlash--
							} else if key == "down" && h.previewSlash < count-1 {
								h.previewSlash++
							}
							return h, nil
						}
					}
				case "backspace":
					if h.previewAttach {
						h.previewImagePath = dropLastRune(h.previewImagePath)
					} else {
						h.previewDraft = dropLastRune(h.previewDraft)
						h.previewSlash = 0
					}
				default:
					if typedText(msg) {
						if h.previewAttach {
							h.previewImagePath += msg.Text
						} else {
							h.previewDraft += msg.Text
							h.previewSlash = 0
						}
					}
				}
				return h, nil
			}
			switch key {
			case "esc", "ctrl+]":
				h.cancelPreviewStream()
				h.panel = h.previewFrom
			case "q", "ctrl+c":
				return h, tea.Quit
			case "ctrl+r":
				h.previewLines, h.previewState = nil, nil
				return h, h.loadPreviewCmd()
			case "ctrl+p":
				h.paletteFrom, h.panel, h.paletteCursor = panelPreview, panelPalette, 0
			case "ctrl+x":
				h.leader = true
			case "a", "A", "r":
				if h.previewPerm.requestID != "" {
					index := 0
					if key == "A" {
						index = 1
					}
					if key == "r" {
						index = 2
					}
					return h, h.answerPermissionViaGateway(permissionOptionID(h.previewPerm.optionIDs, index))
				} else if key == "a" {
					if session := h.selectedSession(); session != nil && session.info.Transport == "acp" && h.previewElicit.id == "" {
						h.previewCompose, h.previewAttach = true, true
						h.previewErr = nil
					}
				}
			case "1", "2", "3":
				if h.previewPerm.requestID == "" && h.previewElicit.id != "" {
					index := int(msg.Code - '1')
					if value := elicitationChoice(h.previewElicit, index); value != "" {
						if h.previewElicit.allowMultiple {
							h.previewElicitSelected = toggleElicitationSelection(h.previewElicitSelected, value)
							return h, nil
						}
						return h, h.answerElicitationViaGateway(value)
					}
				}
			case "j":
				if h.previewTail > 0 {
					h.previewTail--
				}
			case "k":
				h.previewTail++
			case "e", "enter":
				if key == "enter" && h.previewElicit.id != "" && h.previewElicit.allowMultiple && len(h.previewElicitSelected) > 0 {
					value, err := json.Marshal(h.previewElicitSelected)
					if err != nil {
						h.previewActionErr = err
						return h, nil
					}
					return h, h.answerElicitationViaGateway(string(value))
				}
				if key == "enter" && h.previewState != nil {
					image := chatImageAtLine(*h.previewState, h.chatTextWidth(), h.previewHoverLine, h.expandedTools)
					if image.SHA != "" {
						h.previewImageErr = nil
						return h, h.openChatImageCmd(image)
					}
					if item := chatToolGroupAtLine(*h.previewState, h.chatTextWidth(), h.previewHoverLine, h.expandedTools); item != "" {
						h.expandedTools[item] = !chatExpandableOpen(item, h.expandedTools)
						return h, nil
					}
				}
				h.toggleFirstToolGroup()
			case "o":
				if h.previewState != nil {
					image := chatImageAtLine(*h.previewState, h.chatTextWidth(), h.previewHoverLine, h.expandedTools)
					if image.SHA != "" {
						h.previewImageErr = nil
						return h, h.openChatImageCmd(image)
					}
				}
			case "c":
				return h, h.copyHoveredMessage()
			case "f":
				if cmd := h.forkHoveredMessageCmd(); cmd != nil {
					return h, cmd
				}
			case "i":
				if session := h.selectedSession(); session != nil && session.info.Transport == "acp" && h.previewPerm.requestID == "" && h.previewElicit.id == "" {
					h.previewCompose, h.previewAttach = true, false
					h.previewErr = nil
				}
			}
			return h, nil
		case panelPalette:
			switch key {
			case "esc":
				h.panel = h.paletteFrom
			case "ctrl+c":
				return h, tea.Quit
			case "q":
				return h, tea.Quit
			case "j", "down":
				if h.paletteCursor < len(paletteCommands)-1 {
					h.paletteCursor++
				}
			case "k", "up":
				if h.paletteCursor > 0 {
					h.paletteCursor--
				}
			case "enter":
				return h.paletteActivate()
			}
			return h, nil
		case panelConfig:
			switch key {
			case "esc", "q":
				h.configPending = false
				h.panel = h.configFrom
			case "ctrl+c":
				return h, tea.Quit
			case "up", "k":
				if h.configCursor > 0 {
					h.configCursor--
				}
			case "down", "j":
				if option := configOptionFor(h.previewConfigOptions, h.configCategory); option != nil && h.configCursor < len(option.Options)-1 {
					h.configCursor++
				}
			case "enter":
				if !h.configPending {
					if cmd := h.setConfigChoiceCmd(); cmd != nil {
						h.configPending = true
						return h, cmd
					}
				}
			}
			return h, nil
		case panelNew:
			if key == "?" {
				h.helpFrom, h.panel = panelNew, panelHelp
				return h, nil
			}
			return h.updateNewSession(msg)
		case panelHelp:
			if key == "esc" || key == "q" || key == "?" {
				h.panel = h.helpFrom
			}
			return h, nil
		case panelHome:
			switch key {
			case "ctrl+c":
				return h, tea.Quit
			case "esc":
				h.homeDraft = ""
				h.homePromptErr = nil
			case "enter":
				if strings.TrimSpace(h.homeDraft) != "" {
					if h.newSession.loading || len(h.newSession.directories) == 0 {
						h.homePromptErr = fmt.Errorf("no recent project is available; add or launch from a project folder first")
						return h, nil
					}
					h.newFrom = panelHome
					return h, h.startHomePrompt()
				}
				if h.sessionCount() > 0 {
					return h, h.openPreview(panelHome)
				}
			case "tab":
				h.homeField = (h.homeField + 1) % 4
			case "shift+tab":
				h.homeField = (h.homeField + 3) % 4
			case "ctrl+x":
				h.leader = true
			// h and l are letters of the prompt here, not selector keys.
			case "left":
				h.changeHomeSelector(-1)
			case "right":
				h.changeHomeSelector(1)
			case "up":
				if h.cursor > 0 {
					h.cursor--
				}
			case "down":
				if h.cursor < h.sessionCount()-1 {
					h.cursor++
				}
			case "ctrl+r":
				return h, h.reloadCmd()
			case "ctrl+p":
				h.paletteFrom, h.panel, h.paletteCursor = panelHome, panelPalette, 0
			case "?":
				if h.homeDraft == "" {
					h.helpFrom, h.panel = panelHome, panelHelp
					return h, nil
				}
				h.homeDraft += key
			case "backspace":
				h.homeDraft = dropLastRune(h.homeDraft)
			default:
				if typedText(msg) {
					h.homeDraft += acp.StripControls(msg.Text)
					h.homePromptErr = nil
				}
			}
			return h, nil
		case panelList:
			if h.panel == panelList && h.sessionFilterFocused {
				switch key {
				case "esc":
					h.sessionFilterFocused = false
					if h.sessionFilter != "" {
						h.sessionFilter = ""
						h.listScrollStart = 0
						h.ensureSessionCursor()
					}
					return h, h.loadSessionExcerptCmd()
				case "enter":
					h.sessionFilterFocused = false
					return h, nil
				case "backspace":
					filter := []rune(h.sessionFilter)
					if len(filter) > 0 {
						h.sessionFilter = string(filter[:len(filter)-1])
						h.listScrollStart = 0
						h.ensureSessionCursor()
					}
					return h, h.loadSessionExcerptCmd()
				}
				if typedText(msg) {
					h.sessionFilter += cleanSessionFilter(msg.Text)
					h.listScrollStart = 0
					h.ensureSessionCursor()
					return h, h.loadSessionExcerptCmd()
				}
			}
			if h.panel == panelList {
				switch key {
				case "1", "2", "3", "4":
					h.sessionTab = int(key[0] - '1')
					h.sessionFilterFocused = false
					h.listScrollStart = 0
					h.ensureSessionCursor()
					return h, h.loadSessionExcerptCmd()
				case "tab":
					h.sessionTab = (h.sessionTab + 1) % len(sessionTabs)
					h.sessionFilterFocused = false
					h.listScrollStart = 0
					h.ensureSessionCursor()
					return h, h.loadSessionExcerptCmd()
				case "shift+tab":
					h.sessionTab = (h.sessionTab + len(sessionTabs) - 1) % len(sessionTabs)
					h.sessionFilterFocused = false
					h.listScrollStart = 0
					h.ensureSessionCursor()
					return h, h.loadSessionExcerptCmd()
				case "/":
					h.sessionFilterFocused = true
					return h, nil
				}
			}
			switch key {
			case "q", "ctrl+c":
				return h, tea.Quit
			case "esc":
				if h.panel == panelList {
					h.panel = panelHome
				}
			case "j", "down":
				h.moveSessionCursor(1)
				return h, h.loadSessionExcerptCmd()
			case "k", "up":
				h.moveSessionCursor(-1)
				return h, h.loadSessionExcerptCmd()
			case "enter":
				if h.panel == panelList {
					if len(h.visibleSessionIndices()) > 0 {
						return h, h.openPreview(h.panel)
					}
				} else if h.sessionCount() > 0 {
					return h, h.openPreview(h.panel)
				}
			case "n":
				return h, h.openNewSession()
			case "l":
				h.panel = panelList
				return h, h.loadSessionExcerptCmd()
			case "?":
				h.helpFrom, h.panel = h.panel, panelHelp
			case "ctrl+r":
				return h, h.reloadCmd()
			case "ctrl+p":
				h.paletteFrom, h.panel, h.paletteCursor = h.panel, panelPalette, 0
			case "ctrl+x":
				h.leader = true
			}
			return h, nil
		}
	}
	return h, nil
}

func (h *home) openPreview(from panel) tea.Cmd {
	h.detachTerminal()
	h.previewFrom = from
	h.panel = panelPreview
	h.previewLines, h.previewState = nil, nil
	h.previewErr = nil
	h.previewStreamErr = nil
	h.previewActionErr = nil
	h.previewTruncated = false
	h.previewImageErr = nil
	h.previewConfigOptions = nil
	h.previewConfigErr = nil
	h.previewConfigLoaded = false
	h.previewConfigLine = -1
	h.configPending = false
	h.previewPerm = pendingPermission{}
	h.previewElicit = pendingElicitation{}
	h.previewElicitSelected = nil
	h.previewCompose, h.previewAttach = false, false
	h.previewDraft, h.previewImagePath = "", ""
	h.previewSlash = 0
	clear(h.expandedTools)
	if session := h.selectedSession(); session != nil && session.info.Transport != "acp" {
		h.cancelPreviewStream()
		generation := h.terminalGeneration
		h.terminal = &terminalPane{
			sessionID: session.info.ID, generation: generation, state: session.info.State,
			panY:     max(0, int(session.info.State.Rows)-max(1, h.height-5)),
			emulator: newTerminalEmulator(session.info.State),
		}
		return h.connectTerminalCmd(session.info.ID, session.info.State, 0, generation)
	}
	return h.loadPreviewCmd()
}
func (h *home) runHubChatSlashCommand(name string) (tea.Model, tea.Cmd) {
	switch name {
	case "/help":
		h.previewCompose = false
		h.previewDraft, h.previewSlash = "", 0
		h.helpFrom, h.panel = panelPreview, panelHelp
	case "/image":
		h.previewAttach = true
		h.previewDraft, h.previewSlash = "", 0
	case "/reload":
		h.previewCompose, h.previewAttach = false, false
		h.previewDraft, h.previewImagePath = "", ""
		h.previewLines, h.previewState = nil, nil
		h.previewErr = nil
		return h, h.loadPreviewCmd()
	case "/sessions":
		h.cancelPreviewStream()
		h.previewCompose, h.previewAttach = false, false
		h.previewDraft, h.previewSlash = "", 0
		h.panel = h.previewFrom
	}
	return h, nil
}

func (h *home) selectSessionID(id string) bool {
	index := 0
	for _, row := range h.rows {
		if row.kind != rowSession {
			continue
		}
		if row.session.info.ID == id {
			h.cursor = index
			return true
		}
		index++
	}
	return false
}

func (h *home) toggleFirstToolGroup() {
	if h.previewState == nil {
		return
	}
	width := max(1, h.width-4)
	if key := chatFirstExpandable(*h.previewState, width, h.expandedTools); key != "" {
		h.expandedTools[key] = !chatExpandableOpen(key, h.expandedTools)
	}
}
func (h *home) copyHoveredMessage() tea.Cmd {
	if h.previewState == nil {
		return nil
	}
	text := chatMessageAtLine(*h.previewState, h.chatTextWidth(), h.previewHoverLine, h.expandedTools)
	if text == "" {
		return nil
	}
	h.previewActionErr = nil
	h.previewActionStatus = "Copy sent via OSC 52; terminal clipboard support is required"
	return copyChatMessage(text)
}

func (h *home) forkHoveredMessageCmd() tea.Cmd {
	if h.previewState == nil || !sessionCanFork(h.selectedSession()) ||
		chatMessageAtLine(*h.previewState, h.chatTextWidth(), h.previewHoverLine, h.expandedTools) == "" {
		return nil
	}
	h.previewActionErr = nil
	h.previewActionStatus = "Forking session…"
	return h.forkSelectedSessionCmd()
}

func (h *home) selectedSession() *listedSession {
	idx := 0
	for i := range h.rows {
		if h.rows[i].kind != rowSession {
			continue
		}
		if idx == h.cursor {
			return h.rows[i].session
		}
		idx++
	}
	return nil
}

func (h *home) View() tea.View {
	var body string
	switch h.panel {
	case panelList:
		body = h.viewSessions()
	case panelPreview:
		body = h.viewPreview()
	case panelNew:
		body = h.viewNewSession()
	case panelPalette:
		body = h.viewPalette()
	case panelHelp:
		body = h.viewHelp()
	case panelConfig:
		body = h.viewConfigPicker()
	default:
		body = h.viewHome()
	}
	v := tea.NewView(body)
	v.MouseMode = tea.MouseModeAllMotion
	v.ReportFocus = true
	v.AltScreen = true
	return v
}

func (h *home) cancelPreviewStream() {
	h.previewStreamGeneration++
	if h.previewStreamCancel != nil {
		h.previewStreamCancel()
	}
	h.previewStreamCancel = nil
	h.previewStreamEvents = nil
}

func (h *home) nextConversationUpdateCmd(generation uint64, updates <-chan network.ConversationUpdate) tea.Cmd {
	if updates == nil {
		return nil
	}
	return func() tea.Msg {
		update, ok := <-updates
		return conversationUpdateMsg{generation: generation, update: update, closed: !ok}
	}
}
func (h *home) loadPreviewCmd() tea.Cmd {
	h.cancelPreviewStream()
	h.previewStreamErr = nil
	h.previewActionErr = nil
	h.previewTruncated = false
	home := h.agentpackHome
	selected := h.selectedSession()
	var session *listedSession
	if selected != nil {
		copy := *selected
		session = &copy
	}
	width := h.width
	generation := h.previewStreamGeneration
	return func() tea.Msg {
		if session == nil {
			return previewLoadedMsg{lines: []string{"(no session)"}, streamGeneration: generation}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		entries, truncated, err := network.ConversationSnapshot(ctx, home, session.info.ID, session.machine, 0)
		if err != nil {
			cancel()
			return previewLoadedMsg{err: err, lines: []string{"Could not load conversation."}, streamGeneration: generation, configErr: err, hasConfig: true}
		}
		configOptions, configErr := conversationConfig(ctx, home, session.info.ID, session.machine)
		cancel()
		var reducer acp.ThreadReducer
		var state acp.ThreadState
		var lastSeq int64
		for _, entry := range entries {
			state = reducer.Apply(entry)
			if entry.Seq > lastSeq {
				lastSeq = entry.Seq
			}
		}
		perm, elicit := previewPending(state)
		updates, streamCancel, streamErr := network.SubscribeConversation(context.Background(), home, session.info.ID, session.machine, lastSeq)
		return previewLoadedMsg{
			lines:            formatChatThread(state, width, nil),
			state:            &state,
			perm:             perm,
			elicit:           elicit,
			streamEvents:     updates,
			streamCancel:     streamCancel,
			streamGeneration: generation,
			reducer:          reducer,
			lastSeq:          lastSeq,
			truncated:        truncated,
			streamErr:        streamErr,
			configOptions:    configOptions,
			configErr:        configErr,
			hasConfig:        true,
		}
	}
}

func (h *home) refreshPreviewSnapshotCmd() tea.Cmd {
	home := h.agentpackHome
	selected := h.selectedSession()
	var session *listedSession
	if selected != nil {
		copy := *selected
		session = &copy
	}
	var previousState *acp.ThreadState
	if h.previewState != nil {
		copy := *h.previewState
		previousState = &copy
	}
	previousReducer, previousSeq := h.previewReducer, h.previewLastSeq
	previousPerm, previousElicit := h.previewPerm, h.previewElicit
	wasTruncated := h.previewTruncated
	width, generation := h.width, h.previewStreamGeneration
	return func() tea.Msg {
		if session == nil {
			return previewLoadedMsg{
				state: previousState, perm: previousPerm, elicit: previousElicit,
				streamGeneration: generation, streamErr: fmt.Errorf("no session selected"),
				truncated: wasTruncated, reducer: previousReducer, lastSeq: previousSeq, continueStream: true,
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		entries, truncated, err := network.ConversationSnapshot(ctx, home, session.info.ID, session.machine, 0)
		cancel()
		if err != nil {
			return previewLoadedMsg{
				state: previousState, perm: previousPerm, elicit: previousElicit,
				streamGeneration: generation, streamErr: err,
				truncated: wasTruncated, reducer: previousReducer, lastSeq: previousSeq, continueStream: true,
			}
		}
		var reducer acp.ThreadReducer
		var state acp.ThreadState
		var lastSeq int64
		for _, entry := range entries {
			state = reducer.Apply(entry)
			if entry.Seq > lastSeq {
				lastSeq = entry.Seq
			}
		}
		perm, elicit := previewPending(state)
		return previewLoadedMsg{
			lines:            formatChatThread(state, width, nil),
			state:            &state,
			perm:             perm,
			elicit:           elicit,
			streamGeneration: generation,
			reducer:          reducer,
			lastSeq:          lastSeq,
			truncated:        wasTruncated || truncated,
			continueStream:   true,
		}
	}
}
func (h *home) viewPreview() string {
	session := h.selectedSession()
	terminal := h.terminalActive()
	h.previewActionLine = -1
	h.previewActionRanges = [2][2]int{}
	title := "Session preview"
	if session != nil {
		title = sessionTitle(*session)
	}
	contentWidth := h.chatTextWidth()
	sidebar := !terminal && h.previewSide && h.width >= 100 && h.previewState != nil && session != nil
	sideWidth := 0
	if sidebar {
		sideWidth = min(30, max(24, h.width/4))
	}
	mono := lipgloss.NoColor{}
	headerText := "‹ sessions  " + title
	if session != nil {
		headerText += " · " + session.info.Harness + " · " + pathBase(session.info.Directory) + " · " + session.machineName
		if !terminal && h.previewState != nil {
			headerText += " · " + chatStatusLabel(session, *h.previewState)
		}
	}
	if !terminal && h.sessionCount() > 1 && contentWidth >= 28 {
		actions := "‹ prev · next ›"
		leftWidth := max(1, contentWidth-lipgloss.Width(actions)-1)
		left := truncate(headerText, leftWidth)
		gap := max(1, contentWidth-lipgloss.Width(left)-lipgloss.Width(actions))
		headerText = left + strings.Repeat(" ", gap) + actions
	} else {
		headerText = truncate(headerText, contentWidth)
	}
	header := lipgloss.NewStyle().Bold(true).Foreground(mono).Render(acp.StripControls(headerText))
	var hintLines []string
	if terminal {
		hintLines = []string{"ctrl+] back · keys and mouse go to the agent · alt+arrows pan"}
		if h.terminal != nil && h.terminal.truncated {
			hintLines = append(hintLines, "Replay truncated; older terminal bytes are no longer buffered.")
		}
		if h.terminal != nil && h.terminal.err != nil {
			hintLines = append(hintLines, truncate(h.terminal.err.Error(), contentWidth))
		}
	} else {
		actions := "i message · a image"
		if h.previewPerm.requestID != "" {
			actions = "a/A/r permission"
		} else if h.previewElicit.id != "" {
			actions = "1–3 answer"
			if h.previewElicit.allowMultiple {
				actions = "1–3 select · Enter submit"
			}
		}
		switch {
		case h.width >= 110:
			hintLines = []string{"ctrl+] back · ctrl+r reload · j/k scroll · e tools · " + actions}
		case h.width >= 70:
			hintLines = []string{"ctrl+] back · j/k scroll · e tools · " + actions, "ctrl+x b details · tab switches composer field"}
		default:
			hintLines = []string{"ctrl+] back · q quit · j/k scroll", "e tools · " + actions}
		}
	}
	if !terminal {
		if h.width >= 70 {
			hintLines = append(hintLines, "click image to open · o opens hovered image")
		} else {
			hintLines = append(hintLines, "click image to open")
		}
		if h.previewConfigLoaded && h.previewState != nil {
			actionHint := "ctrl+x a permissions · ctrl+x m model · ctrl+x e effort · hover: c copy"
			if sessionCanFork(session) {
				actionHint += " · f fork"
			}
			hintLines = append(hintLines, actionHint)
		}
		if h.previewState != nil {
			hintLines = append(hintLines, chatContextSummary(*h.previewState))
			if plan := chatPlanSummary(*h.previewState, contentWidth); len(plan) > 0 {
				hintLines = append(hintLines, truncate("◇ "+strings.TrimSpace(plan[0]), contentWidth))
			}
		}
		if h.previewConfigErr != nil {
			hintLines = append(hintLines, truncate("Agent settings unavailable: "+cleanChatText(h.previewConfigErr.Error()), contentWidth))
		}
		if h.previewImageErr != nil {
			hintLines = append(hintLines, truncate("Image open failed: "+h.previewImageErr.Error(), contentWidth))
		}
		if h.previewElicit.allowMultiple && len(h.previewElicitSelected) > 0 {
			var labels []string
			for _, selected := range h.previewElicitSelected {
				label := selected
				for i, id := range h.previewElicit.optionIDs {
					if id == selected && i < len(h.previewElicit.choices) {
						label = h.previewElicit.choices[i]
						break
					}
				}
				labels = append(labels, label)
			}
			hintLines = append(hintLines, truncate("Selected: "+strings.Join(labels, ", "), contentWidth))
		}
		if h.previewTruncated {
			hintLines = append(hintLines, "Earlier conversation events were truncated; showing retained messages.")
		}
		if h.previewStreamErr != nil {
			hintLines = append(hintLines, truncate("Live updates stopped: "+cleanChatText(h.previewStreamErr.Error()), contentWidth))
		}
		if h.previewActionErr != nil {
			hintLines = append(hintLines, truncate("Action failed: "+cleanChatText(h.previewActionErr.Error()), contentWidth))
		}
		if h.previewActionStatus != "" {
			hintLines = append(hintLines, truncate(h.previewActionStatus, contentWidth))
		}
	}
	var slashMatches [len(hubChatSlashCommands)]hubChatSlashCommand
	slashCount := 0
	if h.previewCompose && !h.previewAttach {
		slashMatches, slashCount = matchingHubChatSlashCommands(h.previewDraft)
	}
	if slashCount > 0 {
		hintLines = append(hintLines, "↑/↓ choose · Tab complete · Enter run")
	}
	maxRows := max(1, h.height-8-len(hintLines))
	if terminal {
		maxRows = max(1, h.height-3-len(hintLines))
	}
	if h.previewCompose {
		maxRows = max(1, maxRows-3-slashCount)
	}
	var bodyLines []string
	switch {
	case terminal && h.terminal != nil:
		h.previewBodyTop = 1
		h.terminal.viewportWidth, h.terminal.viewportHeight = contentWidth, maxRows
		bodyLines = terminalScreenLines(h.terminal.emulator, contentWidth, maxRows, h.terminal.panX, h.terminal.panY)
	case terminal:
		h.previewBodyTop = 1
		bodyLines = []string{"Connecting to terminal…"}
	case h.previewErr != nil:
		h.previewBodyTop = 3
		appendWrapped(&bodyLines, h.previewErr.Error(), contentWidth)
	case h.previewState != nil:
		h.previewBodyTop = 3
		bodyLines = formatChatThread(*h.previewState, contentWidth, h.expandedTools)
	case len(h.previewLines) == 0:
		h.previewBodyTop = 3
		bodyLines = []string{"Loading…"}
	default:
		h.previewBodyTop = 3
		for _, line := range h.previewLines {
			bodyLines = append(bodyLines, strings.Split(ansi.WrapWc(line, contentWidth, " "), "\n")...)
		}
	}
	h.previewStart = 0
	if !terminal && len(bodyLines) > maxRows {
		tail := max(0, min(h.previewTail, len(bodyLines)-maxRows))
		h.previewStart = len(bodyLines) - maxRows - tail
	}
	if !terminal {
		bodyLines = previewWindow(bodyLines, maxRows, h.previewTail)
		if h.previewHoverLine >= h.previewStart && h.previewHoverLine < h.previewStart+len(bodyLines) {
			index := h.previewHoverLine - h.previewStart
			line := strings.TrimSpace(bodyLines[index])
			actions := chatLineActions{}
			if h.previewState != nil {
				actions = chatActionsAtLine(*h.previewState, contentWidth, h.previewHoverLine, h.expandedTools)
			}
			canFork := sessionCanFork(session)
			actionText := " [Copy]"
			if canFork {
				actionText += " [Fork]"
			}
			if actions.message != "" && contentWidth >= lipgloss.Width(actionText)+1 {
				base := truncate(line, max(1, contentWidth-lipgloss.Width(actionText)))
				bodyLines[index] = base + actionText
				h.previewActionLine = h.previewBodyTop + index
				start := 2 + lipgloss.Width(base) + 1
				h.previewActionRanges[0] = [2]int{start, start + lipgloss.Width("[Copy]")}
				if canFork {
					forkStart := h.previewActionRanges[0][1] + 1
					h.previewActionRanges[1] = [2]int{forkStart, forkStart + lipgloss.Width("[Fork]")}
				}
			} else if actions.expandable != "" {
				action := "  [click/Enter to expand]"
				if chatExpandableOpen(actions.expandable, h.expandedTools) {
					action = "  [click/Enter to collapse]"
				}
				bodyLines[index] = truncate(line, max(1, contentWidth-lipgloss.Width(action))) + action
			} else if actions.image.SHA != "" {
				action := "  [click/Enter to open]"
				bodyLines[index] = truncate(line, max(1, contentWidth-lipgloss.Width(action))) + action
			} else {
				bodyLines[index] = truncate("› "+line, contentWidth)
			}
		}
	}
	text := strings.Join(bodyLines, "\n")
	if !terminal {
		text = lipgloss.JoinVertical(lipgloss.Left, header, "", text)
	}
	if h.previewCompose && !terminal && slashCount > 0 {
		var suggestions []string
		selected := min(h.previewSlash, slashCount-1)
		for i := 0; i < slashCount; i++ {
			marker := "  "
			if i == selected {
				marker = "› "
			}
			command := slashMatches[i]
			appendChatWrapped(&suggestions, marker+command.name+" · "+command.help, contentWidth)
		}
		text = lipgloss.JoinVertical(lipgloss.Left, text, "", strings.Join(suggestions, "\n"))
	}
	if h.previewCompose && !terminal {
		var draft []string
		draft = append(draft, h.composerSettingsLine(contentWidth))
		h.previewConfigLine = 2 + strings.Count(text, "\n") + 1
		if h.previewAttach {
			appendWrapped(&draft, "Image path: "+h.previewImagePath+"▌", contentWidth)
			if h.previewDraft != "" {
				appendWrapped(&draft, "> "+h.previewDraft, contentWidth)
			}
		} else {
			appendWrapped(&draft, "> "+h.previewDraft+"▌", contentWidth)
			if h.previewImagePath != "" {
				appendWrapped(&draft, "Image: "+h.previewImagePath+" (tab to edit)", contentWidth)
			}
		}
		text = lipgloss.JoinVertical(lipgloss.Left, text, "", strings.Join(draft, "\n"))
	} else {
		h.previewConfigLine = -1
		h.previewConfigRanges = [3][2]int{}
	}
	for i := range hintLines {
		hintLines[i] = truncate(hintLines[i], contentWidth)
	}
	hint := lipgloss.NewStyle().Foreground(mono).Render(strings.Join(hintLines, "\n"))
	main := lipgloss.NewStyle().Padding(1, 2).Width(contentWidth + 4).Render(lipgloss.JoinVertical(lipgloss.Left, text, "", hint))
	if sidebar {
		side := chatDetails(session, *h.previewState, sideWidth)
		separator := lipgloss.NewStyle().Foreground(mono).Render(strings.Repeat("│\n", max(1, h.height-2)))
		main = lipgloss.JoinHorizontal(lipgloss.Top, main, separator, side)
	}
	return main
}

func (h *home) paletteActivate() (tea.Model, tea.Cmd) {
	if h.paletteFrom == panelPreview && (h.paletteCursor == 0 || h.paletteCursor == 4 || h.paletteCursor == 5) {
		h.cancelPreviewStream()
	}
	switch h.paletteCursor {
	case 0:
		h.panel = panelList
		return h, h.reloadCmd()
	case 1:
		if h.sessionCount() == 0 {
			h.cancelPreviewStream()
			h.panel = panelList
			return h, nil
		}
		return h, h.openPreview(panelList)
	case 2:
		return h, tea.Quit
	case 3:
		return h, h.openNewSession()
	case 4:
		h.panel = panelList
	case 5:
		h.panel = panelHome
	case 6:
		h.helpFrom, h.panel = h.paletteFrom, panelHelp
	default:
		h.panel = panelHome
	}
	return h, nil
}

func (h *home) viewPalette() string {
	mono := lipgloss.NoColor{}
	bold := lipgloss.NewStyle().Bold(true).Foreground(mono)
	dim := lipgloss.NewStyle().Foreground(mono)

	title := bold.Render(" Commands")
	lines := []string{title, dim.Render(strings.Repeat("─", max(1, h.width)))}
	for i, cmd := range paletteCommands {
		prefix := "  "
		if i == h.paletteCursor {
			prefix = "> "
		}
		line := prefix + cmd.label
		if cmd.hint != "" {
			padding := h.width - lipgloss.Width(line) - lipgloss.Width(cmd.hint) - 2
			if padding < 1 {
				padding = 1
			}
			line += strings.Repeat(" ", padding) + dim.Render(cmd.hint)
		}
		if i == h.paletteCursor {
			line = bold.Render(line)
		}
		lines = append(lines, line)
	}
	for len(lines) < h.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, dim.Render("j/k move · enter run · esc close · q quit"))
	if len(lines) > h.height {
		lines = lines[:h.height]
	}
	return strings.Join(lines, "\n")
}

func truncate(value string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= maxWidth {
		return value
	}
	for len(value) > 0 && lipgloss.Width(value+"…") > maxWidth {
		value = value[:len(value)-1]
	}
	return value + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// typedText reports whether a key press is text to insert. Shift is how
// capitals and many symbols arrive, so it counts; ctrl and alt do not.
func typedText(msg tea.KeyPressMsg) bool {
	return msg.Text != "" && msg.Mod&^tea.ModShift == 0
}
