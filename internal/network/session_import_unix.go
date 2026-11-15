//go:build linux || darwin

package network

import (
	"errors"
	"syscall"
)

func workerProcessAlive(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return true, err
}
