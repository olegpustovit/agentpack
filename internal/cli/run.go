package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/environment"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/proxy"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

var Version = "0.14.4"

// workerMemoryLimit is a soft GC target for session workers; GOMEMLIMIT overrides it.
const workerMemoryLimit = 64 << 20

type Runner struct {
	Stdout, Stderr io.Writer
	Stdin          io.Reader
	Service        packSync.Service
	Launch         func(*os.Process) (func(), error)
}

func NewRunner() Runner {
	return Runner{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, Service: packSync.NewService()}
}

// runLauncher starts a harness. Its arguments belong to the harness, so the
// product-owned extractor reads only agentpack's own options out of them.
func (runner Runner) runLauncher(ctx context.Context, arguments []string) (int, error) {
	invocation, err := Parse(arguments)
	if err != nil {
		return 2, err
	}
	runner = runner.withOutput(invocation)
	if invocation.Global.Proxy && invocation.Command != "claude" {
		return 2, errors.New("--proxy is only supported with `agentpack claude`")
	}
	packContext, err := resolveInvocationContext(invocation, true)
	if err != nil {
		return 1, err
	}
	code, err := runner.launchManaged(ctx, packContext, invocation, arguments)
	return code, runner.reportIntegrity(err)
}

// withOutput wraps stderr for progress and installs the service's message sinks.
func (runner Runner) withOutput(invocation Invocation) Runner {
	if invocation.Command != "hook-exec" && invocation.Command != "mcp-exec" && invocation.Command != "mcp-bridge" {
		runner.Stderr = &progressWriter{Writer: runner.Stderr}
	}
	if runner.Service.Notify == nil {
		runner.Service.Notify = func(message string) { fmt.Fprintln(runner.Stderr, "warning: "+message) }
	}
	if runner.Service.Info == nil && !invocation.Global.Quiet {
		runner.Service.Info = func(message string) { fmt.Fprintln(runner.Stderr, message) }
	}
	return runner
}

// dispatch runs a non-launcher command whose flags Cobra already parsed.
func (runner Runner) dispatch(ctx context.Context, invocation Invocation) (int, error) {
	if invocation.Global.Version {
		fmt.Fprintln(runner.Stdout, "agentpack "+Version)
		return 0, nil
	}
	if invocation.Global.Proxy {
		return 2, errors.New("--proxy is only supported with `agentpack claude`")
	}
	runner = runner.withOutput(invocation)
	label := &stepLabel{operation: invocation.Command}
	if !invocation.Global.Quiet && !invocation.Global.NoProgress {
		switch invocation.Command {
		case "init", "lock", "add", "remove", "update", "sync", "list", "mcp", "env", "preflight", "probe", "config", "support", "extra", "eject":
			defer watchProgress(runner.Stderr, label.String, time.Second)()
			// On a terminal each step changes the spinner's text instead of adding a line.
			if _, terminal := terminalColumns(runner.Stderr); terminal {
				runner.Service.Step = label.set
				if runner.Service.Progress == nil {
					runner.Service.Progress = label.set
				}
			}
		case "mode":
			defer startProgress(runner.Stderr, "mode", time.Second)()
		}
	}
	if runner.Service.Progress == nil && !invocation.Global.Quiet && !invocation.Global.NoProgress {
		switch invocation.Command {
		case "lock", "add", "remove", "update":
			runner.Service.Progress = func(message string) { fmt.Fprintln(runner.Stderr, message) }
		}
	}
	switch invocation.Command {
	case "hook-exec":
		return runner.runHook(ctx, invocation)
	case "mcp-exec":
		return runner.runMCPExec(ctx, invocation)
	case "mcp-bridge":
		return runner.runMCPBridge(ctx, invocation)
	case "session-signal":
		return runner.runSessionSignal(invocation.Args)
	case "session-worker":
		// A worker's live heap is small; a soft limit makes GC return idle memory instead of growing to 2x.
		if os.Getenv("GOMEMLIMIT") == "" {
			debug.SetMemoryLimit(workerMemoryLimit)
		}
		return runner.runSessionWorker(ctx, invocation.Args)
	case "gateway":
		return runner.runGateway(ctx, invocation)
	case "service":
		return runner.runNetworkService(ctx, invocation.Args)
	case "deploy":
		return runner.runDeploy(ctx, invocation)
	case "init":
		if err := runner.runInit(ctx, invocation); err != nil {
			return 1, err
		}
		return 0, nil
	}
	if mcpCredentialAction(invocation) {
		return runner.runMCPCredentials(ctx, invocation)
	}
	if invocation.Command == "mcp" && first(invocation.Args) == "add" {
		// mcp add owns --env for KEY=VALUE; a bare value still names the environment.
		for _, value := range stringsFlag(invocation, "env") {
			if !strings.Contains(value, "=") {
				invocation.Global.Env = value
			}
		}
	}
	allowsMissing := map[string]bool{"add": true, "env": true, "config": true, "support": true}
	packContext, err := resolveInvocationContext(invocation, allowsMissing[invocation.Command])
	if err != nil {
		return 1, err
	}
	root := packContext.DefinitionRoot
	workspace := packContext.WorkspaceRoot
	if definitionMutation(invocation) {
		held, release, lockErr := packSync.LockDefinition(ctx, root)
		if lockErr != nil {
			return 1, lockErr
		}
		defer release()
		ctx = held
	}
	switch invocation.Command {
	case "lock":
		if err := noArgs(invocation.Args); err != nil {
			return 2, err
		}
		var locked lockfile.PackLock
		locked, err = runner.Service.LockWithOptions(ctx, root, packSync.LockOptions{Refresh: boolFlag(invocation, "update"), AllowUnpinnedMCP: boolFlag(invocation, "allow-unpinned-mcp")})
		if err == nil && !invocation.Global.Quiet {
			fmt.Fprintf(runner.Stdout, "Wrote %s (%d package(s)).\n", paths.LockPath(root), len(locked.Packages))
		}
	case "add", "remove":
		// Staging is lazy: each launcher stages its own harness when the lock
		// changed, and running sessions are restarted below.
		args := invocation.Args
		plugin, hasPlugin := stringFlag(invocation, "plugin"), invocation.Command == "add" && invocation.Flags.Changed("plugin")
		if len(args) != 1 {
			return 2, fmt.Errorf("%s requires exactly one package spec", invocation.Command)
		}
		if invocation.Command == "add" {
			if !invocation.Global.Quiet {
				if hasPlugin {
					fmt.Fprintf(runner.Stdout, "Adding: plugin %s of marketplace %s\n", plugin, args[0])
				} else {
					fmt.Fprintf(runner.Stdout, "Adding: %s\n", args[0])
				}
			}
			var added []lockfile.Package
			if hasPlugin {
				var pkg lockfile.Package
				pkg, err = runner.Service.AddPlugin(ctx, root, args[0], plugin)
				added = []lockfile.Package{pkg}
			} else {
				added, err = runner.Service.AddAll(ctx, root, args[0])
			}
			if err == nil && !invocation.Global.Quiet {
				if len(added) > 1 {
					fmt.Fprintf(runner.Stdout, "The marketplace lists %d plugins; added each one.\n", len(added))
				}
				for _, pkg := range added {
					fmt.Fprintf(runner.Stdout, "Recorded %s in agentpack.toml and refreshed pack.lock.\n", pkg.Module)
				}
			}
		} else {
			var key string
			key, err = runner.Service.Remove(ctx, root, args[0])
			if err == nil && !invocation.Global.Quiet {
				fmt.Fprintf(runner.Stdout, "Removed %s from %s and refreshed %s.\n", key, paths.ManifestPath(root), paths.LockPath(root))
			}
		}
	case "update":
		args := invocation.Args
		var updated lockfile.PackLock
		updated, err = runner.Service.Update(ctx, root, args)
		if err == nil && !invocation.Global.Quiet {
			if len(args) == 0 {
				fmt.Fprintf(runner.Stdout, "Updated floating dependencies and refreshed %s (%d package(s)).\n", paths.LockPath(root), len(updated.Packages))
			} else {
				fmt.Fprintf(runner.Stdout, "Updated %s and refreshed %s.\n", strings.Join(args, ", "), paths.LockPath(root))
			}
		}
	case "list":
		if err := noArgs(invocation.Args); err != nil {
			return 2, err
		}
		var listed lockfile.PackLock
		listed, err = runner.Service.LoadLock(root)
		if err == nil && !invocation.Global.Quiet {
			project, loadErr := manifest.Load(root)
			if loadErr != nil {
				return 1, loadErr
			}
			fmt.Fprintln(runner.Stdout, "MODULE\tKIND\tSCOPE\tSELECTOR\tCOMMIT")
			for _, pkg := range listed.Packages {
				scope := "transitive"
				selector := ""
				if pkg.Direct {
					scope = "direct"
					selector = dependencySelector(project, pkg.Module)
				}
				commit := pkg.Commit
				if len(commit) > 12 {
					commit = commit[:12]
				}
				fmt.Fprintf(runner.Stdout, "%s\t%s\t%s\t%s\t%s\n", pkg.Module, pkg.Kind, scope, selector, commit)
			}
		}
	case "sync":
		if err := noArgs(invocation.Args); err != nil {
			return 2, err
		}
		dry, verify := boolFlag(invocation, "dry-run"), boolFlag(invocation, "verify-only")
		var result packSync.SyncResult
		result, err = runner.Service.Sync(ctx, root, packSync.SyncOptions{
			DryRun: dry, VerifyOnly: verify, UpdateLock: boolFlag(invocation, "update-lock"), Repair: boolFlag(invocation, "repair"),
			Mode: invocation.Global.Mode, WorkspaceRoot: workspace, StrictExternal: invocation.Global.StrictExternal,
		})
		if err == nil && !invocation.Global.Quiet {
			for _, warning := range result.Warnings {
				fmt.Fprintln(runner.Stderr, "warning: "+warning)
			}
			if dry {
				fmt.Fprintf(runner.Stdout, "Dry-run: would sync %d skill(s), %d plugin(s); %d skill(s) shadowed by plugins (omitted from staging); no changes made.\n", result.Skills, result.Plugins, result.Shadowed)
			} else if verify {
				fmt.Fprintln(runner.Stdout, "Staging checks passed")
			} else {
				fmt.Fprintf(runner.Stdout, "Sync finished — %d skill(s), %d plugin(s), %d cache index entr(ies). One merged bundle: agentpack-bundle.\n", result.Skills, result.Plugins, result.IndexEntries)
			}
		}
	case "preflight":
		code, preflightErr := runner.runPreflight(root, workspace, invocation)
		return code, runner.reportIntegrity(preflightErr)
	case "probe":
		code, probeErr := runner.runProbe(ctx, root, workspace, invocation)
		return code, runner.reportIntegrity(probeErr)
	case "config":
		if len(invocation.Args) == 0 {
			return 2, fmt.Errorf("config requires an action (compare)")
		}
		switch invocation.Args[0] {
		case "compare":
			code, compareErr := runner.runConfigCompare(workspace, invocation.Args[1:], boolFlag(invocation, "json"), invocation.Global.Quiet)
			return code, runner.reportIntegrity(compareErr)
		default:
			return 2, fmt.Errorf("unknown config action %q", invocation.Args[0])
		}
	case "support":
		code, supportErr := runner.runSupport(workspace, invocation)
		return code, runner.reportIntegrity(supportErr)
	case "env":
		err = runner.runEnv(ctx, root, workspace, invocation)
	case "mcp":
		err = runner.runMCP(ctx, root, invocation)
	case "mode":
		err = runner.runMode(root, invocation.Args, invocation.Global.Quiet)
	case "extra":
		err = runner.runExtra(workspace, invocation)
	case "eject":
		err = runner.runEject(ctx, root, workspace, invocation)
	default:
		return 2, fmt.Errorf("unknown command %q", invocation.Command)
	}
	if err != nil {
		return 1, runner.reportIntegrity(err)
	}
	if invocation.Command == "lock" || invocation.Command == "add" || invocation.Command == "remove" || invocation.Command == "update" || invocation.Command == "sync" && definitionMutation(invocation) {
		if err := captureNetworkEnvironment(root, workspace, invocation.Global.Mode); err != nil {
			return 1, fmt.Errorf("environment saved, but library capture failed: %w", err)
		}
	}
	if definitionMutation(invocation) {
		if err = runner.restartStaleSessions(ctx, root); err != nil {
			return 1, runner.reportIntegrity(err)
		}
	}
	return 0, nil
}

// reportIntegrity prints an integrity failure as plain lines, because the
// error renderer reflows text and would break digests and paths.
func (runner Runner) reportIntegrity(err error) error {
	var failure interface {
		Details() string
		Summary() string
	}
	if !errors.As(err, &failure) {
		return err
	}
	fmt.Fprint(runner.Stderr, failure.Details())
	return errors.New(failure.Summary())
}

func dependencySelector(project *manifest.Manifest, module string) string {
	if project == nil {
		return ""
	}
	dependency, found := project.Dependencies[module]
	if !found {
		return ""
	}
	if dependency.Short != nil {
		if *dependency.Short == "" {
			return "HEAD"
		}
		return *dependency.Short
	}
	if dependency.Table == nil {
		return ""
	}
	switch {
	case dependency.Table.Path != nil:
		return "path:" + *dependency.Table.Path
	case dependency.Table.Commit != nil:
		return "commit:" + *dependency.Table.Commit
	case dependency.Table.Branch != nil:
		return "branch:" + *dependency.Table.Branch
	case dependency.Table.Tag != nil:
		return "tag:" + *dependency.Table.Tag
	case dependency.Table.Version != nil:
		return "version:" + *dependency.Table.Version
	default:
		return "HEAD"
	}
}

func (runner Runner) runInit(ctx context.Context, invocation Invocation) error {
	root, err := paths.ResolveProjectRootOrCWD(invocation.Global.ProjectRoot)
	if invocation.Global.ProjectRoot != "" {
		root, err = filepath.Abs(invocation.Global.ProjectRoot)
	}
	if err != nil {
		return err
	}
	name, version := stringFlag(invocation, "name"), stringFlag(invocation, "version")
	if err := noArgs(invocation.Args); err != nil {
		return err
	}
	if name == "" {
		name = filepath.Base(root)
	}
	if version == "" {
		version = "0.0.1"
	}
	held, release, err := packSync.LockDefinition(ctx, root)
	if err != nil {
		return err
	}
	defer release()
	ctx = held
	if !invocation.Global.Quiet {
		fmt.Fprintln(runner.Stdout, "Initializing agentpack…")
	}
	if err := manifest.WriteStub(root, name, version); err != nil {
		return err
	}
	if err := lockfile.Init(root, name, version); err != nil {
		return err
	}
	if !invocation.Global.Quiet {
		fmt.Fprintf(runner.Stdout, "Created %s and %s\n", paths.ManifestPath(root), paths.LockPath(root))
	}
	return nil
}

func resolveInvocationContext(invocation Invocation, allowMissing bool) (paths.Context, error) {
	definitionRoot := invocation.Global.DefinitionRoot
	if invocation.Global.Env != "" {
		if definitionRoot != "" {
			return paths.Context{}, fmt.Errorf("use either --env or --definition-root, not both")
		}
		resolved, err := environment.ResolveRef(invocation.Global.Env)
		if err != nil {
			return paths.Context{}, err
		}
		definitionRoot = resolved
	}
	workspaceHint := firstNonEmpty(invocation.Global.WorkspaceRoot, invocation.Global.ProjectRoot)
	if workspaceHint == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return paths.Context{}, err
		}
		workspaceHint = cwd
	}
	bound := ""
	// Explicit selector wins; never fall through from a bad --env/--definition-root to a binding.
	if definitionRoot == "" {
		if abs, err := filepath.Abs(workspaceHint); err == nil {
			if binding, found, bindErr := environment.LoadBinding(abs); bindErr != nil {
				return paths.Context{}, bindErr
			} else if found {
				bound = binding.DefinitionRoot
			}
		}
	}
	if allowMissing {
		return paths.ResolveContextOrCWD(invocation.Global.ProjectRoot, definitionRoot, invocation.Global.WorkspaceRoot, bound)
	}
	return paths.ResolveContext(invocation.Global.ProjectRoot, definitionRoot, invocation.Global.WorkspaceRoot, bound)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (runner Runner) launch(ctx context.Context, packContext paths.Context, invocation Invocation) (code int, err error) {
	label := &stepLabel{operation: "preparing " + invocation.Command}
	setStep := label.set
	setStep("waiting for another agentpack command on this project")
	stopProgress := func() {}
	if !invocation.Global.Quiet && !invocation.Global.NoProgress {
		stopProgress = watchProgress(runner.Stderr, label.String, time.Second)
	}
	defer stopProgress()
	held, release, err := packSync.LockDefinition(ctx, packContext.DefinitionRoot)
	if err != nil {
		return 1, err
	}
	defer release()
	ctx = held
	target, ok := launcherTarget(invocation.Command)
	if !ok {
		return 1, fmt.Errorf("unknown harness %q", invocation.Command)
	}
	if invocation.Global.StrictExternal && target.UsesWorkspaceOverlay() {
		return 1, fmt.Errorf("%s requires a workspace overlay write; refuse under --strict-external (use claude/opencode, or omit --strict-external)", target)
	}
	effective, skipped, err := runner.Service.SyncForLaunchOptions(ctx, packContext.DefinitionRoot, packSync.SyncOptions{
		Mode: invocation.Global.Mode, Target: &target,
		WorkspaceRoot: packContext.WorkspaceRoot, StrictExternal: invocation.Global.StrictExternal,
		Progress: setStep,
	})
	if err != nil {
		return 1, err
	}
	setStep("starting " + invocation.Command)
	if invocation.Global.Debug {
		fmt.Fprintf(runner.Stderr, "agentpack: target=%s mode=%s definition=%s workspace=%s fast-sync=%t\n", target, effective.Name(), packContext.DefinitionRoot, packContext.WorkspaceRoot, skipped)
	}
	if id := os.Getenv("AGENTPACK_SESSION_ID"); id != "" && os.Getenv("AGENTPACK_SESSION_WORKER") == "1" {
		home, err := paths.UserAgentpackHome()
		if err == nil {
			err = network.RecordLaunchEnvironment(home, id, packContext.DefinitionRoot, packContext.WorkspaceRoot, effective.Name(), target)
		}
		if err != nil {
			return 1, err
		}
	}
	if invocation.Global.StrictExternal {
		options := packSync.PreflightOptions{Mode: effective.Name(), Target: &target, Policy: environment.PolicyLocal, WorkspaceRoot: packContext.WorkspaceRoot, StrictExternal: true}
		contract, contractErr := environment.LoadContract(filepath.Join(packContext.DefinitionRoot, "contract.json"))
		if contractErr != nil && !os.IsNotExist(contractErr) {
			return 2, contractErr
		}
		for _, requirement := range contract.Requirements {
			if (requirement.MinimumEvidence == "observed" || requirement.Predicate == "native_property" || requirement.Predicate == "coverage_complete") && (requirement.Target == "" || requirement.Target == "*" || requirement.Target == string(target)) {
				root, err := environment.ReceiptsRoot(packContext.WorkspaceRoot)
				if err != nil {
					return 5, err
				}
				if _, err := os.Stat(root); err == nil {
					options.ReceiptID = "latest"
				} else if !os.IsNotExist(err) {
					return 5, err
				}
				break
			}
		}
		report, err := runner.Service.Preflight(packContext.DefinitionRoot, options)
		if err != nil {
			return 5, err
		}
		if !report.OK {
			printPreflight(runner, report)
			if report.OverallStatus == "unknown" {
				return 4, fmt.Errorf("strict launch requires fresh observation; run agentpack probe --agent %s --strict-external", target)
			}
			if report.OverallStatus == "error" {
				return 5, fmt.Errorf("strict launch preflight operational failure; resolve findings before launch")
			}
			return 3, fmt.Errorf("strict launch preflight failed; resolve findings before launch")
		}
	}
	harness, err := registry.ByTarget(target)
	if err != nil {
		return 1, err
	}
	args := invocation.Args
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	launch := base.LaunchContext{
		ProjectRoot:    packContext.DefinitionRoot,
		WorkspaceRoot:  packContext.WorkspaceRoot,
		Arguments:      args,
		Mode:           effective,
		Yolo:           invocation.Global.Yolo,
		StrictExternal: invocation.Global.StrictExternal,
	}
	if os.Getenv("AGENTPACK_SESSION_WORKER") == "1" {
		launch.SessionID = os.Getenv("AGENTPACK_SESSION_ID")
	}
	command, finish, err := harness.Launch(launch)
	if err != nil {
		return 1, err
	}
	// The harness's after-launch work (credentials, generation leases) runs on every path out.
	defer func() { err = errors.Join(err, finish()) }()
	command.Dir = packContext.WorkspaceRoot
	// The agent starts in the workspace; PWD must say so too, because some
	// agents (OpenCode) take the project directory from PWD, which would
	// otherwise still name the directory agentpack was started from.
	command.Env = withPWD(command.Env, packContext.WorkspaceRoot)
	for i := 0; i < len(command.Env); {
		key, _, _ := strings.Cut(command.Env[i], "=")
		if strings.EqualFold(key, "AGENTPACK_SESSION_WORKER") || strings.EqualFold(key, "AGENTPACK_SESSION_ID") {
			command.Env = append(command.Env[:i], command.Env[i+1:]...)
		} else {
			i++
		}
	}
	// Workers report restart readiness after spawning; release before the agent runs.
	if err := release(); err != nil {
		return 1, err
	}
	command.Stdin, command.Stdout, command.Stderr = runner.Stdin, runner.Stdout, runner.Stderr
	stopProgress()
	// Give the child the original file descriptor so terminal detection still works.
	if writer, ok := runner.Stderr.(*progressWriter); ok {
		command.Stderr = writer.Writer
	}
	if invocation.Global.Proxy {
		running, err := proxy.Start(packContext.DefinitionRoot)
		if err != nil {
			return 1, err
		}
		if invocation.Global.Debug {
			fmt.Fprintf(runner.Stderr, "agentpack: Claude proxy listening on %s\n", running.Server.BaseURL())
		}
		running.Apply(command)
		code, runErr := runner.runProcess(command)
		running.Shutdown()
		return code, runErr
	}
	return runner.runProcess(command)
}

// withPWD returns env (the inherited environment when env is nil) with PWD
// set to directory.
func withPWD(env []string, directory string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "PWD=") {
			out = append(out, entry)
		}
	}
	return append(out, "PWD="+directory)
}

func noArgs(arguments []string) error {
	if len(arguments) != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(arguments, " "))
	}
	return nil
}

func hasBeforeDoubleDash(arguments []string, values ...string) bool {
	for _, argument := range arguments {
		if argument == "--" {
			return false
		}
		for _, value := range values {
			if argument == value {
				return true
			}
		}
	}
	return false
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

// launcherTarget maps a launcher command to its harness. The Cursor launcher is `agent`, so the
// target name `cursor` is not a command.
func launcherTarget(command string) (base.Target, bool) {
	if command == "cursor" {
		return "", false
	}
	if command == "agent" {
		command = "cursor"
	}
	target, err := base.ParseTarget(command)
	return target, err == nil
}
