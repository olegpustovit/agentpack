package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/term"
)

func TestHubTTY(t *testing.T) {
	t.Run("nonFiles", func(t *testing.T) {
		if hubTTY(nil, nil) || hubTTY(strings.NewReader("x"), io.Discard) {
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
		if hubTTY(inReader, outWriter) {
			t.Fatal("expected false for pipe fds")
		}
	})
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		t.Run("stdioTTY", func(t *testing.T) {
			if !hubTTY(os.Stdin, os.Stdout) {
				t.Fatal("expected true when stdin and stdout are terminals")
			}
		})
	}
}
func TestHubHelpRemainsAvailableInsideAgentSession(t *testing.T) {
	t.Setenv("AGENTPACK_SESSION", "1")
	for _, args := range [][]string{{"ap", "--help"}, {"ap", "help"}} {
		var output strings.Builder
		runner := Runner{Stdin: strings.NewReader(""), Stdout: &output, Stderr: io.Discard}
		code, err := runner.Execute(context.Background(), args)
		if err != nil || code != 0 || !strings.Contains(output.String(), "Open the agentpack hub") {
			t.Fatalf("args=%v code=%d err=%v output=%q", args, code, err, output.String())
		}
	}
}

func TestHubRefusesInsideAgentSession(t *testing.T) {
	t.Setenv("AGENTPACK_SESSION", "1")
	runner := Runner{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	root := runner.rootCommand(nil, new(int))
	err := root.RunE(root, nil)
	if err == nil || !strings.Contains(err.Error(), "hub is unavailable") {
		t.Fatalf("expected refusal, got %v", err)
	}
}

func TestServiceStartIsTheOnlyInstallEntryPoint(t *testing.T) {
	runner := Runner{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}
	root := runner.rootCommand(nil, new(int))
	service, _, err := root.Find([]string{"service", "start"})
	if err != nil || service.Name() != "start" {
		t.Fatalf("service start command missing: command=%v err=%v", service, err)
	}
	service, _, err = root.Find([]string{"service", "install"})
	if err == nil && service.Name() == "install" {
		t.Fatal("separate service install command should not be exposed")
	}
}
