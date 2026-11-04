package hub

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/olegpustovit/agentpack/internal/network"
)

func TestMuxHeaderSingleSession(t *testing.T) {
	h := newHome("", func(_ context.Context, _ string) ([]listedSession, error) {
		return listedFromInfos("local", "local", []network.SessionInfo{{
			ID: "only", Name: "Solo", Harness: "cursor", Directory: "/work",
			State: network.TerminalState{Running: true},
		}}), nil
	})
	h.width = 80
	h.applySessions(listedFromInfos("local", "local", []network.SessionInfo{{
		ID: "only", Name: "Solo", Harness: "cursor", Directory: "/work",
		State: network.TerminalState{Running: true},
	}}))
	line := ansi.Strip(renderMuxHeader(h))
	if strings.Contains(line, "‹ prev") || strings.Contains(line, "next ›") {
		t.Fatalf("single-session header should omit navigation hints: %q", line)
	}
	if !strings.Contains(line, "‹ sessions") {
		t.Fatalf("header = %q", line)
	}
	if !strings.Contains(line, "Cursor CLI") || !strings.Contains(line, "work") {
		t.Fatalf("header = %q", line)
	}
}

func TestMuxHeaderMultipleSessions(t *testing.T) {
	h := testHome()
	line := ansi.Strip(renderMuxHeader(h))
	if !strings.Contains(line, "‹ prev") || !strings.Contains(line, "next ›") {
		t.Fatalf("expected prev/next hints: %q", line)
	}
	if !strings.Contains(line, "Cursor CLI") || !strings.Contains(line, "agentpack") {
		t.Fatalf("header = %q", line)
	}
}

func TestMuxViewShowsHeaderForTerminalPreview(t *testing.T) {
	m := newMux("", func(_ context.Context, _ string) ([]listedSession, error) {
		return listedFromInfos("local", "devbox", fixtureSessions()), nil
	})
	m.home.machineName = "devbox"
	m.home.applySessions(listedFromInfos("local", "devbox", fixtureSessions()))
	m.home.width, m.home.height = 120, 36
	m.home.panel = panelPreview
	m.home.previewLines = []string{"terminal preview"}
	first := strings.SplitN(ansi.Strip(m.View().Content), "\n", 2)[0]
	if !strings.HasPrefix(first, "‹ sessions") {
		t.Fatalf("first line = %q", first)
	}
}

func TestMuxBracketKeysMoveCursor(t *testing.T) {
	m := newMux("", func(_ context.Context, _ string) ([]listedSession, error) {
		return listedFromInfos("local", "devbox", fixtureSessions()), nil
	})
	m.home.applySessions(listedFromInfos("local", "devbox", fixtureSessions()))
	m.home.cursor = 0
	m.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	if m.home.cursor != 1 {
		t.Fatalf("cursor = %d", m.home.cursor)
	}
	m.Update(tea.KeyPressMsg{Code: '[', Text: "["})
	if m.home.cursor != 0 {
		t.Fatalf("cursor = %d", m.home.cursor)
	}
}
func TestChatPreviewHeaderNavigatesSessionsAndBack(t *testing.T) {
	m := newMux("", func(_ context.Context, _ string) ([]listedSession, error) {
		return listedFromInfos("local", "devbox", fixtureSessions()), nil
	})
	m.home.applySessions(listedFromInfos("local", "devbox", fixtureSessions()))
	for i := range m.home.rows {
		if m.home.rows[i].session != nil {
			m.home.rows[i].session.info.Transport = "acp"
		}
	}
	m.home.width, m.home.height = 120, 36
	m.home.panel, m.home.previewFrom = panelPreview, panelList
	screen := ansi.Strip(m.View().Content)
	if strings.Count(screen, "‹ sessions") != 1 || !strings.Contains(screen, "‹ prev · next ›") {
		t.Fatalf("chat header = %q", strings.SplitN(screen, "\n", 2)[0])
	}
	m.home.previewCompose = true
	m.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	if m.home.cursor != 0 {
		t.Fatalf("session navigation changed the cursor while composing: %d", m.home.cursor)
	}
	m.home.previewCompose = false
	start := 2 + m.home.chatTextWidth() - ansi.StringWidth("‹ prev · next ›")
	m.Update(tea.MouseClickMsg{X: start + ansi.StringWidth("‹ prev · "), Y: 1, Button: tea.MouseLeft})
	if m.home.cursor != 1 {
		t.Fatalf("next header click selected %d, want 1", m.home.cursor)
	}
	m.Update(tea.KeyPressMsg{Code: '[', Text: "["})
	if m.home.cursor != 0 {
		t.Fatalf("previous key selected %d, want 0", m.home.cursor)
	}
	m.Update(tea.MouseClickMsg{X: 3, Y: 1, Button: tea.MouseLeft})
	if m.home.panel != panelList {
		t.Fatalf("back click selected panel %d, want sessions", m.home.panel)
	}
}
