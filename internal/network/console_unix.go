//go:build !windows

package network

import (
	"golang.org/x/term"
	"os"
)

func ConsoleSize(input, _ *os.File) (int, int, error) {
	return term.GetSize(int(input.Fd()))
}

func prepareConsoleOutput(*os.File) (func(), error) { return func() {}, nil }

func SignalConsole(uint32) error { return ErrUnsupportedTerminal }
