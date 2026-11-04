package hub

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type mux struct {
	home *home
}

func newMux(agentpackHome string, load sessionLoader) *mux {
	return &mux{home: newHome(agentpackHome, load)}
}

func (m *mux) Init() tea.Cmd {
	return m.home.Init()
}

func (m *mux) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if mouse, ok := msg.(tea.MouseMsg); ok && m.terminalHeader() {
		event := mouse.Mouse()
		if _, ok := msg.(tea.MouseClickMsg); ok && event.Y == 0 && event.Button == tea.MouseLeft {
			switch {
			case event.X < 11:
				m.home.detachTerminal()
				m.home.panel = panelList
				return m, nil
			case m.home.sessionCount() > 1 && event.X >= m.home.width-16:
				changed := false
				if event.X < m.home.width-8 && m.home.cursor > 0 {
					m.home.cursor--
					changed = true
				} else if event.X >= m.home.width-8 && m.home.cursor < m.home.sessionCount()-1 {
					m.home.cursor++
					changed = true
				}
				if changed {
					return m, m.home.openPreview(panelList)
				}
			}
			return m, nil
		}
		if event.Y > 0 {
			event.Y--
			switch msg.(type) {
			case tea.MouseClickMsg:
				msg = tea.MouseClickMsg(event)
			case tea.MouseReleaseMsg:
				msg = tea.MouseReleaseMsg(event)
			case tea.MouseMotionMsg:
				msg = tea.MouseMotionMsg(event)
			case tea.MouseWheelMsg:
				msg = tea.MouseWheelMsg(event)
			}
		}
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && m.home.panel == panelPreview && !m.home.previewCompose && !m.terminalHeader() && m.home.sessionCount() > 1 {
		switch key.String() {
		case "[", "left":
			if m.home.cursor > 0 {
				m.home.cursor--
				return m, m.home.openPreview(m.home.previewFrom)
			}
			return m, nil
		case "]", "right":
			if m.home.cursor < m.home.sessionCount()-1 {
				m.home.cursor++
				return m, m.home.openPreview(m.home.previewFrom)
			}
			return m, nil
		}
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && (m.home.panel == panelList || m.home.panel == panelHome) && m.home.sessionCount() > 1 {
		switch key.String() {
		case "[", "left":
			if m.home.cursor > 0 {
				m.home.cursor--
			}
			return m, nil
		case "]", "right":
			if m.home.cursor < m.home.sessionCount()-1 {
				m.home.cursor++
			}
			return m, nil
		}
	}
	model, cmd := m.home.Update(msg)
	m.home = model.(*home)
	return m, cmd
}

func (m *mux) terminalHeader() bool {
	session := m.home.selectedSession()
	return m.home.panel == panelPreview && session != nil && session.info.Transport != "acp"
}

func (m *mux) View() tea.View {
	if !m.terminalHeader() {
		return m.home.View()
	}
	height := m.home.height
	m.home.height = max(1, height-1)
	inner := m.home.View()
	m.home.height = height
	v := tea.NewView(renderMuxHeader(m.home) + "\n" + inner.Content)
	v.AltScreen = inner.AltScreen
	v.MouseMode = tea.MouseModeAllMotion
	v.ReportFocus = true
	return v
}

func renderMuxHeader(h *home) string {
	mono := lipgloss.NoColor{}
	dim := lipgloss.NewStyle().Foreground(mono)
	bold := lipgloss.NewStyle().Bold(true).Foreground(mono)
	w := max(1, h.width)
	session := h.selectedSession()
	if session == nil {
		return truncate("‹ sessions", w)
	}
	left := dim.Render("‹ sessions")
	detail := "agentpack session · " + harnessLabel(session.info.Harness) + " · " + pathBase(session.info.Directory) + " · " + session.machineName
	right := "ctrl+] back"
	if h.sessionCount() > 1 && w >= 80 {
		right = "‹ prev  next ›"
	}
	if w < 56 {
		detail = harnessLabel(session.info.Harness) + " · " + pathBase(session.info.Directory)
	}
	if w < 40 {
		detail = sessionTitle(*session)
	}
	right = dim.Render(right)
	leftWidth, rightWidth := lipgloss.Width(left), lipgloss.Width(right)
	middleWidth := max(1, w-leftWidth-rightWidth-6)
	middle := bold.Render(truncate(detail, middleWidth))
	padding := max(1, w-leftWidth-rightWidth-lipgloss.Width(middle)-4)
	return left + " │ " + middle + strings.Repeat(" ", padding) + right
}
