package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/term"
)

func TestInteractiveTerminal(t *testing.T) {
	t.Run("nonFiles", func(t *testing.T) {
		if interactiveTerminal(nil, nil) || interactiveTerminal(strings.NewReader("x"), io.Discard) {
			t.Fatal("expected false for non-*os.File readers/writers")
		}
	})
	t.Run("pipes", func(t *testing.T) {
		inReader, inWriter, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer inReader.Close()
		defer inWriter.Close()
		outReader, outWriter, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer outReader.Close()
		defer outWriter.Close()
		if interactiveTerminal(inReader, outWriter) {
			t.Fatal("expected false for pipe fds")
		}
	})
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		t.Run("stdioTTY", func(t *testing.T) {
			if !interactiveTerminal(os.Stdin, os.Stdout) {
				t.Fatal("expected true when stdin and stdout are terminals")
			}
		})
	}
}
func TestRootHelpRemainsAvailableInsideAgentSession(t *testing.T) {
	t.Setenv("AGENTPACK_SESSION", "1")
	for _, args := range [][]string{{"ap", "--help"}, {"ap", "help"}} {
		var output strings.Builder
		runner := Runner{Stdin: strings.NewReader(""), Stdout: &output, Stderr: io.Discard}
		code, err := runner.Execute(context.Background(), args)
		if err != nil || code != 0 || !strings.Contains(output.String(), "Open the gateway web UI") {
			t.Fatalf("args=%v code=%d err=%v output=%q", args, code, err, output.String())
		}
	}
}

func TestOnlyGatewayExposesServiceControls(t *testing.T) {
	runner := Runner{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}
	root := runner.rootCommand(nil, new(int))
	for _, action := range []string{"start", "stop", "status"} {
		command, _, err := root.Find([]string{"gateway", action})
		if err != nil || command.Name() != action || command.Hidden {
			t.Fatalf("gateway %s missing: %v", action, err)
		}
		command, _, err = root.Find([]string{"service", action})
		if err == nil && command.Name() == action {
			t.Fatalf("service %s is still exposed", action)
		}
	}
	command, _, err := root.Find([]string{"service", "run"})
	if err != nil || command.Name() != "run" || !command.Hidden {
		t.Fatalf("hidden service run missing: %v", err)
	}
}
