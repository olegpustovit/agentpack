//go:build windows

package network

import (
	"golang.org/x/sys/windows"
	"golang.org/x/term"
	"os"
)

// Windows obtains the visible window dimensions from CONOUT$, not CONIN$.
func ConsoleSize(_, output *os.File) (int, int, error) {
	if output == nil {
		return 0, 0, os.ErrInvalid
	}
	return term.GetSize(int(output.Fd()))
}

func prepareConsoleOutput(output *os.File) (func(), error) {
	if output == nil {
		return func() {}, nil
	}
	handle := windows.Handle(output.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return func() {}, nil
	}
	if err := windows.SetConsoleMode(handle, mode|windows.ENABLE_PROCESSED_OUTPUT|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(handle, mode) }, nil
}
