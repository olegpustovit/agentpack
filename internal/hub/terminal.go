package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/gorilla/websocket"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
)

type terminalPane struct {
	sessionID        string
	generation       uint64
	conn             *websocket.Conn
	emulator         *vt.SafeEmulator
	state            network.TerminalState
	offset           uint64
	panX             int
	panY             int
	viewportWidth    int
	viewportHeight   int
	err              error
	truncated        bool
	writeMu          sync.Mutex
	inputReadPending bool
}

type terminalNotice struct {
	Type   string                `json:"type"`
	State  network.TerminalState `json:"state"`
	Offset uint64                `json:"offset,omitempty"`
	Error  string                `json:"error,omitempty"`
}

type terminalConnectedMsg struct {
	sessionID  string
	generation uint64
	conn       *websocket.Conn
	state      network.TerminalState
	offset     uint64
	snapshot   []byte
	err        error
}

type terminalFrameMsg struct {
	generation uint64
	conn       *websocket.Conn
	kind       int
	data       []byte
	err        error
}

type terminalInputMsg struct {
	generation uint64
	data       []byte
	err        error
}

type terminalWriteMsg struct {
	generation uint64
	conn       *websocket.Conn
	err        error
}

type terminalRetryMsg struct{ generation uint64 }

func (h *home) connectTerminalCmd(sessionID string, state network.TerminalState, since, generation uint64) tea.Cmd {
	home := h.agentpackHome
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		conn, err := network.AttachTerminal(ctx, home, sessionID, since)
		cancel()
		if err == nil {
			return terminalConnectedMsg{sessionID: sessionID, generation: generation, conn: conn, state: state, offset: since}
		}
		if errors.Is(err, network.ErrSessionEnded) {
			state.Running = false
			state.Restarting = false
		}
		if !state.Running && !state.Restarting {
			tail, tailErr := network.TerminalReplayTail(context.Background(), home, sessionID, 32<<10)
			if tailErr == nil {
				return terminalConnectedMsg{sessionID: sessionID, generation: generation, state: state, snapshot: []byte(tail)}
			}
		}
		return terminalConnectedMsg{sessionID: sessionID, generation: generation, state: state, err: err}
	}
}

func (h *home) reconnectTerminalCmd() tea.Cmd {
	pane := h.terminal
	if pane == nil {
		return nil
	}
	pane.writeMu.Lock()
	conn := pane.conn
	pane.conn = nil
	pane.writeMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	return h.connectTerminalCmd(pane.sessionID, pane.state, pane.offset, pane.generation)
}

func terminalReadCmd(generation uint64, conn *websocket.Conn) tea.Cmd {
	return func() tea.Msg {
		kind, data, err := conn.ReadMessage()
		return terminalFrameMsg{generation: generation, conn: conn, kind: kind, data: data, err: err}
	}
}

func terminalInputReadCmd(pane *terminalPane) tea.Cmd {
	if pane.inputReadPending || pane.emulator == nil {
		return nil
	}
	pane.inputReadPending = true
	generation, emulator := pane.generation, pane.emulator
	return func() tea.Msg {
		buffer := make([]byte, 4096)
		n, err := emulator.Read(buffer)
		if n > 0 {
			return terminalInputMsg{generation: generation, data: buffer[:n]}
		}
		return terminalInputMsg{generation: generation, err: err}
	}
}

func terminalWriteCmd(pane *terminalPane, data []byte) tea.Cmd {
	generation := pane.generation
	return func() tea.Msg {
		pane.writeMu.Lock()
		conn := pane.conn
		if conn == nil {
			pane.writeMu.Unlock()
			return terminalWriteMsg{generation: generation, err: io.ErrClosedPipe}
		}
		err := conn.WriteMessage(websocket.BinaryMessage, data)
		pane.writeMu.Unlock()
		return terminalWriteMsg{generation: generation, conn: conn, err: err}
	}
}

func terminalRetryCmd(generation uint64) tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return terminalRetryMsg{generation} })
}

func terminalNoticeData(data []byte) (terminalNotice, error) {
	var notice terminalNotice
	if err := json.Unmarshal(data, &notice); err != nil {
		return terminalNotice{}, err
	}
	return notice, nil
}

func terminalScreenLines(emulator *vt.SafeEmulator, width, height, panX, panY int) []string {
	if emulator == nil {
		return []string{"Connecting to terminal…"}
	}
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	text := acp.StripControls(strings.ReplaceAll(emulator.Emulator.String(), "\r\n", "\n"))
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 0 || len(lines) == 1 && lines[0] == "" {
		return []string{"(empty terminal)"}
	}
	maxY := max(0, len(lines)-height)
	panY = max(0, min(panY, maxY))
	visible := lines[panY:min(len(lines), panY+height)]
	out := make([]string, 0, height)
	for _, line := range visible {
		cells := ansi.StringWidth(line)
		if cells > width {
			maxX := cells - width
			left := max(0, min(panX, maxX))
			line = ansi.CutWc(line, left, left+width)
		} else if cells < width {
			line = strings.Repeat(" ", (width-cells)/2) + line
		}
		out = append(out, line)
	}
	if len(out) < height {
		pad := (height - len(out)) / 2
		blank := make([]string, pad)
		for i := range blank {
			blank[i] = ""
		}
		out = append(blank, out...)
		for len(out) < height {
			out = append(out, "")
		}
	}
	return out
}

func newTerminalEmulator(state network.TerminalState) *vt.SafeEmulator {
	width, height := int(state.Columns), int(state.Rows)
	if width < 20 {
		width = 80
	}
	if height < 3 {
		height = 24
	}
	return vt.NewSafeEmulator(width, height)
}

func (h *home) detachTerminal() {
	h.terminalGeneration++
	pane := h.terminal
	h.terminal = nil
	if pane == nil {
		return
	}
	if pane.emulator != nil {
		_ = pane.emulator.Close()
	}
	pane.writeMu.Lock()
	conn := pane.conn
	pane.conn = nil
	pane.writeMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (h *home) installTerminal(msg terminalConnectedMsg) tea.Cmd {
	if msg.generation != h.terminalGeneration {
		if msg.conn != nil {
			_ = msg.conn.Close()
		}
		return nil
	}
	pane := h.terminal
	if pane == nil || pane.generation != msg.generation {
		pane = &terminalPane{
			sessionID: msg.sessionID, generation: msg.generation,
			emulator: newTerminalEmulator(msg.state), state: msg.state,
			offset: msg.offset,
		}
		h.terminal = pane
	}
	pane.err = msg.err
	if pane.emulator == nil {
		pane.emulator = newTerminalEmulator(msg.state)
	}
	pane.state = msg.state
	if len(msg.snapshot) > 0 {
		feedPTY(pane.emulator, msg.snapshot)
	}
	if msg.conn != nil {
		pane.writeMu.Lock()
		old := pane.conn
		pane.conn = msg.conn
		pane.writeMu.Unlock()
		if old != nil && old != msg.conn {
			_ = old.Close()
		}
		pane.err = nil
		if pane.inputReadPending {
			return tea.Batch(terminalReadCmd(pane.generation, msg.conn))
		}
		return tea.Batch(terminalReadCmd(pane.generation, msg.conn), terminalInputReadCmd(pane))
	}
	if msg.err != nil && (pane.state.Running || pane.state.Restarting) {
		return terminalRetryCmd(pane.generation)
	}
	return nil
}

func (h *home) handleTerminalFrame(msg terminalFrameMsg) tea.Cmd {
	pane := h.terminal
	if pane == nil || msg.generation != pane.generation || msg.conn != pane.conn {
		if msg.conn != nil {
			_ = msg.conn.Close()
		}
		return nil
	}
	if msg.err != nil {
		pane.err = msg.err
		pane.writeMu.Lock()
		pane.conn = nil
		pane.writeMu.Unlock()
		_ = msg.conn.Close()
		if pane.state.Running || pane.state.Restarting {
			return terminalRetryCmd(pane.generation)
		}
		return nil
	}
	if msg.kind == websocket.BinaryMessage {
		if pane.emulator != nil {
			if _, err := pane.emulator.Write(msg.data); err != nil {
				pane.err = err
			}
		}
		pane.offset += uint64(len(msg.data))
	} else if msg.kind == websocket.TextMessage {
		notice, err := terminalNoticeData(msg.data)
		if err != nil {
			pane.err = err
			return nil
		}
		if notice.Type == "attached" {
			if notice.Offset != pane.offset {
				pane.truncated = pane.offset != 0 || notice.Offset != 0
				if pane.truncated && pane.emulator != nil {
					_, _ = pane.emulator.Write([]byte("\x1b[2J\x1b[H"))
				}
			}
			pane.offset = notice.Offset
		}
		if notice.Type == "replay_complete" {
			pane.offset = notice.Offset
		}
		pane.state = notice.State
		if notice.Type == "error" {
			pane.err = errors.New(notice.Error)
		}
		if notice.Type == "ended" && !pane.state.Running && !pane.state.Restarting {
			pane.err = network.ErrSessionEnded
			pane.writeMu.Lock()
			pane.conn = nil
			pane.writeMu.Unlock()
			_ = msg.conn.Close()
			return nil
		}
	}
	return terminalReadCmd(pane.generation, pane.conn)
}

func (h *home) handleTerminalInput(msg terminalInputMsg) tea.Cmd {
	pane := h.terminal
	if pane == nil || msg.generation != pane.generation {
		return nil
	}
	pane.inputReadPending = false
	if len(msg.data) > 0 {
		return tea.Batch(terminalWriteCmd(pane, msg.data), terminalInputReadCmd(pane))
	}
	if msg.err != nil {
		if !errors.Is(msg.err, io.EOF) {
			pane.err = msg.err
		}
		return nil
	}
	return terminalInputReadCmd(pane)
}

func (h *home) handleTerminalWrite(msg terminalWriteMsg) tea.Cmd {
	pane := h.terminal
	if pane == nil || msg.generation != pane.generation || msg.conn != pane.conn {
		return nil
	}
	if msg.err != nil {
		pane.err = msg.err
	}
	return terminalInputReadCmd(pane)
}

func (h *home) sendTerminalKey(msg tea.KeyPressMsg) {
	if h.terminal != nil && h.terminal.emulator != nil && h.terminal.inputReadPending {
		h.terminal.emulator.SendKey(uv.KeyPressEvent(msg))
	}
}

func (h *home) sendTerminalPaste(text string) {
	if h.terminal != nil && h.terminal.emulator != nil && h.terminal.inputReadPending {
		h.terminal.emulator.Paste(text)
	}
}

func (h *home) sendTerminalFocus(focus bool) {
	if h.terminal == nil || h.terminal.emulator == nil || !h.terminal.inputReadPending {
		return
	}
	if focus {
		h.terminal.emulator.Focus()
	} else {
		h.terminal.emulator.Blur()
	}
}

func (h *home) terminalActive() bool {
	session := h.selectedSession()
	return h.panel == panelPreview && session != nil && session.info.Transport != "acp"
}

func (h *home) sendTerminalMouseAt(message tea.MouseMsg) {
	pane := h.terminal
	if pane == nil || pane.emulator == nil || !pane.inputReadPending || pane.viewportWidth < 1 || pane.viewportHeight < 1 {
		return
	}
	mouse := message.Mouse()
	mouse.X -= 2
	mouse.Y -= h.previewBodyTop
	if pane.emulator.Width() > pane.viewportWidth {
		mouse.X += pane.panX
	} else {
		mouse.X -= max(0, (pane.viewportWidth-pane.emulator.Width())/2)
	}
	if pane.emulator.Height() > pane.viewportHeight {
		mouse.Y += pane.panY
	} else {
		mouse.Y -= max(0, (pane.viewportHeight-pane.emulator.Height())/2)
	}
	if mouse.X < 0 || mouse.Y < 0 || mouse.X >= pane.emulator.Width() || mouse.Y >= pane.emulator.Height() {
		return
	}
	converted := uv.Mouse{X: mouse.X, Y: mouse.Y, Button: uv.MouseButton(mouse.Button), Mod: uv.KeyMod(mouse.Mod)}
	switch message.(type) {
	case tea.MouseClickMsg:
		pane.emulator.SendMouse(uv.MouseClickEvent(converted))
	case tea.MouseReleaseMsg:
		pane.emulator.SendMouse(uv.MouseReleaseEvent(converted))
	case tea.MouseMotionMsg:
		pane.emulator.SendMouse(uv.MouseMotionEvent(converted))
	case tea.MouseWheelMsg:
		pane.emulator.SendMouse(uv.MouseWheelEvent(converted))
	}
}

func (h *home) panTerminal(dx, dy int) {
	pane := h.terminal
	if pane == nil || pane.emulator == nil {
		return
	}
	pane.panX = max(0, min(pane.panX+dx, max(0, pane.emulator.Width()-pane.viewportWidth)))
	pane.panY = max(0, min(pane.panY+dy, max(0, pane.emulator.Height()-pane.viewportHeight)))
}
