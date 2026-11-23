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
	home, err := os.MkdirTemp("", "agentpack-cli-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("AGENTPACK_HOME", home)
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
