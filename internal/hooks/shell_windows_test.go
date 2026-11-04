package hooks

import (
	"context"
	"strings"
	"testing"
)

func TestShellCommandKeepsQuotesAndPluginRootForCmd(t *testing.T) {
	spec := ExecutionSpec{Handler: Handler{Kind: CommandHandler, Command: `echo "a b" ${CLAUDE_PLUGIN_ROOT}`}, WorkingDir: `C:\plugin`}
	output, err := RunCommand(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(output.Stdout)); got != `"a b" C:\plugin` {
		t.Fatalf("cmd received %q", got)
	}
}
