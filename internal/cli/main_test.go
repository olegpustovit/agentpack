package cli

import (
	"os"
	"testing"

	"github.com/olegpustovit/agentpack/internal/paths"
)

// TestMain restores the user environment variables some Windows shells drop,
// which the tests read the user home through, and gives the package its own
// AGENTPACK_HOME so a test that runs a failing command never writes
// diagnostics, state or caches into the developer's real home.
func TestMain(m *testing.M) {
	paths.RestoreUserEnvironment()
	// A child process re-run from a test inherits the parent's home, so both share its locks.
	if os.Getenv("AGENTPACK_TEST_HOME") != "" {
		os.Exit(m.Run())
	}
	home, err := os.MkdirTemp("", "agentpack-cli-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("AGENTPACK_HOME", home)
	os.Setenv("AGENTPACK_TEST_HOME", home)
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
