package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
)

func TestDefinitionMutationChild(t *testing.T) {
	name := os.Getenv("AGENTPACK_MUTATION_CHILD")
	if name == "" {
		return
	}
	runner := NewRunner()
	runner.Stdout = io.Discard
	fmt.Fprintln(os.Stdout, "READY")
	code, err := runner.Run(context.Background(), []string{"--project-root", os.Getenv("AGENTPACK_MUTATION_ROOT"), "--quiet", "mode", "create", name})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

func TestDefinitionMutationsSerializeAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	if err := manifest.WriteStub(root, "test", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(root, "test", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	mutation, err := acquireDefinitionMutation(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer mutation.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type child struct {
		done   chan error
		stderr *bytes.Buffer
	}
	var children []child
	for _, name := range []string{"first", "second"} {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDefinitionMutationChild$")
		command.Env = append(os.Environ(), "AGENTPACK_MUTATION_ROOT="+root, "AGENTPACK_MUTATION_CHILD="+name)
		stderr := new(bytes.Buffer)
		command.Stderr = stderr
		stdout, err := command.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		ready, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil || ready != "READY\n" {
			t.Fatalf("child readiness: %q %v", ready, err)
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		children = append(children, child{done, stderr})
	}
	// Both children reached Run, but neither may read/edit the manifest yet.
	for _, child := range children {
		select {
		case err := <-child.done:
			t.Fatalf("mutation bypassed transaction: %v %s", err, child.stderr)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err := manifest.CreateMode(root, "parent"); err != nil {
		t.Fatal(err)
	}
	if err := mutation.Unlock(); err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if err := <-child.done; err != nil {
			t.Fatalf("child mutation: %v %s", err, child.stderr)
		}
	}
	project, err := manifest.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"parent", "first", "second"} {
		if _, ok := project.ModeDefinition(name); !ok {
			t.Fatalf("concurrent edit lost mode %s", name)
		}
	}
}

func TestGatewayInstallWaitsForCLIMutationAndCancels(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	if err := manifest.WriteStub(root, "test", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	mutation, err := acquireDefinitionMutation(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer mutation.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	runner := NewRunner()
	runner.Stdout, runner.Stderr = io.Discard, io.Discard
	err = runner.installNetworkPackage(ctx, root, "must-not-resolve", true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gateway did not wait for CLI transaction: %v", err)
	}
}
