package cli

import (
	"context"
	"errors"
	"image/color"
	"io"
	"os"
	"slices"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/fang"
	"github.com/olegpustovit/agentpack/internal/gateway"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Execute runs agentpack through Cobra's command model and Fang's terminal
// presentation, including help, completion, version, and error rendering.
func (runner Runner) Execute(ctx context.Context, arguments []string) (int, error) {
	ctx = obs.ProcessTrace(ctx)
	ctx = gateway.WithPeerCache(ctx, "")
	if len(arguments) == 1 && arguments[0] == "-V" {
		arguments = slices.Clone(arguments)
		arguments[0] = "--version"
	}
	exitCode := 0
	root := runner.rootCommand(arguments, &exitCode)
	root.SetArgs(arguments)
	root.SetIn(runner.Stdin)
	root.SetOut(runner.Stdout)
	root.SetErr(runner.Stderr)
	err := fang.Execute(ctx, root, fang.WithVersion(Version), fang.WithoutManpage(), fang.WithColorSchemeFunc(monochromeColorScheme))
	if err != nil && exitCode == 0 {
		exitCode = 2
	}
	if err != nil || exitCode != 0 {
		runner.recordFailure(ctx, root, arguments, exitCode, err)
	}
	return exitCode, err
}

func monochromeColorScheme(_ lipgloss.LightDarkFunc) fang.ColorScheme {
	foreground := lipgloss.NoColor{}
	return fang.ColorScheme{
		Base: foreground, Title: foreground, Description: foreground,
		Codeblock: foreground, Program: foreground, DimmedArgument: foreground,
		Comment: foreground, Flag: foreground, FlagDefault: foreground,
		Command: foreground, QuotedString: foreground, Argument: foreground,
		Help: foreground, Dash: foreground,
		ErrorHeader:  [2]color.Color{foreground, foreground},
		ErrorDetails: foreground,
	}
}

func (runner Runner) rootCommand(original []string, exitCode *int) *cobra.Command {
	rootHub := func(command *cobra.Command, args []string) error {
		if len(args) > 0 {
			return command.Help()
		}
		if interactiveTerminal(runner.Stdin, runner.Stdout) {
			return runner.openGateway(command.Context())
		}
		return command.Help()
	}
	root := &cobra.Command{
		Use:           "ap",
		Short:         "Pin skills and plugins for every coding agent",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          rootHub,
	}
	ap := &cobra.Command{
		Use:           "ap",
		Short:         "Open the gateway web UI",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          rootHub,
	}
	root.AddCommand(ap)
	root.Version = Version
	root.SetVersionTemplate("agentpack {{.Version}}\n")

	var global Global
	flags := root.PersistentFlags()
	flags.StringVar(&global.ProjectRoot, "project-root", "", "legacy single root (definition+workspace when no split flags)")
	flags.StringVar(&global.DefinitionRoot, "definition-root", "", "external directory holding agentpack.toml and pack.lock")
	flags.StringVar(&global.Env, "env", "", "external environment name or path (fails if missing; no fallthrough)")
	flags.StringVar(&global.WorkspaceRoot, "workspace", "", "checkout / agent cwd for .agents and overlays")
	flags.BoolVar(&global.StrictExternal, "strict-external", false, "refuse workspace overlay writes (Cursor/Agy)")
	flags.BoolVarP(&global.Quiet, "quiet", "q", false, "suppress status output")
	flags.BoolVar(&global.NoProgress, "no-progress", false, "disable progress output")
	flags.BoolVar(&global.Yolo, "yolo", false, "enable unattended harness execution")
	flags.StringVar(&global.Mode, "mode", "", "project-local mode to stage")
	flags.BoolVar(&global.Debug, "debug", false, "print launch diagnostics")
	flags.BoolVar(&global.Proxy, "proxy", false, "route Claude through the agentpack proxy")
	flags.BoolVarP(&global.Version, "version", "v", false, "print the agentpack version")

	leaf := func(use, short string) *cobra.Command {
		command := &cobra.Command{Use: use, Short: short}
		command.RunE = func(command *cobra.Command, args []string) error {
			code, err := runner.dispatch(command.Context(), leafInvocation(command, global, args))
			*exitCode = code
			return err
		}
		return command
	}
	// Launchers keep their arguments verbatim for the harness; only the
	// product-owned extractor reads agentpack options out of them.
	launcher := func(use, short string) *cobra.Command {
		command := &cobra.Command{Use: use, Short: short, DisableFlagParsing: true}
		command.RunE = func(command *cobra.Command, args []string) error {
			if hasBeforeDoubleDash(args, "--help", "-h") {
				return command.Help()
			}
			code, err := runner.runLauncher(command.Context(), original)
			*exitCode = code
			return err
		}
		return command
	}
	addDefinitionCommands(root, leaf)
	root.AddCommand(runner.logsCommand())
	worker := leaf("session-worker", "Own a private agent terminal session")
	worker.Hidden = true
	signal := leaf("session-signal PID", "Signal a private Windows agent console")
	signal.Hidden = true
	root.AddCommand(worker, signal)
	addGatewayCommands(root, leaf)
	agent := launcher("agent [ARGS...]", "Launch Cursor Agent with a staged HOME")
	agent.Aliases = []string{"cursor-agent"}
	root.AddCommand(
		launcher("claude [ARGS...]", "Launch Claude Code with staged plugins"),
		launcher("opencode [ARGS...]", "Launch OpenCode with staged configuration"),
		launcher("codex [ARGS...]", "Launch Codex with a staged CODEX_HOME"),
		launcher("omp [ARGS...]", "Launch Oh My Pi with staged plugins"),
		launcher("grok [ARGS...]", "Launch Grok with a staged GROK_HOME"),
		launcher("agy [ARGS...]", "Launch Antigravity with the project workspace"),
		agent,
	)

	addMCPCommands(root, leaf)

	mode := &cobra.Command{Use: "mode", Short: "Manage project-local staging modes"}
	mode.AddCommand(
		leaf("list", "List modes"),
		leaf("show NAME", "Show a mode definition"),
		leaf("create NAME", "Create a mode"),
		leaf("delete NAME", "Delete a mode"),
		leaf("enable NAME [SELECTOR...]", "Enable capabilities in a mode"),
		leaf("disable NAME [SELECTOR...]", "Disable capabilities in a mode"),
		leaf("base NAME <all|none>", "Set a mode's default selection"),
	)
	root.AddCommand(mode)

	addEnvironmentCommands(root, leaf)

	eject := leaf("eject [AGENT...]", "Write staged content into the project for IDEs and agents started without agentpack")
	eject.Flags().Bool("remove", false, "take out everything the last eject wrote")
	root.AddCommand(eject)

	extra := &cobra.Command{Use: "extra", Short: "Optional, non-core commands"}
	syncClaude := leaf("sync-claude", "Reconcile .claude/skills and .agents/skills")
	syncClaude.Flags().Bool("dry-run", false, "show what would change")
	extra.AddCommand(syncClaude)
	root.AddCommand(extra)

	addInternalCommands(root, leaf)
	return root
}

func addStringFlag(command *cobra.Command, name, placeholder, usage string) {
	command.Flags().String(name, "", usage+" ("+placeholder+")")
}

func addStringArrayFlag(command *cobra.Command, name, placeholder, usage string) {
	command.Flags().StringArray(name, nil, usage+" ("+placeholder+", repeatable)")
}

func addDefinitionCommands(root *cobra.Command, leaf func(use, short string) *cobra.Command) {
	init := leaf("init", "Create agentpack.toml and pack.lock")
	addStringFlag(init, "name", "NAME", "project name")
	addStringFlag(init, "version", "VERSION", "project version")
	lock := leaf("lock", "Resolve the manifest and refresh pack.lock")
	lock.Flags().Bool("update", false, "refresh floating dependency pins")
	lock.Flags().Bool("allow-unpinned-mcp", false, "record an MCP server as unpinned when its registry is unreachable")
	add := leaf("add SPEC", "Add and pin a package dependency")
	addStringFlag(add, "plugin", "NAME", "add only the plugin of this name from the marketplace catalog at SPEC (without it, every plugin the catalog lists is added)")
	remove := leaf("remove SPEC", "Remove a direct package dependency")
	update := leaf("update [SPEC...]", "Refresh one dependency, or every floating dependency")
	list := leaf("list", "List resolved package dependencies")
	syncCommand := leaf("sync", "Ensure cache and staging match pack.lock")
	syncCommand.Flags().Bool("dry-run", false, "show what would change")
	syncCommand.Flags().Bool("verify-only", false, "verify existing cache and staging")
	syncCommand.Flags().Bool("update-lock", false, "refresh the lock before staging")
	syncCommand.Flags().Bool("repair", false, "re-fetch cache entries that fail content verification")
	preflight := leaf("preflight", "Pure offline check of a locked environment (no mutation)")
	preflight.Flags().Bool("json", false, "emit a JSON report")
	addStringFlag(preflight, "agent", "TARGET", "harness target to check (claude, opencode, ...)")
	addStringFlag(preflight, "target", "TARGET", "alias for --agent")
	preflight.Flags().MarkHidden("target")
	addStringFlag(preflight, "policy", "local|ci", "inheritance policy")
	addStringFlag(preflight, "contract", "PATH", "versioned contract.json (default: <definition>/contract.json)")
	addStringFlag(preflight, "receipt", "ID", "compare a prior probe receipt (freshness / observed coverage)")
	probe := leaf("probe", "Run an explicit native observation probe (writes a receipt)")
	probe.Flags().Bool("json", false, "emit the receipt JSON")
	addStringFlag(probe, "agent", "claude|codex", "native adapter to probe")
	addStringFlag(probe, "target", "claude|codex", "alias for --agent")
	probe.Flags().MarkHidden("target")
	addStringFlag(probe, "policy", "local|ci", "policy to bind into receipt freshness")

	configCmd := &cobra.Command{Use: "config", Short: "Compare configuration observation receipts"}
	configCompare := leaf("compare RECEIPT_A RECEIPT_B", "Diff two probe receipts")
	configCompare.Flags().Bool("json", false, "emit JSON comparison")
	configCmd.AddCommand(configCompare)
	supportCmd := &cobra.Command{Use: "support", Short: "Export redacted diagnostic capsules"}
	supportExport := leaf("export RECEIPT_ID", "Write a redacted support capsule")
	addStringFlag(supportExport, "output", "PATH", "destination JSON file")
	supportExport.Flags().Bool("json", false, "also emit the capsule on stdout")
	supportCmd.AddCommand(supportExport)

	root.AddCommand(init, lock, add, remove, update, list, syncCommand, preflight, probe, configCmd, supportCmd)
}

func addGatewayCommands(root *cobra.Command, leaf func(use, short string) *cobra.Command) {
	gateway := &cobra.Command{Use: "gateway", Short: "Manage shared agent terminals and browser access"}
	service := &cobra.Command{Use: "service", Hidden: true}
	for _, action := range []string{"run"} {
		command := leaf(action, "User service "+action)
		command.Hidden = action == "run"
		service.AddCommand(command)
	}
	sharing := &cobra.Command{Use: "sharing", Short: "Manage personal Tailscale access"}
	for _, action := range []string{"on", "off", "status", "allow ID_OR_TAG", "revoke ID_OR_TAG"} {
		sharing.AddCommand(leaf(action, "Tailnet sharing "+action))
	}
	gateway.AddCommand(sharing)
	restart := leaf("restart-after-update", "Restart the gateway after installing an update")
	restart.Hidden = true
	gateway.AddCommand(restart)
	for _, action := range []string{"start", "stop", "status", "stop-agents", "serve", "passwd EMAIL", "whoami EMAIL", "join NODE", "reset-owner"} {
		command := leaf(action, "Gateway "+action)
		if action == "serve" {
			addStringFlag(command, "listen", "ADDRESS", "listen address (default 127.0.0.1:7420)")
		}
		gateway.AddCommand(command)
	}
	mainGateway := leaf("main", "Show the main gateway and archive readiness")
	mainGateway.AddCommand(leaf("set", "Make this machine the main gateway"))
	archiveReady := leaf("archive-ready", "Confirm this main gateway has its archive")
	archiveReady.Flags().Bool("empty", false, "create a fresh empty archive when none exists")
	mainGateway.AddCommand(archiveReady)
	gateway.AddCommand(mainGateway)
	users := &cobra.Command{Use: "user", Short: "Manage gateway accounts offline"}
	users.RunE = func(command *cobra.Command, args []string) error {
		if len(args) == 0 {
			return command.Help()
		}
		return errors.New("unknown gateway user command")
	}
	gateway.RunE = func(command *cobra.Command, args []string) error {
		if len(args) == 0 {
			return command.Help()
		}
		return errors.New("unknown gateway command")
	}
	for _, action := range []string{"add EMAIL", "list", "disable EMAIL"} {
		users.AddCommand(leaf(action, "Gateway user "+action))
	}
	gateway.AddCommand(users)
	deploy := leaf("deploy", "Deploy the public gateway to oracle-vm")
	addStringFlag(deploy, "binary", "PATH", "prebuilt Linux ARM64 agentpack binary")
	deploy.Flags().Bool("dry-run", false, "validate and show deployment without changing the VM")
	root.AddCommand(gateway, deploy, service)
}

func addMCPCommands(root *cobra.Command, leaf func(use, short string) *cobra.Command) {
	mcp := &cobra.Command{Use: "mcp", Short: "Manage MCP servers"}
	mcpAdd := leaf("add NAME [-- ARGS...]", "Add or replace an MCP server; arguments after -- are passed to the server")
	addStringFlag(mcpAdd, "command", "COMMAND", "server executable (local server)")
	addStringFlag(mcpAdd, "url", "URL", "remote server URL (instead of --command)")
	addStringFlag(mcpAdd, "type", "TYPE", "remote transport, such as http or sse (default http)")
	addStringArrayFlag(mcpAdd, "header", "KEY=VALUE", "header sent to the remote server")
	addStringArrayFlag(mcpAdd, "env", "KEY=VALUE", "environment passed to the server")
	mcpAdd.Flags().Bool("allow-unpinned", false, "record the server as unpinned when its registry is unreachable")
	mcpRemove := leaf("remove NAME", "Remove an MCP server")
	mcpLogin := leaf("login SERVER|URL", "Connect a remote MCP server once for every agent and node")
	addStringFlag(mcpLogin, "header", "NAME", "store a header value (read hidden) instead of signing in with OAuth")
	addStringArrayFlag(mcpLogin, "var", "NAME", "store environment variables (read hidden) for local servers that name --label")
	addStringFlag(mcpLogin, "label", "LABEL", "connection name, used by credential = \"LABEL\"")
	addStringFlag(mcpLogin, "client-id", "ID", "pre-registered OAuth client ID (default: register dynamically)")
	mcp.AddCommand(mcpAdd, mcpRemove, leaf("list", "List configured MCP servers"), mcpLogin,
		leaf("logout CONNECTION", "Remove an MCP connection from every node"),
		leaf("connections", "List MCP connections shared across the mesh"),
		leaf("approve CONNECTION MODULE", "Let a plugin's MCP server use a connection"))
	root.AddCommand(mcp)
}

func addEnvironmentCommands(root *cobra.Command, leaf func(use, short string) *cobra.Command) {
	env := &cobra.Command{Use: "env", Short: "Manage portable external environments"}
	envInit := leaf("init [NAME]", "Create an external definition directory")
	addStringFlag(envInit, "name", "NAME", "environment name under AGENTPACK_HOME/environments")
	addStringFlag(envInit, "dir", "PATH", "explicit definition directory")
	addStringFlag(envInit, "from", "PATH", "copy agentpack.toml and pack.lock from a project")
	envUse := leaf("use REF", "Bind the workspace to an external definition")
	addStringFlag(envUse, "project", "PATH", "workspace checkout to bind")
	envBind := leaf("bind REF", "Alias for env use")
	addStringFlag(envBind, "project", "PATH", "workspace checkout to bind")
	envExport := leaf("export PATH", "Export definition+lock as a portable bundle")
	addStringFlag(envExport, "from", "REF", "environment name or definition path")
	envImport := leaf("import PATH", "Import a portable bundle")
	addStringFlag(envImport, "name", "NAME", "destination environment name")
	addStringFlag(envImport, "dir", "PATH", "explicit destination directory")
	env.AddCommand(
		envInit,
		envUse,
		envBind,
		leaf("unuse", "Clear the workspace environment binding"),
		leaf("list", "List environments under AGENTPACK_HOME"),
		leaf("status", "Show binding and storage location"),
		envExport,
		envImport,
		leaf("restore", "Fetch locked cache inputs without changing pack.lock"),
	)
	root.AddCommand(env)
}

func addInternalCommands(root *cobra.Command, leaf func(use, short string) *cobra.Command) {
	hook := leaf("hook-exec KIND [-- NOTIFY...]", "Internal hook execution bridge")
	hook.Hidden = true
	for _, name := range []string{"target", "event", "specs-dir", "file", "session", "state", "spec"} {
		addStringFlag(hook, name, "VALUE", name)
	}
	root.AddCommand(hook)
	mcpExec := leaf("mcp-exec -- ARGS...", "Internal: start an MCP server from its locked npm tree")
	mcpExec.Hidden = true
	addStringFlag(mcpExec, "spec", "FILE", "locked npm tree spec")
	root.AddCommand(mcpExec)
	mcpBridge := leaf("mcp-bridge ID [-- COMMAND...]", "Internal: serve an MCP server with a stored credential")
	mcpBridge.Hidden = true
	addStringFlag(mcpBridge, "url", "URL", "remote server URL")
	addStringArrayFlag(mcpBridge, "header", "KEY=VALUE", "header sent to the remote server")
	mcpBridge.Flags().Bool("sse", false, "use the SSE transport")
	root.AddCommand(mcpBridge)
	root.AddCommand(&cobra.Command{
		Use:    "state-selfcheck",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE:   runStateSelfcheck,
	})
}

// leafInvocation names a parsed command by its top-level command, with the
// rest of its path ahead of the positional arguments.
func leafInvocation(command *cobra.Command, global Global, args []string) Invocation {
	path := strings.Fields(command.CommandPath())[1:]
	return Invocation{Global: global, Command: path[0], Args: append(path[1:], args...), Flags: command.Flags()}
}

func interactiveTerminal(in io.Reader, out io.Writer) bool {
	inFile, okIn := in.(*os.File)
	outFile, okOut := out.(*os.File)
	if !okIn || !okOut {
		return false
	}
	return term.IsTerminal(int(inFile.Fd())) && term.IsTerminal(int(outFile.Fd()))
}
