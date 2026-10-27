//go:build !linux && !darwin && !windows

package network

import (
	"os"
	"os/exec"
)

type terminal struct{ *os.File }

func startTerminal(*exec.Cmd, uint16, uint16) (*terminal, error) { return nil, ErrUnsupportedTerminal }
func resizeTerminal(*terminal, uint16, uint16) error             { return ErrUnsupportedTerminal }
func terminalEOF(error) bool                                     { return false }
func (*terminal) wait(command *exec.Cmd) error                   { return command.Wait() }
func (*terminal) finish(<-chan struct{})                         {}
func (*terminal) terminate(process *os.Process, _ bool) error    { return process.Kill() }
func ConfigureDetached(*exec.Cmd)                                {}
