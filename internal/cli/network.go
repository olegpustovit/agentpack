package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	"golang.org/x/term"
)

func (runner Runner) runSessionWorker(ctx context.Context, arguments []string) (int, error) {
	if err := noArgs(arguments); err != nil {
		return 2, err
	}
	decoder := json.NewDecoder(io.LimitReader(runner.Stdin, 1<<20))
	decoder.DisallowUnknownFields()
	var launch network.WorkerLaunch
	if err := decoder.Decode(&launch); err != nil {
		return 2, errors.New("invalid private worker launch specification")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return 2, errors.New("worker requires exactly one launch specification")
	}
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return 1, err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return network.RunWorker(ctx, home, launch, func(id string) error {
		_, err := fmt.Fprintln(runner.Stdout, id)
		return err
	})
}

func (runner Runner) runGateway(ctx context.Context, arguments []string) (int, error) {
	if len(arguments) > 0 && arguments[0] == "sharing" {
		return runner.runSharing(ctx, arguments[1:])
	}
	if len(arguments) == 0 {
		return 2, errors.New("expected gateway serve, start, stop, status, setup, or stop-agents")
	}
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return 1, err
	}
	executable, err := os.Executable()
	if err != nil {
		return 1, err
	}
	action, args := arguments[0], arguments[1:]
	address, args, _, err := takeFlag(args, "--listen")
	if err != nil {
		return 2, err
	}
	if err = noArgs(args); err != nil {
		return 2, err
	}
	switch action {
	case "serve":
		ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
		options := network.GatewayOptions{Home: home, Address: address, PublicURL: os.Getenv("AGENTPACK_PUBLIC_URL"), Assets: network.WebAssets()}
		options.Start = func(ctx context.Context, info network.SessionInfo) (string, error) {
			return runner.startRemoteSession(ctx, home, executable, info)
		}
		options.Packages = func(ctx context.Context, directory string) (network.ProjectContents, error) {
			packContext, err := resolveInvocationContext(Invocation{Global: Global{WorkspaceRoot: directory}}, true)
			if err != nil {
				return network.ProjectContents{}, err
			}
			locked, err := lockfile.Load(packContext.DefinitionRoot)
			if err != nil {
				return network.ProjectContents{}, err
			}
			return projectContents(locked), nil
		}
		options.Install = runner.installNetworkPackage
		options.Library, err = runner.networkLibrary(home)
		if err != nil {
			return 1, err
		}
		return 0, network.ServeGateway(ctx, options)
	case "start":
		if err := network.SetGatewayEnabled(home, true); err != nil {
			return 1, err
		}
		return 0, network.EnsureNode(ctx, home, executable)
	case "setup":
		token, err := os.ReadFile(filepath.Join(home, "network", "setup-token"))
		if err != nil {
			return 1, fmt.Errorf("start the gateway first; setup may already be complete: %w", err)
		}
		publicURL := os.Getenv("AGENTPACK_PUBLIC_URL")
		if publicURL == "" {
			publicURL, err = network.GatewayURL(home)
			if err != nil {
				return 1, err
			}
		}
		fmt.Fprintf(runner.Stdout, "Open %s and enter this one-time setup token:\n%s\n", publicURL, token)
		return 0, nil
	case "stop", "status", "stop-agents":
		method, path := "GET", "/api/sessions"
		if action == "stop" {
			if err := network.SetGatewayEnabled(home, false); err != nil {
				return 1, err
			}
			method, path = "POST", "/api/node/stop"
		}
		if action == "stop-agents" {
			method, path = "POST", "/api/node/stop-agents"
		}
		data, err := network.NodeRequest(ctx, home, method, path)
		if err != nil {
			return 1, err
		}
		fmt.Fprintln(runner.Stdout, string(data))
		return 0, nil
	default:
		return 2, fmt.Errorf("unknown gateway action %q", action)
	}
}

func (runner Runner) startRemoteSession(ctx context.Context, home, executable string, info network.SessionInfo) (string, error) {
	switch info.Harness {
	case "agy":
		return "", errors.New("Antigravity uses a desktop window; launch it locally (remote desktop is unavailable)")
	case "claude", "codex", "grok", "omp", "opencode", "agent":
	default:
		return "", errors.New("unknown harness")
	}
	if !filepath.IsAbs(info.Directory) {
		return "", errors.New("folder must be absolute")
	}
	recent, err := network.Recent(home)
	if err != nil {
		return "", err
	}
	known := false
	for _, entry := range recent {
		if entry.Directory == info.Directory {
			identity, err := network.FolderIdentity(info.Directory)
			if err != nil {
				return "", err
			}
			if entry.FolderID == "" || identity != entry.FolderID {
				return "", errors.New("folder has changed; launch an agent locally before starting it remotely")
			}
			known = true
			info.DefinitionRoot = entry.DefinitionRoot
			break
		}
	}
	if !known {
		return "", errors.New("folder has not been used before; launch an agent locally before starting it remotely")
	}
	arguments := []string{"--workspace", info.Directory}
	if info.DefinitionRoot != "" {
		arguments = append(arguments, "--definition-root", info.DefinitionRoot)
	}
	if info.Mode != "" {
		arguments = append(arguments, "--mode", info.Mode)
	}
	arguments = append(arguments, info.Harness)
	return network.StartWorker(ctx, home, executable, network.WorkerLaunch{Program: executable, Arguments: arguments, Directory: info.Directory, Environment: append(os.Environ(), "AGENTPACK_SESSION_WORKER=1"), Rows: 24, Columns: 80, Session: info})
}

func (runner Runner) launchManaged(ctx context.Context, packContext paths.Context, invocation Invocation, arguments []string) (int, error) {
	// Embedded callers and the inner launch retain the existing launch contract.
	if os.Getenv("AGENTPACK_SESSION_WORKER") == "1" || runner.Stdin != os.Stdin || runner.Stdout != os.Stdout {
		return runner.launch(ctx, packContext, invocation)
	}
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return 1, err
	}
	executable, err := os.Executable()
	if err != nil {
		return 1, err
	}
	if err = network.EnsureNode(ctx, home, executable); err != nil {
		if errors.Is(err, network.ErrGatewayDisabled) {
			return runner.launch(ctx, packContext, invocation)
		}
		fmt.Fprintf(runner.Stderr, "warning: shared gateway unavailable (%v); continuing with a local session\n", err)
		return runner.launch(ctx, packContext, invocation)
	}
	if invocation.Command == "agy" || !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		runner.Launch = func(process *os.Process) (func(), error) {
			cleanup, err := network.RegisterPresence(home, network.SessionInfo{Directory: packContext.WorkspaceRoot, DefinitionRoot: packContext.DefinitionRoot, Harness: invocation.Command, Mode: invocation.Global.Mode}, process)
			if err != nil {
				fmt.Fprintf(runner.Stderr, "warning: could not register native-stdio session: %v\n", err)
				return nil, nil
			}
			return cleanup, nil
		}
		return runner.launch(ctx, packContext, invocation)
	}
	columns, rows, err := network.ConsoleSize(os.Stdin, os.Stdout)
	if err != nil {
		return 1, err
	}
	directory, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	info := network.SessionInfo{Directory: packContext.WorkspaceRoot, DefinitionRoot: packContext.DefinitionRoot, Harness: invocation.Command, Mode: invocation.Global.Mode}
	// tmux pane closure sends SIGHUP; let the local attachment stop its worker.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGHUP, syscall.SIGTERM)
	defer stop()
	id, err := network.StartWorker(ctx, home, executable, network.WorkerLaunch{Program: executable, Arguments: arguments, Directory: directory, Environment: append(os.Environ(), "AGENTPACK_SESSION_WORKER=1"), Rows: uint16(rows), Columns: uint16(columns), Session: info})
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(runner.Stderr, "agentpack: session %s; local and browser input are shared\n", id)
	if url, err := network.GatewayURL(home); err == nil {
		fmt.Fprintln(runner.Stderr, "agentpack: browser "+url+" (first use: agentpack gateway setup)")
	}
	return network.RunTerminal(ctx, home, id, os.Stdin, runner.Stdout)
}

func (runner Runner) runSessionSignal(arguments []string) (int, error) {
	if len(arguments) != 1 {
		return 2, errors.New("session-signal requires a process ID")
	}
	pid, err := strconv.ParseUint(arguments[0], 10, 32)
	if err != nil || pid == 0 {
		return 2, errors.New("invalid session process ID")
	}
	if err := network.SignalConsole(uint32(pid)); err != nil {
		return 1, err
	}
	return 0, nil
}
