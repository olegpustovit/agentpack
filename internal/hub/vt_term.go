package hub

import (
	"strings"

	"github.com/charmbracelet/x/vt"
	"github.com/olegpustovit/agentpack/internal/acp"
)

// feedPTY writes replay bytes into a bounded emulator for hub terminal preview (row 0 reserved by caller).
func feedPTY(emu *vt.SafeEmulator, pty []byte) {
	if len(pty) == 0 || emu == nil {
		return
	}
	_, _ = emu.Write(pty)
}

func terminalPreviewLines(pty []byte, cols, rows int) []string {
	if cols < 20 {
		cols = 80
	}
	if rows < 3 {
		rows = max(3, rows)
	}
	emu := vt.NewSafeEmulator(cols, rows)
	feedPTY(emu, pty)
	text := acp.StripControls(emu.Emulator.String())
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	for i, line := range lines {
		lines[i] = clipLine(strings.TrimRight(line, " "), cols)
	}
	if len(lines) == 0 {
		return []string{"(empty terminal)"}
	}
	return lines
}
