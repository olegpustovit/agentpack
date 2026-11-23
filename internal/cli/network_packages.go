package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/olegpustovit/agentpack/internal/gateway"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/registry"
	"github.com/olegpustovit/agentpack/internal/session"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

// runningEnvironment is a running session the gateway can restart after a package change.
type runningEnvironment struct {
	packSync.RunningEnvironment
	harness     string
	operationID string
}

// restartable: chats (ACP) and browser terminals; native-stdio presence has no worker to restart.
func restartable(session session.SessionInfo) bool {
	return (session.Transport == "acp" || session.Transport != "shell" && session.State.Interactive) &&
		session.State.Running && !session.State.Restarting
}

func snapshotRunningEnvironments(ctx context.Context, definitionRoot string) ([]runningEnvironment, error) {
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return nil, err
	}
	sessions, err := network.Sessions(ctx, home)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	root, err := os.Stat(definitionRoot)
	if err != nil {
		return nil, err
	}
	var snapshots []runningEnvironment
	for _, session := range sessions {
		if !restartable(session) {
			continue
		}
		definition := session.DefinitionRoot
		if definition == "" {
			definition = session.Directory
		}
		info, err := os.Stat(definition)
		if err != nil || !os.SameFile(root, info) {
			continue
		}
		target, ok := launcherTarget(session.Harness)
		if !ok {
			return nil, fmt.Errorf("unknown harness %q", session.Harness)
		}
		digest, err := network.SessionEnvironment(home, session.ID)
		if err == nil && digest == "" {
			digest, err = packSync.EnvironmentDigest(definitionRoot, session.Directory, session.Mode, target)
		}
		if err != nil {
			return nil, fmt.Errorf("check running session %s before package change: %w", session.ID, err)
		}
		snapshots = append(snapshots, runningEnvironment{
			RunningEnvironment: packSync.RunningEnvironment{SessionID: session.ID, DefinitionRoot: definitionRoot, Workspace: session.Directory, Mode: session.Mode, Target: target, Digest: digest},
			harness:            session.Harness, operationID: rand.Text(),
		})
	}
	return snapshots, nil
}

func (runner Runner) restartChangedEnvironments(ctx context.Context, snapshots []runningEnvironment) error {
	if len(snapshots) == 0 {
		return nil
	}
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return err
	}
	live, err := network.Sessions(ctx, home)
	if err != nil {
		return err
	}
	running := make(map[string]bool, len(live))
	for _, session := range live {
		running[session.ID] = restartable(session)
	}
	byID := map[string]runningEnvironment{}
	var before []packSync.RunningEnvironment
	for _, snapshot := range snapshots {
		if running[snapshot.SessionID] {
			byID[snapshot.SessionID] = snapshot
			before = append(before, snapshot.RunningEnvironment)
		}
	}
	changed, err := runner.Service.ChangedEnvironments(ctx, before)
	if err != nil {
		return err
	}
	var failures []error
	for _, environment := range changed {
		snapshot := byID[environment.SessionID]
		if err := network.RestartWorkerWhenIdle(ctx, home, environment.SessionID, snapshot.operationID); err != nil {
			failures = append(failures, fmt.Errorf("package change saved, but session %s could not restart: %w", environment.SessionID, err))
			continue
		}
		fmt.Fprintf(runner.Stderr, "agentpack: restarting %s in %s (mode %s) after its environment changed\n", snapshot.harness, environment.Workspace, environment.Mode)
	}
	return errors.Join(failures...)
}

func (runner Runner) installNetworkPackage(ctx context.Context, directory, spec string, restart bool) error {
	return runner.changeNetworkProject(ctx, directory, restart, func(ctx context.Context, root string) error {
		_, err := runner.Service.AddAll(ctx, root, spec)
		return err
	})
}

// installNetworkMCP is `agentpack mcp add` for a recent folder, from POST /api/mcp/servers.
func (runner Runner) installNetworkMCP(ctx context.Context, directory string, input registry.MCPServerInput, restart bool) error {
	server, err := mcpServerFrom(input)
	if err != nil {
		return err
	}
	return runner.changeNetworkProject(ctx, directory, restart, func(ctx context.Context, root string) error {
		return runner.addMCPServer(ctx, root, input.Name, server, false, true)
	})
}

// changeNetworkProject applies a gateway install under the definition lock, captures the
// project for the Library, and restarts the running agents the change affects.
func (runner Runner) changeNetworkProject(ctx context.Context, directory string, restart bool, change func(ctx context.Context, root string) error) error {
	packContext, err := resolveInvocationContext(Invocation{Global: Global{WorkspaceRoot: directory}}, true)
	if err != nil {
		return err
	}
	held, release, err := packSync.LockDefinition(ctx, packContext.DefinitionRoot)
	if err != nil {
		return err
	}
	defer release()
	ctx = held
	var before []runningEnvironment
	if restart {
		before, err = snapshotRunningEnvironments(ctx, packContext.DefinitionRoot)
		if err != nil {
			return err
		}
	}
	if err := change(ctx, packContext.DefinitionRoot); err != nil {
		return err
	}
	if err := captureNetworkEnvironment(packContext.DefinitionRoot, directory, ""); err != nil {
		return fmt.Errorf("installed, but library capture failed: %w", err)
	}
	return runner.restartChangedEnvironments(ctx, before)
}

// projectContents converts pack.lock into the node's project view. Commands, arguments,
// environment and headers never leave the node.
// shortcut: lists only servers recorded in pack.lock, extend when mcp list provenance moves out of cli
func projectContents(locked lockfile.PackLock, project *manifest.Manifest) gateway.ProjectContents {
	contents := gateway.ProjectContents{Packages: []gateway.ProjectPackage{}, MCP: []gateway.ProjectMCP{}, Modes: []gateway.ProjectMode{}}
	for _, pkg := range locked.Packages {
		contents.Packages = append(contents.Packages, gateway.ProjectPackage{
			Module: pkg.Module, Commit: pkg.Commit, ContentHash: pkg.ContentHash, CacheKey: pkg.CacheKey,
			Kind: string(pkg.Kind), Name: pkg.Name, URL: pkg.URL, Direct: pkg.Direct,
		})
	}
	for _, server := range locked.MCPServers {
		contents.MCP = append(contents.MCP, gateway.ProjectMCP{
			Name: server.Name, Source: server.Source, Launcher: server.Launcher, Status: server.Status,
			Package: server.Package, Version: server.Version, Integrity: server.Integrity, Host: server.Host, Reason: server.Reason,
		})
	}
	names := []string{mode.DefaultName}
	if project != nil {
		names = project.ListModeNames()
	}
	for _, name := range names {
		definition := mode.ImplicitDefault()
		if project != nil {
			definition, _ = project.ModeDefinition(name)
		}
		contents.Modes = append(contents.Modes, projectMode(name, definition, contents))
	}
	return contents
}

// projectMode reports what a mode turns off, so the new-session sheet can show it before launch.
// A package is off when its package-level selectors and base exclude it; any path selector marks it partial.
func projectMode(name string, definition mode.Definition, contents gateway.ProjectContents) gateway.ProjectMode {
	definition.ApplyDefaults()
	parse := func(raws []string) []mode.Selector {
		selectors := []mode.Selector{}
		for _, raw := range raws {
			if selector, err := mode.ParseSelector(raw); err == nil {
				selectors = append(selectors, selector)
			}
		}
		return selectors
	}
	enabled, disabled := parse(definition.Enable), parse(definition.Disable)
	view := gateway.ProjectMode{Name: name, OffPackages: []string{}, PartialPackages: []string{}, OffMCP: []string{}}
	effective, err := mode.NewEffective(name, definition, nil)
	if err != nil {
		return view // launching this mode fails anyway; the launch reports why
	}
	for _, pkg := range contents.Packages {
		whole := func(selectors []mode.Selector) bool {
			return slices.ContainsFunc(selectors, func(s mode.Selector) bool { return s.Kind == mode.SelectorPackage && s.Module == pkg.Module })
		}
		paths := slices.ContainsFunc(append(append([]mode.Selector{}, enabled...), disabled...), func(s mode.Selector) bool {
			return s.Kind == mode.SelectorPackagePath && s.Module == pkg.Module
		})
		off := whole(disabled) || (!whole(enabled) && definition.Base != mode.BaseAll)
		switch {
		case off && !paths:
			view.OffPackages = append(view.OffPackages, pkg.Module)
		case paths:
			view.PartialPackages = append(view.PartialPackages, pkg.Module)
		}
	}
	for _, server := range contents.MCP {
		if !effective.AllowsMCP(server.Name) {
			view.OffMCP = append(view.OffMCP, server.Name)
		}
	}
	return view
}
