package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestStateSelfcheck(t *testing.T) {
	var output bytes.Buffer
	runner := Runner{Stdin: strings.NewReader(""), Stdout: &output, Stderr: io.Discard}
	code, err := runner.Execute(context.Background(), []string{"state-selfcheck"})
	if err != nil || code != 0 || output.String() != "ok\n" {
		t.Fatalf("code=%d err=%v output=%q", code, err, output.String())
	}
	if command, _, err := runner.rootCommand(nil, new(int)).Find([]string{"state-selfcheck"}); err != nil || !command.Hidden {
		t.Fatal("self-check must remain hidden", err)
	}
}
