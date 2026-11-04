package hub

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/olegpustovit/agentpack/internal/network"
)

type listedSession struct {
	info        network.SessionInfo
	machine     string
	machineName string
}

type sessionGroup struct {
	label   string
	subhead []sessionSubhead
}

type sessionSubhead struct {
	label  string
	parent string
	rows   []listedSession
}

type rowKind int

const (
	rowGroup rowKind = iota
	rowSubhead
	rowSession
)

type listRow struct {
	kind    rowKind
	label   string
	parent  string
	session *listedSession
}

func trimSlashes(path string) string {
	return strings.TrimRight(path, `/\`)
}

func pathBase(path string) string {
	path = trimSlashes(path)
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}

func parentName(directory string) string {
	parts := strings.FieldsFunc(directory, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2]
}

func harnessLabel(harness string) string {
	switch harness {
	case "agent", "cursor":
		return "Cursor CLI"
	case "codex":
		return "Codex"
	case "claude":
		return "Claude"
	case "agy":
		return "Antigravity"
	case "grok":
		return "Grok"
	case "omp":
		return "Oh My Pi"
	case "opencode":
		return "OpenCode"
	default:
		return harness
	}
}

func sessionTitle(s listedSession) string {
	if s.info.Name != "" {
		return s.info.Name
	}
	return harnessLabel(s.info.Harness)
}

func dotState(info network.SessionInfo) string {
	switch info.ConversationState {
	case "needs_you":
		return "needs_you"
	case "idle":
		if info.State.Running {
			return "idle"
		}
	case "working":
		if info.State.Running {
			return "running"
		}
	}
	if info.State.Running {
		return "running"
	}
	return "stopped"
}

func dotGlyph(state string) string {
	switch state {
	case "needs_you":
		return "!"
	case "idle":
		return "○"
	case "running":
		return "●"
	default:
		return "·"
	}
}

func rowStatusWord(s listedSession) string {
	switch dotState(s.info) {
	case "needs_you":
		return "Needs you"
	case "idle":
		return "Idle"
	case "running":
		return ""
	default:
		return "Stopped"
	}
}

func groupSessionsMachine(sessions []listedSession) []sessionGroup {
	directoriesByFolder := map[string]map[string]struct{}{}
	for _, session := range sessions {
		directory := trimSlashes(session.info.Directory)
		key := session.machine + "\x00" + pathBase(directory)
		set := directoriesByFolder[key]
		if set == nil {
			set = map[string]struct{}{}
			directoriesByFolder[key] = set
		}
		set[directory] = struct{}{}
	}

	type pending struct {
		label string
		heads map[string]*sessionSubhead
	}
	groups := map[string]*pending{}
	groupOrder := []string{}
	for _, session := range sessions {
		directory := trimSlashes(session.info.Directory)
		folder := pathBase(directory)
		groupKey := "machine\x00" + session.machine
		entry := groups[groupKey]
		if entry == nil {
			entry = &pending{label: session.machineName, heads: map[string]*sessionSubhead{}}
			groups[groupKey] = entry
			groupOrder = append(groupOrder, groupKey)
		}
		headKey := session.machine + "\x00" + directory
		head := entry.heads[headKey]
		if head == nil {
			shared := len(directoriesByFolder[session.machine+"\x00"+folder]) > 1
			parent := ""
			if shared {
				parent = parentName(directory)
			}
			head = &sessionSubhead{label: folder, parent: parent}
			entry.heads[headKey] = head
		}
		head.rows = append(head.rows, session)
	}

	sort.Strings(groupOrder)
	out := make([]sessionGroup, 0, len(groupOrder))
	for _, key := range groupOrder {
		entry := groups[key]
		subheads := make([]sessionSubhead, 0, len(entry.heads))
		for _, head := range entry.heads {
			subheads = append(subheads, *head)
		}
		sort.Slice(subheads, func(i, j int) bool {
			a, b := subheads[i].label, subheads[j].label
			if a == b {
				return subheads[i].parent < subheads[j].parent
			}
			return a < b
		})
		out = append(out, sessionGroup{label: entry.label, subhead: subheads})
	}
	return out
}

func flattenGroups(groups []sessionGroup) []listRow {
	rows := []listRow{}
	for _, group := range groups {
		rows = append(rows, listRow{kind: rowGroup, label: group.label})
		for _, sub := range group.subhead {
			rows = append(rows, listRow{kind: rowSubhead, label: sub.label, parent: sub.parent})
			for i := range sub.rows {
				row := sub.rows[i]
				rows = append(rows, listRow{kind: rowSession, session: &row})
			}
		}
	}
	return rows
}

func listedFromInfos(machine, machineName string, infos []network.SessionInfo) []listedSession {
	out := make([]listedSession, len(infos))
	for i, info := range infos {
		out[i] = listedSession{info: info, machine: machine, machineName: machineName}
	}
	return out
}

type sessionScreenLine struct {
	text  string
	index int
	title bool
}

const (
	sessionTabAll = iota
	sessionTabNeedsYou
	sessionTabRunning
	sessionTabHistory
)

var sessionTabs = []string{"All", "Needs you", "Running", "History"}

func sessionScreenLines(rows []listRow, tab int, filter string, width int) []sessionScreenLine {
	filter = strings.ToLower(strings.TrimSpace(filter))
	type indexedSession struct {
		session *listedSession
		index   int
	}
	groups := [...]struct {
		label string
		match func(listedSession) bool
	}{
		{"Needs you", func(s listedSession) bool { return dotState(s.info) == "needs_you" }},
		{"Running", func(s listedSession) bool { return dotState(s.info) == "running" }},
		{"Idle", func(s listedSession) bool { return dotState(s.info) == "idle" }},
		{"History", func(s listedSession) bool { return dotState(s.info) == "stopped" }},
	}
	var lines []sessionScreenLine
	for _, group := range groups {
		matches := make([]indexedSession, 0)
		sessionIndex := 0
		for _, row := range rows {
			if row.kind != rowSession {
				continue
			}
			session := row.session
			state := dotState(session.info)
			switch tab {
			case sessionTabNeedsYou:
				if state != "needs_you" {
					sessionIndex++
					continue
				}
			case sessionTabRunning:
				if !session.info.State.Running {
					sessionIndex++
					continue
				}
			case sessionTabHistory:
				if session.info.State.Running {
					sessionIndex++
					continue
				}
			}
			if group.match(*session) && sessionMatchesFilter(*session, filter) {
				matches = append(matches, indexedSession{session: session, index: sessionIndex})
			}
			sessionIndex++
		}
		if len(matches) == 0 {
			continue
		}
		lines = append(lines, sessionScreenLine{text: fmt.Sprintf("  %s · %d", group.label, len(matches)), index: -1})
		for _, entry := range matches {
			session := entry.session
			title := cleanChatText(sessionTitle(*session))
			if state := rowStatusWord(*session); state != "" {
				title += " · " + state
			}
			lines = append(lines, sessionScreenLine{text: title, index: entry.index, title: true})
			agent := cleanChatText(harnessLabel(session.info.Harness))
			project := cleanChatText(pathBase(session.info.Directory))
			machine := cleanChatText(session.machineName)
			view := strings.ToUpper(sessionViewLabel(session.info.Transport)[:1]) + sessionViewLabel(session.info.Transport)[1:]
			age := sessionAgeLabel(session.info.LastUsed, session.info.StartedAt)
			switch {
			case width < 52:
				lines = append(lines,
					sessionScreenLine{text: "    Agent " + agent, index: entry.index},
					sessionScreenLine{text: "    Project " + project, index: entry.index},
					sessionScreenLine{text: "    Machine " + machine, index: entry.index},
					sessionScreenLine{text: "    View " + view + " · Age " + age, index: entry.index})
			case width < 80:
				lines = append(lines,
					sessionScreenLine{text: "    Agent " + agent + " · Project " + project + " · Machine " + machine, index: entry.index},
					sessionScreenLine{text: "    View " + view + " · Age " + age, index: entry.index})
			default:
				lines = append(lines, sessionScreenLine{text: "    Agent " + agent + " · Project " + project + " · Machine " + machine + " · View " + view + " · Age " + age, index: entry.index})
			}
		}
	}
	return lines
}

func sessionMatchesFilter(session listedSession, filter string) bool {
	if filter == "" {
		return true
	}
	text := strings.ToLower(strings.Join([]string{
		sessionTitle(session), harnessLabel(session.info.Harness), session.info.Directory,
		session.machineName, sessionViewLabel(session.info.Transport), rowStatusWord(session), dotState(session.info),
	}, " "))
	return strings.Contains(text, filter)
}
func cleanSessionFilter(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r >= 0x7f && r <= 0x9f {
			return -1
		}
		return r
	}, value)
}

func (h *home) sessionLines() []sessionScreenLine {
	if h.loadErr != nil {
		return nil
	}
	return sessionScreenLines(h.rows, h.sessionTab, h.sessionFilter, sessionListWidth(h.width))
}

func (h *home) visibleSessionIndices() []int {
	lines := h.sessionLines()
	indices := make([]int, 0, h.sessionCount())
	for _, line := range lines {
		if line.title {
			indices = append(indices, line.index)
		}
	}
	return indices
}

func (h *home) ensureSessionCursor() {
	indices := h.visibleSessionIndices()
	if len(indices) == 0 {
		return
	}
	for _, index := range indices {
		if index == h.cursor {
			return
		}
	}
	h.cursor = indices[0]
}

func (h *home) moveSessionCursor(step int) {
	indices := h.visibleSessionIndices()
	if len(indices) == 0 {
		return
	}
	for i, index := range indices {
		if index == h.cursor {
			h.cursor = indices[min(max(0, i+step), len(indices)-1)]
			return
		}
	}
	if step < 0 {
		h.cursor = indices[len(indices)-1]
	} else {
		h.cursor = indices[0]
	}
}
