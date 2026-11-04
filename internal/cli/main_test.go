package cli

import (
	"os"
	"testing"

	"github.com/olegpustovit/agentpack/internal/paths"
)

// TestMain restores the user environment variables some Windows shells drop,
// which the tests read the user home through.
func TestMain(m *testing.M) {
	paths.RestoreUserEnvironment()
	os.Exit(m.Run())
}
