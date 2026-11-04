//go:build !linux && !darwin

package acp

import (
	"errors"
	"os/exec"
)

var errTerminalUnsupported = errors.New("ACP terminal is not supported on this platform")

func startManagedTerminal(*exec.Cmd) (*managedTerminal, error) {
	return nil, errTerminalUnsupported
}

func terminalReadDone(error) bool { return true }
