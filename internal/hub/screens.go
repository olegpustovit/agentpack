package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
)

type hubMachine struct {
	id, name string
}

type newSessionForm struct {
	directories []string
	field       int
	project     int
	harness     int
	machine     string
	view        int
	prompt      string
	loading     bool
	starting    bool
	err         error
}

type recentFoldersLoadedMsg struct {
	directories []string
	machines    []hubMachine
	err         error
}

type sessionStartedMsg struct {
	info      network.SessionInfo
	machine   string
	prompt    string
	transport string
	err       error
}

type initialPromptSentMsg struct {
	sessionID string
	err       error
}

func (h *home) availableMachines() []hubMachine {
	machines := []hubMachine{{id: "local", name: h.machineName}}
	seen := map[string]struct{}{"local": {}}
	for _, machine := range h.machineChoices {
		if machine.id == "" || machine.id == "local" {
			continue
		}
		if _, ok := seen[machine.id]; ok {
			continue
		}
		seen[machine.id] = struct{}{}
		if machine.name == "" {
			machine.name = machine.id
		}
		machines = append(machines, machine)
	}
	for _, row := range h.rows {
		if row.kind != rowSession || row.session.machine == "" || row.session.machine == "local" {
			continue
		}
		if _, ok := seen[row.session.machine]; ok {
			continue
		}
		seen[row.session.machine] = struct{}{}
		name := row.session.machineName
		if name == "" {
			name = row.session.machine
		}
		machines = append(machines, hubMachine{id: row.session.machine, name: name})
	}
	return machines
}

func (h *home) selectedMachineName(id string) string {
	for _, machine := range h.availableMachines() {
		if machine.id == id {
			return machine.name
		}
	}
	return h.machineName
}

func (h *home) setNewSessionMachine(step int) {
	machines := h.availableMachines()
	index := 0
	for i, machine := range machines {
		if machine.id == h.newSession.machine {
			index = i
			break
		}
	}
	h.newSession.machine = machines[(index+step+len(machines))%len(machines)].id
}

func (h *home) changeHomeSelector(step int) {
	form := &h.newSession
	switch h.homeField {
	case 0:
		form.harness = (form.harness + step + len(hubHarnesses)) % len(hubHarnesses)
	case 1:
		if len(form.directories) > 0 {
			form.project = (form.project + step + len(form.directories)) % len(form.directories)
		}
	case 2:
		h.setNewSessionMachine(step)
	case 3:
		form.view = (form.view + step + 2) % 2
	}
}

func (h *home) startHomePrompt() tea.Cmd {
	if strings.TrimSpace(h.homeDraft) == "" {
		return nil
	}
	h.newSession.prompt = strings.TrimSpace(h.homeDraft)
	h.newSession.starting = true
	h.homePromptErr = nil
	return h.startNewSessionCmd()
}

var hubHarnesses = []struct {
	id, label string
}{
	{"claude", "Claude"},
	{"codex", "Codex"},
	{"opencode", "OpenCode"},
	{"agent", "Cursor"},
	{"grok", "Grok"},
	{"agy", "Antigravity"},
	{"omp", "Oh My Pi"},
}

func availableRecentDirectories(folders []network.RecentFolder) []string {
	seen := make(map[string]struct{}, len(folders))
	directories := make([]string, 0, len(folders))
	for _, folder := range folders {
		if !folder.Available || folder.Directory == "" {
			continue
		}
		directory := filepath.Clean(folder.Directory)
		if _, ok := seen[directory]; ok {
			continue
		}
		seen[directory] = struct{}{}
		directories = append(directories, directory)
	}
	return directories
}

// preferDirectory puts the project ap was opened in first: the listed folder
// that contains cwd (the deepest one), or cwd itself when none does.
func preferDirectory(directories []string, cwd string) []string {
	cwd = filepath.Clean(cwd)
	best := -1
	for i, directory := range directories {
		if relative, err := filepath.Rel(directory, cwd); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			if best < 0 || len(directory) > len(directories[best]) {
				best = i
			}
		}
	}
	chosen := cwd
	if best >= 0 {
		chosen = directories[best]
	}
	ordered := []string{chosen}
	for _, directory := range directories {
		if directory != chosen {
			ordered = append(ordered, directory)
		}
	}
	return ordered
}

func (h *home) openNewSession() tea.Cmd {
	h.cancelPreviewStream()
	h.newFrom = h.panel
	h.panel = panelNew
	h.newSession.prompt = ""
	h.newSession.field = 0
	h.newSession.loading = true
	h.newSession.starting = false
	h.newSession.err = nil
	return h.loadRecentFoldersCmd()
}

func (h *home) loadRecentFoldersCmd() tea.Cmd {
	home := h.agentpackHome
	return func() tea.Msg {
		folders, err := network.RecentFolders(home)
		machines := make([]hubMachine, 0)
		if enabled, gatewayErr := network.GatewayEnabled(home); gatewayErr == nil && enabled {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			data, requestErr := network.NodeRequest(ctx, home, http.MethodGet, "/api/nodes", nil)
			cancel()
			if requestErr == nil {
				var nodes []network.TailnetNode
				if json.Unmarshal(data, &nodes) == nil {
					for _, node := range nodes {
						if !node.Online || node.ID == "" {
							continue
						}
						name := node.Name
						if name == "" {
							name = node.ID
						}
						machines = append(machines, hubMachine{id: node.ID, name: name})
					}
				}
			}
		}
		directories := availableRecentDirectories(folders)
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			directories = preferDirectory(directories, cwd)
		}
		return recentFoldersLoadedMsg{directories: directories, machines: machines, err: err}
	}
}

func (h *home) startNewSessionCmd() tea.Cmd {
	form := h.newSession
	home := h.agentpackHome
	machines := h.availableMachines()
	machine := form.machine
	if machine == "" {
		machine = "local"
	}
	if form.project < 0 || form.project >= len(form.directories) || form.harness < 0 || form.harness >= len(hubHarnesses) {
		return func() tea.Msg { return sessionStartedMsg{err: fmt.Errorf("choose an available project and agent")} }
	}
	if form.view != 0 && form.prompt != "" {
		return func() tea.Msg {
			return sessionStartedMsg{err: fmt.Errorf("initial prompts require Chat; switch View to Chat")}
		}
	}
	available := false
	for _, option := range machines {
		available = available || option.id == machine
	}
	if !available {
		return func() tea.Msg { return sessionStartedMsg{err: fmt.Errorf("choose an online machine")} }
	}
	directory := form.directories[form.project]
	harness := hubHarnesses[form.harness].id
	transport := "acp"
	if form.view != 0 {
		transport = "terminal"
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		body, err := json.Marshal(struct {
			Directory string `json:"directory"`
			Harness   string `json:"harness"`
			Mode      string `json:"mode"`
			Transport string `json:"transport"`
		}{Directory: directory, Harness: harness, Mode: "default", Transport: transport})
		if err != nil {
			return sessionStartedMsg{err: err}
		}
		data, err := network.NodeRequest(ctx, home, http.MethodPost, gatewayPeerPath("/api/sessions", machine), bytes.NewReader(body))
		if err != nil {
			return sessionStartedMsg{err: err}
		}
		var info network.SessionInfo
		if err := json.Unmarshal(data, &info); err != nil {
			return sessionStartedMsg{err: fmt.Errorf("decode started session: %w", err)}
		}
		return sessionStartedMsg{info: info, machine: machine, prompt: form.prompt, transport: transport}
	}
}

func (h *home) sendInitialPromptCmd(sessionID, machine, prompt string) tea.Cmd {
	home := h.agentpackHome
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		err := postConversation(ctx, home, sessionID, machine, "prompt", struct {
			Text string `json:"text"`
		}{Text: prompt})
		return initialPromptSentMsg{sessionID: sessionID, err: err}
	}
}

func (h *home) updateNewSession(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	form := &h.newSession
	if key.String() == "esc" || key.String() == "ctrl+c" {
		h.panel = h.newFrom
		return h, nil
	}
	if form.loading || form.starting {
		return h, nil
	}
	if key.String() == "up" || key.String() == "k" {
		if form.field > 0 {
			form.field--
		}
		return h, nil
	}
	if key.String() == "down" || key.String() == "j" {
		if form.field < 3 {
			form.field++
		}
		return h, nil
	}
	if key.String() == "enter" {
		if len(form.directories) == 0 {
			form.err = fmt.Errorf("use an agentpack folder before starting a session")
			return h, nil
		}
		form.starting = true
		form.err = nil
		return h, h.startNewSessionCmd()
	}
	step := 0
	if key.String() == "left" || key.String() == "h" {
		step = -1
	} else if key.String() == "right" || key.String() == "l" {
		step = 1
	}
	if step != 0 {
		switch form.field {
		case 0:
			form.harness = (form.harness + step + len(hubHarnesses)) % len(hubHarnesses)
		case 1:
			if len(form.directories) > 0 {
				form.project = (form.project + step + len(form.directories)) % len(form.directories)
			}
		case 2:
			h.setNewSessionMachine(step)
		case 3:
			form.view = (form.view + step + 2) % 2
		}
	}
	return h, nil
}

func (h *home) viewHome() string {
	width, height := max(1, h.width), max(1, h.height)
	logo := lipgloss.NewStyle().Bold(true).Render("AGENTPACK")
	running, needs := 0, 0
	machineIDs := map[string]struct{}{"local": {}}
	for _, row := range h.rows {
		if row.kind != rowSession {
			continue
		}
		if row.session.info.State.Running {
			running++
		}
		if row.session.info.ConversationState == "needs_you" {
			needs++
		}
		machineIDs[row.session.machine] = struct{}{}
	}
	machines := "machines"
	if len(machineIDs) == 1 {
		machines = "machine"
	}
	status := fmt.Sprintf("%d running · %d needs you · %d %s", running, needs, len(machineIDs), machines)
	start := min(max(0, height/3-5), max(0, height-8))
	lines := make([]string, height)
	center := func(text string) string {
		pad := max(0, (width-lipgloss.Width(text))/2)
		return strings.Repeat(" ", pad) + text
	}
	lines[start] = center(logo)
	if start+2 < height {
		lines[start+2] = center(status)
	}
	if start+4 < height {
		prompt := "Describe a task…"
		if h.homeDraft != "" {
			prompt = h.homeDraft + "▏"
		}
		if h.newSession.starting {
			prompt = "Starting session…"
		}
		lines[start+4] = center(truncate("┃ "+prompt, width))
		h.homePromptLine = start + 4
	}
	values := []string{
		hubHarnesses[min(max(0, h.newSession.harness), len(hubHarnesses)-1)].label,
		"Choose project",
		h.machineName,
		[]string{"Chat", "Terminal"}[min(max(0, h.newSession.view), 1)],
	}
	if len(h.newSession.directories) > 0 {
		project := min(max(0, h.newSession.project), len(h.newSession.directories)-1)
		values[1] = filepath.Base(h.newSession.directories[project])
	}
	values[2] = h.selectedMachineName(h.newSession.machine)
	labels := [...]string{"Agent", "Project", "Machine", "View"}
	segments := make([]string, len(labels))
	for i, label := range labels {
		segments[i] = fmt.Sprintf("%s: %s", label, values[i])
	}
	selectorGroups := [][]int{{0, 1, 2, 3}}
	if width < 60 {
		selectorGroups = [][]int{{0, 1}, {2, 3}}
	}
	if width < 40 {
		selectorGroups = [][]int{{0}, {1}, {2}, {3}}
	}
	h.homeSelectorBounds = [4][2]int{}
	for i := range h.homeSelectorLines {
		h.homeSelectorLines[i] = -1
	}
	for rowIndex, fields := range selectorGroups {
		total := 0
		for i, field := range fields {
			total += lipgloss.Width(segments[field])
			if i > 0 {
				total += 3
			}
		}
		x := max(0, (width-total)/2)
		row := start + 5 + rowIndex
		var selector strings.Builder
		for i, field := range fields {
			if i > 0 {
				selector.WriteString("   ")
				x += 3
			}
			text := segments[field]
			if field == h.homeField {
				text = lipgloss.NewStyle().Bold(true).Render(text)
			}
			begin := x
			selector.WriteString(text)
			x += lipgloss.Width(text)
			h.homeSelectorLines[field] = row
			h.homeSelectorBounds[field] = [2]int{begin, x}
		}
		if row < height {
			lines[row] = truncate(strings.Repeat(" ", max(0, (width-total)/2))+selector.String(), width)
		}
	}
	errorRow := start + 5 + len(selectorGroups)
	if h.homePromptErr != nil && errorRow < height {
		lines[errorRow] = center(truncate(h.homePromptErr.Error(), width))
	}
	row := start + 7 + len(selectorGroups)
	h.homeListStart = row
	// The prototype's table: one centred column, no group headings, "+N more" past the fold.
	column := min(width-4, max(52, min(92, width-16)))
	left := strings.Repeat(" ", max(0, (width-column)/2))
	h.homeLines = h.homeLines[:0]
	var sessions []int
	for i := range h.rows {
		if h.rows[i].kind == rowSession {
			sessions = append(sessions, i)
		}
	}
	shown := min(len(sessions), max(3, height-16), max(0, height-2-row))
	if shown < len(sessions) && shown > 0 {
		shown--
	}
	for _, rowIndex := range sessions[:shown] {
		entry := h.rows[rowIndex]
		line := homeSessionLine(*entry.session, column)
		index := sessionRowIndex(h.rows, rowIndex)
		switch {
		case index == h.cursor:
			line = lipgloss.NewStyle().Reverse(true).Render(line)
		case index == h.hoverRow:
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}
		lines[row] = left + line
		h.homeLines = append(h.homeLines, rowIndex)
		row++
	}
	if shown < len(sessions) && row < height-1 {
		lines[row] = left + lipgloss.NewStyle().Faint(true).Render(fmt.Sprintf(" +%d more · ctrl+x l all sessions", len(sessions)-shown))
	}
	footer := "enter start · ↑/↓ sessions · tab selectors · ctrl+x n new · ? help · ctrl+c quit"
	if width < 60 {
		footer = "enter start · tab select · ctrl+x n new · ? help"
	}
	lines[height-1] = truncate(footer, width)
	return strings.Join(lines, "\n")
}

func (h *home) viewNewSession() string {
	width := max(1, h.width-4)
	form := h.newSession
	lines := []string{"New session", ""}
	if form.loading {
		lines = append(lines, "Loading recent projects…")
	} else if len(form.directories) == 0 {
		lines = append(lines, "No recent projects. Launch an agent in a folder first.")
	} else {
		projectIndex := min(max(0, form.project), len(form.directories)-1)
		harnessIndex := min(max(0, form.harness), len(hubHarnesses)-1)
		viewIndex := min(max(0, form.view), 1)
		values := []string{
			hubHarnesses[harnessIndex].label,
			filepath.Base(form.directories[projectIndex]),
			h.selectedMachineName(form.machine),
			[]string{"Chat", "Terminal"}[viewIndex],
		}
		labels := []string{"Agent", "Project", "Machine", "View"}
		for i, label := range labels {
			marker := "  "
			if i == form.field {
				marker = "› "
			}
			line := marker + label + "  " + values[i]
			switch i {
			case 0:
				line += "  ←/→ change agent"
			case 1:
				line += "  ←/→ change project"
			case 3:
				line += "  ←/→ change view"
			}
			lines = append(lines, truncate(line, width))
		}
		lines = append(lines, "", "Mode  default")
		if form.starting {
			lines = append(lines, "", "Starting session…")
		} else {
			lines = append(lines, "", "Enter start · Esc cancel")
		}
	}
	if form.err != nil {
		lines = append(lines, "", truncate(form.err.Error(), width))
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(lines, "\n"))
}

func (h *home) viewHelp() string {
	rows := []string{
		"Help",
		"",
		"j/k or ↑/↓   move through sessions",
		"Enter        open the selected session",
		"ctrl+x n     start a new session",
		"ctrl+x l     show all sessions",
		"ctrl+p       open commands",
		"?            show this help",
		"ctrl+c       quit the hub",
		"Home: type a prompt · Tab selects Agent/Project/Machine/View",
		"New session: ↑/↓ field · ←/→ choose Agent/Project/Machine/View",
		"Chat: i message · a image path · Tab edits field",
		"Chat: ctrl+x a/m/e settings · c Copy · f Fork when supported",
		"Chat: click image or o opens hovered image",
		"Chat: j/k scroll · ctrl+] back to sessions",
		"Images use text placeholders; Enter/o opens the image viewer.",
	}
	for i := range rows {
		rows[i] = truncate(rows[i], max(1, h.width-4))
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(rows, "\n"))
}

func (h *home) viewConfigPicker() string {
	option := configOptionFor(h.previewConfigOptions, h.configCategory)
	title := h.configCategory
	switch h.configCategory {
	case "mode":
		title = "Permissions"
	case "model":
		title = "Model"
	case "thought_level":
		title = "Effort"
	}
	width := max(1, h.width-4)
	lines := []string{title, ""}
	if option == nil {
		lines = append(lines, "This setting is not available for the selected agent.")
	} else if h.configPending {
		lines = append(lines, "Saving setting…")
	} else {
		for i, choice := range option.Options {
			marker := "  "
			if i == h.configCursor {
				marker = "› "
			}
			name := choice.Name
			if name == "" {
				name = choice.Value
			}
			current := ""
			if choice.Value == option.CurrentValue {
				current = " · current"
			}
			lines = append(lines, truncate(marker+name+current, width))
		}
		lines = append(lines, "", "↑/↓ move · Enter apply · Esc cancel")
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(lines, "\n"))
}

func configCategoryLabel(category string) string {
	switch category {
	case "mode":
		return "Permissions"
	case "model":
		return "Model"
	case "thought_level":
		return "Effort"
	default:
		return category
	}
}

func (h *home) composerSettingsLine(width int) string {
	categories := [...]string{"mode", "model", "thought_level"}
	var line strings.Builder
	h.previewConfigRanges = [3][2]int{}
	for i, category := range categories {
		if i > 0 {
			line.WriteString("  ")
		}
		value := "unavailable"
		if !h.previewConfigLoaded {
			value = "loading…"
		} else if option := configOptionFor(h.previewConfigOptions, category); option != nil {
			value = configValueLabel(*option)
			if value == "" {
				value = "not set"
			}
		}
		label := configCategoryLabel(category) + ": " + value
		start := 2 + lipgloss.Width(line.String())
		line.WriteString(label)
		end := 2 + lipgloss.Width(line.String())
		if start < width+2 {
			h.previewConfigRanges[i] = [2]int{start, min(end, width+2)}
		}
	}
	return truncate(line.String(), width)
}
func chatStatusLabel(session *listedSession, state acp.ThreadState) string {
	if state.NeedsYou || session != nil && session.info.ConversationState == "needs_you" {
		return "◆ Needs you"
	}
	if state.Working || session != nil && session.info.ConversationState == "working" {
		return "… Working"
	}
	if session != nil && session.info.State.Restarting {
		return "◌ Restarting"
	}
	if session != nil && session.info.State.Running {
		return "○ Idle"
	}
	return "Stopped"
}

func chatContextSummary(state acp.ThreadState) string {
	if state.ContextLimit <= 0 {
		return "Context unavailable"
	}
	percent := state.ContextTokens * 100 / state.ContextLimit
	fill := max(0, min(10, percent/10))
	summary := fmt.Sprintf("Context [%s%s] %d%% · %s/%s",
		strings.Repeat("█", fill), strings.Repeat("░", 10-fill), percent,
		number(state.ContextTokens), number(state.ContextLimit))
	if state.CostUSD > 0 {
		summary += fmt.Sprintf(" · $%.2f", state.CostUSD)
	}
	return summary
}

func chatPlanSummary(state acp.ThreadState, width int) []string {
	var lines []string
	if state.PlanKind != "" {
		appendChatWrapped(&lines, "Plan · "+cleanChatText(state.PlanKind), width)
	}
	for _, item := range state.Items {
		if item.Kind != "plan" {
			continue
		}
		var plan struct {
			Entries []struct {
				Status  string `json:"status"`
				Content string `json:"content"`
			} `json:"entries"`
		}
		_ = json.Unmarshal(item.Data, &plan)
		done := 0
		for _, entry := range plan.Entries {
			if entry.Status == "completed" || entry.Status == "done" {
				done++
			}
		}
		appendChatWrapped(&lines, fmt.Sprintf("Plan · %d/%d complete", done, len(plan.Entries)), width)
		for _, entry := range plan.Entries {
			marker := "☐"
			switch entry.Status {
			case "completed", "done":
				marker = "☑"
			case "in_progress", "run":
				marker = "◐"
			}
			appendChatBlock(&lines, marker+" ", entry.Content, width)
		}
		break
	}
	return lines
}

func chatDetails(session *listedSession, state acp.ThreadState, width int) string {
	var lines []string
	appendSection := func(title string, values ...string) {
		if len(values) == 0 {
			return
		}
		lines = append(lines, title)
		for _, value := range values {
			appendChatWrapped(&lines, "  "+value, width)
		}
	}
	if state.ContextLimit > 0 {
		appendSection("Context", chatContextSummary(state))
	}
	plan := chatPlanSummary(state, width)
	if len(plan) > 0 {
		lines = append(lines, "Plan")
		lines = append(lines, plan...)
	}
	files := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range state.Items {
		if item.Kind != "tool" {
			continue
		}
		for _, block := range decodeChatTool(item.Data).Content {
			if block.Type != "diff" || block.Path == "" {
				continue
			}
			if _, ok := seen[block.Path]; ok {
				continue
			}
			seen[block.Path] = struct{}{}
			files = append(files, block.Path)
		}
	}
	if len(files) > 0 {
		appendSection("Changes", files...)
	}
	if session != nil {
		startedBy := ""
		if actor := session.info.StartedBy; actor != nil {
			startedBy = actor.User
			if startedBy == "" {
				startedBy = actor.Node
			}
		}
		values := []string{
			"Status  " + chatStatusLabel(session, state),
			"Agent  " + harnessLabel(session.info.Harness),
			"Machine  " + session.machineName,
			"Project  " + pathBase(session.info.Directory),
			"View  " + sessionViewLabel(session.info.Transport),
			"Staging mode  " + defaultMode(session.info.Mode),
		}
		if session.info.AgentSessionID != "" {
			values = append(values, "Agent session  "+session.info.AgentSessionID)
		}
		if startedBy != "" {
			values = append(values, "Started by  "+startedBy)
		}
		appendSection("Session", values...)
		connection := "Local gateway"
		if session.machine != "" && session.machine != "local" {
			connection = "Peer · " + session.machine
		}
		if !session.info.State.Running {
			connection += " · process stopped"
		}
		appendSection("Connection", connection, "Viewers  not reported by the gateway")
	}
	if len(lines) == 0 {
		lines = append(lines, "No session details yet")
	}
	return strings.Join(lines, "\n")
}

func number(value int) string {
	if value >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	}
	if value >= 1_000 {
		return fmt.Sprintf("%.0fk", float64(value)/1_000)
	}
	return fmt.Sprint(value)
}

func defaultMode(mode string) string {
	if mode == "" {
		return "default"
	}
	return mode
}

func sessionViewLabel(transport string) string {
	if transport == "acp" {
		return "chat"
	}
	return "terminal"
}

func sessionListWidth(width int) int {
	if width >= 100 {
		return max(1, width-38)
	}
	return max(1, width)
}

func sessionAgeLabel(lastUsed, started time.Time) string {
	if lastUsed.IsZero() {
		lastUsed = started
	}
	if lastUsed.IsZero() {
		return "unknown"
	}
	age := time.Since(lastUsed)
	if age < time.Minute {
		return "now"
	}
	if age < time.Hour {
		return fmt.Sprintf("%dm ago", int(age/time.Minute))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(age/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(age/(24*time.Hour)))
}

func (h *home) viewSessions() string {
	width, height := max(1, h.width), max(1, h.height)
	leftWidth := sessionListWidth(width)
	hasDetails := width >= 100
	lines := make([]string, height)
	mono := lipgloss.NoColor{}
	bold := lipgloss.NewStyle().Bold(true).Foreground(mono)
	dim := lipgloss.NewStyle().Foreground(mono)

	title := bold.Render(" ap ") + dim.Render(" · Sessions")
	gateway := dim.Render(truncate(h.gatewayLine, max(0, leftWidth-lipgloss.Width(title)-1)))
	lines[0] = truncate(title+strings.Repeat(" ", max(1, leftWidth-lipgloss.Width(title)-lipgloss.Width(gateway)))+gateway, leftWidth)
	lines[1] = strings.Repeat("─", leftWidth)
	tabLine, _ := sessionTabLine(h.sessionTab)
	lines[2] = truncate(" "+tabLine, leftWidth)
	filterMarker := "  "
	if h.sessionFilterFocused {
		filterMarker = "› "
	}
	filter := h.sessionFilter
	if filter == "" {
		filter = "type / to filter"
	}
	lines[3] = truncate(filterMarker+"Filter  "+filter, leftWidth)
	lines[4] = strings.Repeat("─", leftWidth)

	h.ensureSessionCursor()
	allLines := h.sessionLines()
	bodyTop, bodyHeight := 5, max(1, height-6)
	selectedFirst, selectedLast := -1, -1
	for i, line := range allLines {
		if line.index == h.cursor && line.index >= 0 {
			if selectedFirst < 0 {
				selectedFirst = i
			}
			selectedLast = i
		}
	}
	start := h.listScrollStart
	if selectedFirst >= 0 {
		if selectedFirst < start {
			start = selectedFirst
		}
		if selectedLast >= start+bodyHeight {
			start = selectedLast - bodyHeight + 1
		}
	}
	start = min(max(0, start), max(0, len(allLines)-bodyHeight))
	h.listScrollStart = start
	end := min(len(allLines), start+bodyHeight)
	for i := start; i < end; i++ {
		line := allLines[i]
		text := line.text
		if line.title {
			marker := "  "
			if line.index == h.cursor {
				marker = "› "
			} else if line.index == h.hoverRow {
				marker = "· "
			}
			text = marker + text
		}
		lines[bodyTop+i-start] = truncate(text, leftWidth)
	}
	if len(allLines) == 0 && bodyTop < height-1 {
		if h.loadErr != nil {
			lines[bodyTop] = truncate("Could not load sessions: "+cleanChatText(h.loadErr.Error()), leftWidth)
		} else {
			lines[bodyTop] = "No matching sessions"
		}
	}
	footer := "j/k move · Enter connect · / filter · 1–4 tabs · Esc back · q quit"
	lines[height-1] = truncate(footer, width)

	if hasDetails {
		rightX := leftWidth + 2
		rightWidth := width - rightX
		session := h.selectedSession()
		visible := false
		for _, index := range h.visibleSessionIndices() {
			visible = visible || index == h.cursor
		}
		if !visible {
			session = nil
		}
		right := make([]string, height)
		right[2] = "Preview"
		if session == nil {
			right[4] = "Choose a session"
		} else {
			right[4] = truncate(cleanChatText(sessionTitle(*session)), rightWidth)
			status := rowStatusWord(*session)
			if status == "" {
				status = "Running"
			}
			right[5] = status
			right[7] = "Agent  " + cleanChatText(harnessLabel(session.info.Harness))
			right[8] = "Project  " + cleanChatText(pathBase(session.info.Directory))
			right[9] = "Machine  " + cleanChatText(session.machineName)
			right[10] = "View  " + strings.ToUpper(sessionViewLabel(session.info.Transport)[:1]) + sessionViewLabel(session.info.Transport)[1:]
			right[11] = "Age  " + sessionAgeLabel(session.info.LastUsed, session.info.StartedAt)
			if session.info.Transport == "acp" && h.sessionExcerptID == session.info.ID && h.sessionExcerptMachine == session.machine {
				right[13] = "Recent conversation"
				switch {
				case h.sessionExcerptLoading:
					right[14] = "Loading…"
				case h.sessionExcerptErr != nil:
					right[14] = "Transcript unavailable"
				case len(h.sessionExcerpt) == 0:
					right[14] = "(no messages yet)"
				default:
					for i, excerpt := range h.sessionExcerpt {
						row := 14 + i
						if row >= height-4 {
							break
						}
						right[row] = "┃ " + truncate(cleanChatText(excerpt), max(1, rightWidth-2))
					}
				}
				if h.sessionExcerptErr != nil {
					right[min(height-4, 18)] = truncate("Excerpt unavailable: "+cleanChatText(h.sessionExcerptErr.Error()), rightWidth)
				}
			}
			if h.sessionActionID == session.info.ID && h.sessionActionPending {
				right[height-2] = "Stopping…"
			} else if session.info.State.Running {
				right[height-2] = "[ Connect ]    [ Stop ]"
			} else {
				right[height-2] = "[ Connect ]"
			}
			if h.sessionActionErr != nil && h.sessionActionID == session.info.ID {
				right[max(0, height-4)] = truncate(cleanChatText(h.sessionActionErr.Error()), rightWidth)
			}
		}
		for row := range lines {
			if row == height-1 {
				continue
			}
			left := truncate(lines[row], leftWidth)
			rightText := truncate(right[row], rightWidth)
			lines[row] = left + strings.Repeat(" ", max(0, leftWidth-lipgloss.Width(left))) + "│ " + rightText
		}
	}
	return strings.Join(lines, "\n")
}

func sessionTabAtX(x, selected int) int {
	_, bounds := sessionTabLine(selected)
	for i, bound := range bounds {
		if x >= bound[0] && x < bound[1] {
			return i
		}
	}
	return -1
}

func (h *home) handleSessionMouseClick(x, y int) tea.Cmd {
	if x < 0 || x >= h.width || y < 0 || y >= h.height {
		return nil
	}
	leftWidth := sessionListWidth(h.width)
	if y == 2 && x < leftWidth {
		if tab := sessionTabAtX(x, h.sessionTab); tab >= 0 {
			h.sessionTab = tab
			h.sessionFilterFocused = false
			h.listScrollStart = 0
			h.sessionActionErr = nil
			h.ensureSessionCursor()
		}
		return h.loadSessionExcerptCmd()
	}
	if y == 3 && x < leftWidth {
		h.sessionFilterFocused = true
		return nil
	}
	if h.width >= 100 && y == h.height-2 {
		rightX := leftWidth + 2
		session := h.selectedSession()
		visible := false
		for _, index := range h.visibleSessionIndices() {
			visible = visible || index == h.cursor
		}
		if !visible || session == nil {
			return nil
		}
		if x >= rightX && x < rightX+11 {
			h.sessionFilterFocused = false
			return h.openPreview(panelList)
		}
		if x >= rightX+15 && x < rightX+23 && session.info.State.Running && !h.sessionActionPending {
			h.sessionFilterFocused = false
			h.sessionActionID = session.info.ID
			h.sessionActionPending = true
			h.sessionActionErr = nil
			return h.stopSessionCmd(*session)
		}
		return nil
	}
	if x >= leftWidth || y < 5 || y >= h.height-1 {
		return nil
	}
	lineIndex := h.listScrollStart + y - 5
	lines := h.sessionLines()
	if lineIndex < 0 || lineIndex >= len(lines) || lines[lineIndex].index < 0 {
		return nil
	}
	h.cursor = lines[lineIndex].index
	h.sessionFilterFocused = false
	h.sessionActionErr = nil
	return h.openPreview(panelList)
}

func (h *home) sessionAtListLine(y int) int {
	if y < 5 || y >= h.height-1 {
		return -1
	}
	lineIndex := h.listScrollStart + y - 5
	lines := h.sessionLines()
	if lineIndex < 0 || lineIndex >= len(lines) {
		return -1
	}
	return lines[lineIndex].index
}

type sessionActionResultMsg struct {
	sessionID string
	err       error
}

func stopSessionPath(session listedSession) string {
	path := "/api/sessions/" + url.PathEscape(session.info.ID) + "/stop"
	if session.machine != "" && session.machine != "local" {
		path += "?" + url.Values{"node": {session.machine}}.Encode()
	}
	return path
}

func (h *home) stopSessionCmd(session listedSession) tea.Cmd {
	home := h.agentpackHome
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var err error
		if session.machine == "local" {
			err = network.StopWorker(ctx, home, session.info.ID)
		} else {
			_, err = network.NodeRequest(ctx, home, http.MethodPost, stopSessionPath(session))
		}
		return sessionActionResultMsg{sessionID: session.info.ID, err: err}
	}
}

func sessionTabLine(selected int) (string, [][2]int) {
	var line strings.Builder
	bounds := make([][2]int, len(sessionTabs))
	for i, label := range sessionTabs {
		if i > 0 {
			line.WriteString("   ")
		}
		start := line.Len()
		if i == selected {
			line.WriteByte('[')
			line.WriteString(label)
			line.WriteByte(']')
		} else {
			line.WriteString(label)
		}
		bounds[i] = [2]int{start + 1, line.Len() + 1}
	}
	return line.String(), bounds
}

func sessionRowIndex(rows []listRow, rowIndex int) int {
	index := 0
	for i := range rowIndex {
		if rows[i].kind == rowSession {
			index++
		}
	}
	return index
}

func (h *home) handleMouseClick(x, y int) tea.Cmd {
	if x < 0 || x >= h.width || y < 0 || y >= h.height {
		return nil
	}
	switch h.panel {
	case panelPalette:
		if y < 2 || y >= h.height-1 {
			return nil
		}
		row := y - 2
		if row >= len(paletteCommands) {
			return nil
		}
		h.paletteCursor = row
		_, cmd := h.paletteActivate()
		return cmd
	case panelNew:
		if y < 3 || y >= 7 || h.newSession.loading || h.newSession.starting || len(h.newSession.directories) == 0 {
			return nil
		}
		row := y - 3
		h.newSession.field = row
		label := [...]string{"Agent", "Project", "Machine", "View"}[row]
		if x >= 6+lipgloss.Width(label) {
			_, cmd := h.updateNewSession(tea.KeyPressMsg{Code: tea.KeyRight})
			return cmd
		}
		return nil
	case panelConfig:
		option := configOptionFor(h.previewConfigOptions, h.configCategory)
		if option == nil || h.configPending || y < 3 {
			return nil
		}
		row := y - 3
		if row < 0 || row >= len(option.Options) {
			return nil
		}
		h.configCursor = row
		if cmd := h.setConfigChoiceCmd(); cmd != nil {
			h.configPending = true
			return cmd
		}
	case panelHome:
		for field, bound := range h.homeSelectorBounds {
			if y == h.homeSelectorLines[field] && x >= bound[0] && x < bound[1] {
				h.homeField = field
				h.changeHomeSelector(1)
				return nil
			}
		}
		if y == h.homePromptLine {
			return nil
		}
		if line := y - h.homeListStart; line >= 0 && line < len(h.homeLines) {
			return h.openSessionRow(h.homeLines[line], panelHome)
		}
		return nil
	case panelList:
		return h.handleSessionMouseClick(x, y)
	case panelPreview:
		if h.previewCompose && y == h.previewConfigLine {
			categories := [...]string{"mode", "model", "thought_level"}
			for index, bound := range h.previewConfigRanges {
				if x >= bound[0] && x < bound[1] {
					return h.openConfigPicker(categories[index])
				}
			}
			return nil
		}
		if y < h.previewBodyTop {
			if x < 13 {
				h.panel = h.previewFrom
				return h.loadSessionExcerptCmd()
			}
			if h.sessionCount() > 1 && h.chatTextWidth() >= 28 {
				actions := "‹ prev · next ›"
				start := 2 + h.chatTextWidth() - lipgloss.Width(actions)
				switch {
				case x >= start && x < start+lipgloss.Width("‹ prev"):
					if h.cursor > 0 {
						h.cursor--
						return h.openPreview(h.previewFrom)
					}
				case x >= start+lipgloss.Width("‹ prev · ") && x < 2+h.chatTextWidth():
					if h.cursor < h.sessionCount()-1 {
						h.cursor++
						return h.openPreview(h.previewFrom)
					}
				}
			}
			return nil
		}
		if h.previewState == nil {
			return nil
		}
		line := h.previewStart + y - h.previewBodyTop
		actions := chatActionsAtLine(*h.previewState, h.chatTextWidth(), line, h.expandedTools)
		if actions.message != "" && y == h.previewActionLine {
			if x >= h.previewActionRanges[0][0] && x < h.previewActionRanges[0][1] {
				h.previewActionErr = nil
				h.previewActionStatus = "Copy sent via OSC 52; terminal clipboard support is required"
				return copyChatMessage(actions.message)
			}
			if x >= h.previewActionRanges[1][0] && x < h.previewActionRanges[1][1] && sessionCanFork(h.selectedSession()) {
				h.previewActionErr = nil
				h.previewActionStatus = "Forking session…"
				return h.forkSelectedSessionCmd()
			}
		}
		if actions.expandable != "" {
			h.expandedTools[actions.expandable] = !chatExpandableOpen(actions.expandable, h.expandedTools)
			return nil
		}
		if actions.image.SHA != "" {
			h.previewImageErr = nil
			return h.openChatImageCmd(actions.image)
		}
	}
	return nil
}

func (h *home) openSessionRow(rowIndex int, from panel) tea.Cmd {
	if rowIndex < 0 || rowIndex >= len(h.rows) || h.rows[rowIndex].kind != rowSession {
		return nil
	}
	h.cursor = sessionRowIndex(h.rows, rowIndex)
	return h.openPreview(from)
}

func (h *home) handleMouseWheel(message tea.MouseWheelMsg) {
	switch h.panel {
	case panelPreview:
		if message.Button == tea.MouseWheelUp {
			h.previewTail++
		} else if message.Button == tea.MouseWheelDown && h.previewTail > 0 {
			h.previewTail--
		}
	case panelHome:
		if message.Button == tea.MouseWheelUp && h.cursor > 0 {
			h.cursor--
		} else if message.Button == tea.MouseWheelDown && h.cursor < h.sessionCount()-1 {
			h.cursor++
		}
	case panelList:
		if message.Button == tea.MouseWheelUp {
			h.moveSessionCursor(-1)
		} else if message.Button == tea.MouseWheelDown {
			h.moveSessionCursor(1)
		}
	}
}

func (h *home) handleMouseMotion(x, y int) bool {
	hoverRow, previewHoverLine := -1, -1
	switch h.panel {
	case panelHome:
		if line := y - h.homeListStart; line >= 0 && line < len(h.homeLines) {
			hoverRow = h.sessionAtRow(h.homeLines[line])
		}
	case panelList:
		hoverRow = h.sessionAtListLine(y)
	case panelPreview:
		previewHoverLine = h.previewStart + y - h.previewBodyTop
	}
	if hoverRow == h.hoverRow && previewHoverLine == h.previewHoverLine {
		return false
	}
	h.hoverRow, h.previewHoverLine = hoverRow, previewHoverLine
	return true
}

func (h *home) sessionAtRow(rowIndex int) int {
	if rowIndex < 0 || rowIndex >= len(h.rows) || h.rows[rowIndex].kind != rowSession {
		return -1
	}
	return sessionRowIndex(h.rows, rowIndex)
}

func (h *home) chatTextWidth() int {
	width := max(1, h.width-4)
	if h.previewSide && h.width >= 100 && h.previewState != nil {
		width = max(1, width-min(30, max(24, h.width/4))-3)
	}
	return width
}

// homeSessionLine lays out one session in the prototype's columns, dropping
// machine, view and project as the column narrows. An unnamed session shows
// its agent as its name, so the agent column stays empty.
func homeSessionLine(session listedSession, width int) string {
	type column struct {
		width int
		value string
	}
	agent := harnessLabel(session.info.Harness)
	name := session.info.Name
	if name == "" {
		name, agent = agent, ""
	}
	project := pathBase(session.info.Directory)
	machine := session.machineName
	if machine == "" {
		machine = session.machine
	}
	view := "chat"
	if session.info.Transport != "acp" {
		view = "term"
	}
	tail := []column{{10, sessionAgeLabel(session.info.LastUsed, session.info.StartedAt)}}
	switch {
	case width >= 96:
		tail = append([]column{{10, agent}, {13, project}, {12, machine}, {5, view}}, tail...)
	case width >= 62:
		tail = append([]column{{10, agent}, {11, project}}, tail...)
	case width >= 52:
		tail = append([]column{{10, agent}}, tail...)
	}
	if session.info.ConversationState == "needs_you" {
		tail[len(tail)-1].value = "needs you"
	}
	used := 4
	for _, c := range tail {
		used += c.width + 1
	}
	var line strings.Builder
	line.WriteString(" " + dotGlyph(dotState(session.info)) + " ")
	line.WriteString(padCell(name, max(8, width-used)))
	for _, c := range tail {
		line.WriteString(" " + padCell(c.value, c.width))
	}
	return truncate(line.String(), width)
}

func padCell(value string, width int) string {
	value = truncate(value, width)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}
