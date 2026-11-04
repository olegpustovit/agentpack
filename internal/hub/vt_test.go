package hub

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

func TestSafeEmulatorRendersPTYBytes(t *testing.T) {
	emu := vt.NewSafeEmulator(40, 5)
	feedPTY(emu, []byte("hello-acp\r\n"))
	if !strings.Contains(emu.Render(), "hello-acp") {
		t.Fatalf("render = %q", emu.Render())
	}
}
