package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/olegpustovit/agentpack/internal/gateway"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/staging"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
	"golang.org/x/sync/errgroup"
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
	return network.RunWorker(ctx, home, Version, launch, func(id string) error {
		_, err := fmt.Fprintln(runner.Stdout, id)
		return err
	})
}

func (runner Runner) runGateway(ctx context.Context, invocation Invocation) (int, error) {
	arguments := invocation.Args
	if len(arguments) > 0 && arguments[0] == "sharing" {
		return runner.runSharing(ctx, arguments[1:])
	}
	if len(arguments) == 0 {
		return 2, errors.New("expected gateway serve, start, stop, status, stop-agents, passwd, user, or reset-owner")
	}
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return 1, err
	}
	if arguments[0] == "main" {
		return runner.runGatewayMain(ctx, home, arguments[1:], boolFlag(invocation, "empty"))
	}
	if arguments[0] == "join" {
		return runner.runGatewayJoin(ctx, home, arguments[1:])
	}
	if arguments[0] == "whoami" || arguments[0] == "passwd" || arguments[0] == "user" || arguments[0] == "reset-owner" {
		return runner.runGatewayAccounts(ctx, home, arguments)
	}
	executable, err := os.Executable()
	if err != nil {
		return 1, err
	}
	action, args := arguments[0], arguments[1:]
	address := ""
	if action == "serve" {
		address = stringFlag(invocation, "listen")
	}
	if err = noArgs(args); err != nil {
		return 2, err
	}
	switch action {
	case "serve":
		return runner.serveGateway(ctx, home, executable, address)
	case "restart-after-update":
		// The public deployment owns its fixed listener and service environment.
		if runtime.GOOS == "linux" && os.Getenv("AGENTPACK_PUBLIC_URL") != "" {
			if userHome, err := os.UserHomeDir(); err == nil && executable == filepath.Join(userHome, ".local", "lib", "agentpack", "agentpack") {
				if err := exec.CommandContext(ctx, "systemctl", "--user", "restart", gatewayServiceName).Run(); err != nil {
					return 1, fmt.Errorf("restart deployed gateway: %w", err)
				}
				return 0, gateway.WaitForGateway(ctx, home)
			}
		}
		if !gatewayServiceAvailable(ctx, runtime.GOOS) {
			if err := gateway.StopGateway(ctx, home); err != nil {
				return 1, err
			}
		}
		return 0, runner.startGateway(ctx, home, executable)
	case "start":
		if err := runner.startGateway(ctx, home, executable); err != nil {
			return 1, err
		}
		return 0, runner.printGatewayClaim(ctx, home)
	case "stop":
		if gatewayServiceAvailable(ctx, runtime.GOOS) {
			return runner.runNetworkService(ctx, []string{"stop"})
		}
		if err := gateway.SetGatewayEnabled(home, false); err != nil {
			return 1, err
		}
		if _, err := gateway.NodeRequest(ctx, home, "GET", "/healthz"); err == nil {
			if err := gateway.StopGateway(ctx, home); err != nil {
				return 1, err
			}
		}
		fmt.Fprintln(runner.Stdout, `{"stopping":true}`)
		return 0, nil
	case "status", "stop-agents":
		method, path := "GET", "/api/sessions"
		if action == "stop-agents" {
			method, path = "POST", "/api/node/stop-agents"
		}
		data, err := gateway.NodeRequest(ctx, home, method, path)
		if err != nil {
			return 1, err
		}
		fmt.Fprintln(runner.Stdout, string(data))
		if action == "status" {
			return 0, runner.printGatewayClaim(ctx, home)
		}
		return 0, nil
	default:
		return 2, fmt.Errorf("unknown gateway action %q", action)
	}
}

func (runner Runner) startRemoteSession(ctx context.Context, home, executable string, info session.SessionInfo, forkSessionID string) (string, error) {
	if info.Transport == "shell" {
		program, arguments := loginShell(ctx)
		return network.StartWorker(ctx, home, executable, network.WorkerLaunch{
			Transport: info.Transport, Program: program, Arguments: arguments,
			Directory: info.Directory, Environment: append(os.Environ(), "TERM=xterm-256color", "SHELL="+program),
			Rows: 24, Columns: 80, Session: info,
		})
	}
	target, ok := launcherTarget(info.Harness)
	if !ok {
		return "", errors.New("unknown harness")
	}
	if !filepath.IsAbs(info.Directory) {
		return "", errors.New("folder must be absolute")
	}
	recent, err := network.Recent(home)
	if err != nil {
		return "", err
	}
	// The gateway has already authorized starts whose directory identity it supplied.
	// Verify that identity again before reading definitions or launching the worker.
	if info.FolderID != "" {
		identity, err := network.FolderIdentity(info.Directory)
		if err != nil || !network.SameFolder(info.FolderID, identity) {
			return "", errors.New("folder has changed before launch")
		}
	}
	browserStart := info.FolderID != "" && info.StartedBy != nil && info.StartedBy.CanStartNewFolder()
	known := false
	for _, entry := range recent {
		if entry.Directory == info.Directory {
			identity, err := network.FolderIdentity(info.Directory)
			if err != nil {
				return "", err
			}
			if !network.SameFolder(entry.FolderID, identity) {
				if browserStart {
					break
				}
				return "", errors.New("folder has changed; launch an agent locally before starting it remotely")
			}
			known = true
			info.DefinitionRoot = cmp.Or(info.DefinitionRoot, entry.DefinitionRoot)
			break
		}
	}
	// A fresh worktree has no launch yet: git must list it for a main folder launched here, whose
	// definition root it then shares.
	if !known {
		main, err := gateway.WorktreeMain(ctx, home, info.Directory)
		for _, entry := range recent {
			if err != nil {
				break
			}
			if entry.Directory == main && entry.FolderID != "" {
				if identity, err := network.FolderIdentity(main); err == nil && network.SameFolder(entry.FolderID, identity) {
					known = true
					info.DefinitionRoot = cmp.Or(info.DefinitionRoot, entry.DefinitionRoot, main)
				}
				break
			}
		}
	}
	if !known && !browserStart {
		return "", errors.New("folder has not been used before; launch an agent locally before starting it remotely")
	}
	// A folder that holds its own agentpack.toml records no separate definition root. The worker
	// then stages and launches from the folder itself, so the pre-launch sync must use it too; an
	// empty root would stage the gateway's working directory and leave the agent's home unbuilt.
	definitionRoot := info.DefinitionRoot
	if definitionRoot == "" {
		definitionRoot = info.Directory
	}
	stageInWorker := false
	if info.Transport == "acp" {
		stage := func(ctx context.Context) error {
			_, _, err := runner.Service.SyncForLaunchOptions(ctx, definitionRoot, packSync.SyncOptions{
				Mode: cmp.Or(info.Mode, mode.DefaultName), Target: &target, WorkspaceRoot: info.Directory,
			})
			return err
		}
		agent, err := registry.ByTarget(target)
		if err != nil {
			return "", err
		}
		stageInWorker = agent.ACPStageAtSession && os.Getenv("AGENTPACK_TEST_ACP") == ""
		if !stageInWorker {
			if _, statErr := os.Stat(executable); statErr == nil && os.Getenv("AGENTPACK_FULL_VERIFY") != "1" {
				err = prepareRemoteACP(ctx, stage, agent.PrepareACP)
			} else {
				err = stage(ctx)
			}
			if err != nil {
				return "", err
			}
		}
	}
	arguments := []string{"--workspace", info.Directory}
	if info.DefinitionRoot != "" {
		arguments = append(arguments, "--definition-root", info.DefinitionRoot)
	}
	if info.Mode != "" {
		arguments = append(arguments, "--mode", info.Mode)
	}
	arguments = append(arguments, info.Harness)
	return network.StartWorker(ctx, home, executable, network.WorkerLaunch{Transport: info.Transport, StageInWorker: stageInWorker, ForkSessionID: forkSessionID, Program: executable, Arguments: arguments, Directory: info.Directory, Environment: append(os.Environ(), "AGENTPACK_SESSION_WORKER=1"), Rows: 24, Columns: 80, Session: info})
}

// The adapter cache and project stage are independent. The worker sees the published generation.
func prepareRemoteACP(ctx context.Context, stage, adapter func(context.Context) error) error {
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error { return stage(ctx) })
	group.Go(func() error { return adapter(ctx) })
	return group.Wait()
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
	if err = gateway.EnsureNode(ctx, home, executable); err != nil {
		if errors.Is(err, gateway.ErrGatewayDisabled) {
			return runner.launch(ctx, packContext, invocation)
		}
		fmt.Fprintf(runner.Stderr, "warning: shared gateway unavailable (%v); continuing with a local session\n", err)
		return runner.launch(ctx, packContext, invocation)
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		runner.Launch = func(process *os.Process) (func(), error) {
			cleanup, err := gateway.RegisterPresence(home, Version, session.SessionInfo{Directory: packContext.WorkspaceRoot, DefinitionRoot: packContext.DefinitionRoot, Harness: invocation.Command, Mode: invocation.Global.Mode}, process)
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
	cliActor := session.LocalActor("cli")
	info := session.SessionInfo{Directory: packContext.WorkspaceRoot, DefinitionRoot: packContext.DefinitionRoot, Harness: invocation.Command, Mode: invocation.Global.Mode, StartedBy: &cliActor}
	// tmux pane closure sends SIGHUP; let the local attachment stop its worker.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGHUP, syscall.SIGTERM)
	defer stop()
	id, err := network.StartWorker(ctx, home, executable, network.WorkerLaunch{Program: executable, Arguments: arguments, Directory: directory, Environment: append(os.Environ(), "AGENTPACK_SESSION_WORKER=1"), Rows: uint16(rows), Columns: uint16(columns), Session: info})
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(runner.Stderr, "agentpack: session %s; local and browser input are shared\n", id)
	if url, err := gateway.GatewayURL(home); err == nil {
		fmt.Fprintln(runner.Stderr, "agentpack: browser "+url+" (sign in or claim the gateway)")
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

func (runner Runner) serveGateway(ctx context.Context, home, executable, address string) (int, error) {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	options := gateway.GatewayOptions{Home: home, Address: address, PublicURL: os.Getenv("AGENTPACK_PUBLIC_URL"), Assets: gateway.WebAssets(), Version: Version}
	options.Start = func(ctx context.Context, info session.SessionInfo, forkSessionID string) (string, error) {
		return runner.startRemoteSession(ctx, home, executable, info, forkSessionID)
	}
	options.Update = runner.updateGateway
	options.Packages = func(ctx context.Context, directory string) (gateway.ProjectContents, error) {
		packContext, err := resolveInvocationContext(Invocation{Global: Global{WorkspaceRoot: directory}}, true)
		if err != nil {
			return gateway.ProjectContents{}, err
		}
		locked, err := lockfile.LoadFromPath(paths.LockPath(packContext.DefinitionRoot))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return gateway.ProjectContents{}, err
		}
		// A folder without agentpack.toml still has the implicit default mode.
		project, _ := manifest.Load(packContext.DefinitionRoot)
		contents := projectContents(locked, project)
		// The Library matches a remote server to its shared login by URL and starts one from it.
		if plan, err := staging.CollectMCPPlan(packContext.WorkspaceRoot, locked, project, nil); err == nil {
			for i, server := range contents.MCP {
				contents.MCP[i].URL = mcp.LoginURL(plan.Entries[server.Name].Server)
			}
		}
		return contents, nil
	}
	options.Install = runner.installNetworkPackage
	options.InstallMCP = runner.installNetworkMCP
	var err error
	options.Library, err = runner.networkLibrary(home)
	if err != nil {
		return 1, err
	}
	serveErr := gateway.ServeGateway(ctx, options)
	return 0, errors.Join(serveErr, options.Library.Store.Close())
}
