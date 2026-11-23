package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/olegpustovit/agentpack/internal/gateway"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/registry"
	"github.com/olegpustovit/agentpack/internal/staging"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

// restartStaleSessions reloads the running sessions a change to definitionRoot affected.
func (runner Runner) restartStaleSessions(ctx context.Context, definitionRoot string) error {
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return err
	}
	sessions, err := network.Sessions(ctx, home)
	if err != nil {
		return err
	}
	reloads, err := network.RestartStaleSessions(ctx, home, runner.Service, definitionRoot, sessions)
	for _, reload := range reloads {
		when := "restarting"
		if reload.Deferred {
			when = "will reload when idle:"
		}
		fmt.Fprintf(runner.Stderr, "agentpack: %s %s in %s (mode %s) after its environment changed\n", when, reload.Harness, reload.Workspace, reload.Mode)
	}
	return err
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
	defer func() {
		if release != nil {
			_ = release()
		}
	}()
	if err := change(held, packContext.DefinitionRoot); err != nil {
		return err
	}
	captureErr := captureNetworkEnvironment(packContext.DefinitionRoot, directory, "")
	if captureErr != nil {
		captureErr = fmt.Errorf("project settings saved, but Library capture failed: %w", captureErr)
	}
	if !restart {
		return captureErr
	}
	// A restarting worker takes the definition lock to stage its new environment.
	if err := release(); err != nil {
		return errors.Join(captureErr, err)
	}
	release = nil
	return errors.Join(captureErr, runner.restartStaleSessions(ctx, packContext.DefinitionRoot))
}

// projectContents converts pack.lock into the node's project view. Commands, arguments,
// environment and headers never leave the node.
func projectContents(locked lockfile.PackLock, project *manifest.Manifest) network.ProjectContents {
	contents := network.ProjectContents{Packages: []network.ProjectPackage{}, MCP: []network.ProjectMCP{}, Modes: []network.ProjectMode{}}
	for _, pkg := range locked.Packages {
		contents.Packages = append(contents.Packages, network.ProjectPackage{
			Module: pkg.Module, Commit: pkg.Commit, ContentHash: pkg.ContentHash, CacheKey: pkg.CacheKey,
			Kind: string(pkg.Kind), Name: pkg.Name, URL: pkg.URL, Direct: pkg.Direct,
		})
	}
	for _, server := range locked.MCPServers {
		contents.MCP = append(contents.MCP, network.ProjectMCP{
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
func projectMode(name string, definition mode.Definition, contents network.ProjectContents) network.ProjectMode {
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
	view := network.ProjectMode{Name: name, OffPackages: []string{}, PartialPackages: []string{}, OffMCP: []string{}}
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

func (runner Runner) saveNetworkProject(ctx context.Context, input gateway.ProjectSettingsInput) error {
	return runner.changeNetworkProject(ctx, input.Directory, true, func(ctx context.Context, root string) (err error) {
		loginPath, err := paths.ProjectMCPLoginsPath(root)
		if err != nil {
			return err
		}
		pluginLoginPath, err := paths.ProjectMCPPluginLoginsPath(root)
		if err != nil {
			return err
		}
		rollback, err := packSync.KeepProjectFiles(root, loginPath, pluginLoginPath)
		if err != nil {
			return err
		}
		defer func() {
			if err != nil {
				err = errors.Join(err, rollback())
			}
		}()

		project, err := manifest.Load(root)
		if err != nil {
			return err
		}
		for _, module := range input.Remove {
			if project == nil {
				continue
			}
			if _, exists := project.Dependencies[module]; !exists {
				continue
			}
			if _, err := runner.Service.Remove(ctx, root, module); err != nil {
				return err
			}
		}
		for _, spec := range input.Add {
			if _, err := runner.Service.AddAll(ctx, root, spec); err != nil {
				return err
			}
		}
		for _, name := range input.RemoveMCP {
			if _, err := manifest.RemoveMCPServer(root, name); err != nil {
				return err
			}
		}
		for _, entry := range input.Servers {
			server, err := mcpServerFrom(entry)
			if err != nil {
				return err
			}
			if server.IsRemote() {
				if _, err := mcp.CanonicalURL(entry.URL); err != nil {
					return errors.New("MCP server URL must be http(s) without credentials")
				}
			}
			if project != nil {
				if current, ok := project.MCP.Servers[entry.Name]; ok {
					if current.IsRemote() != server.IsRemote() ||
						current.IsRemote() && *current.URL != entry.URL ||
						current.Command != nil && *current.Command != entry.Command {
						if len(current.Headers) > 0 || len(current.Env) > 0 || current.OAuth != nil || current.Credential != "" {
							return fmt.Errorf("changing the endpoint of a server with private configuration requires editing agentpack.toml")
						}
					}
					server.Headers, server.OAuth, server.Credential = current.Headers, current.OAuth, current.Credential
					server.Env, server.Cwd, server.Disabled = current.Env, current.Cwd, current.Disabled
				}
			}
			if err := manifest.AddMCPServer(root, entry.Name, server); err != nil {
				return err
			}
		}
		if len(input.Servers)+len(input.RemoveMCP) > 0 {
			if err := runner.Service.RefreshMCPRecords(ctx, root, len(input.Servers) > 0, false); err != nil {
				return err
			}
		}
		for url, id := range input.Logins {
			if err := mcp.SaveProjectLogin(loginPath, url, id); err != nil {
				return err
			}
		}
		if len(input.PluginLogins) > 0 {
			locked, err := runner.Service.LoadLock(root)
			if err != nil {
				return err
			}
			project, err := manifest.Load(root)
			if err != nil {
				return err
			}
			plan, err := staging.CollectMCPPlan(input.Directory, locked, project, nil)
			if err != nil {
				return err
			}
			for name, login := range input.PluginLogins {
				if !login.Matches(plan.Entries[name]) {
					return gateway.ErrProjectPluginServerChanged
				}
			}
			return mcp.SaveProjectPluginLogins(pluginLoginPath, input.PluginLogins)
		}
		return nil
	})
}
