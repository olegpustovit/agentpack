package grok

import (
	"os"
	"path/filepath"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

func nativeHome() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(home, ".grok"), true
}
func prepareHistory(staged, native string) error {
	return base.LinkDurableDirectory(filepath.Join(native, "sessions"), filepath.Join(staged, "sessions"))
}
func verifyHistory(staged, native string) error {
	if !base.DurablePathMatches(filepath.Join(staged, "sessions"), filepath.Join(native, "sessions")) {
		return os.ErrInvalid
	}
	return nil
}
