package hub

import "testing"

func TestTerminalPreviewLines(t *testing.T) {
	lines := terminalPreviewLines([]byte("line-one\r\nline-two\r\n"), 40, 5)
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	if lines[0] != "line-one" || lines[1] != "line-two" {
		t.Fatalf("lines = %v", lines)
	}
}
