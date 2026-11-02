package paths

import (
	"os"
	"strings"
	"testing"
)

func TestRestoreUserEnvironmentFillsMissingVariables(t *testing.T) {
	t.Setenv("USERPROFILE", "")
	t.Setenv("LOCALAPPDATA", "")
	t.Setenv("AGENTPACK_HOME", "")
	RestoreUserEnvironment()
	if os.Getenv("USERPROFILE") == "" || os.Getenv("LOCALAPPDATA") == "" {
		t.Fatalf("not restored: %q %q", os.Getenv("USERPROFILE"), os.Getenv("LOCALAPPDATA"))
	}
	home, err := UserAgentpackHome()
	if err != nil || !strings.HasSuffix(home, "agentpack") {
		t.Fatalf("home = %q, %v", home, err)
	}
}

func TestRestoreUserEnvironmentKeepsSetVariables(t *testing.T) {
	t.Setenv("USERPROFILE", `C:\custom`)
	RestoreUserEnvironment()
	if got := os.Getenv("USERPROFILE"); got != `C:\custom` {
		t.Fatalf("changed to %q", got)
	}
}
