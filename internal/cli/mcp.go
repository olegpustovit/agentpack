package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/staging"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

func (runner Runner) runMCP(ctx context.Context, root string, arguments []string, quiet bool) error {
	if len(arguments) == 0 {
		return fmt.Errorf("mcp requires add, remove, or list")
	}
	action, args := arguments[0], arguments[1:]
	switch action {
	case "add":
		if len(args) == 0 {
			return fmt.Errorf("mcp add requires a name")
		}
		name := args[0]
		args = args[1:]
		server, noSync, allowUnpinned, err := parseMCPAdd(args)
		if err != nil {
			return err
		}
		if err := manifest.AddMCPServer(root, name, server); err != nil {
			return err
		}
		if !quiet {
			fmt.Fprintf(runner.Stdout, "Added MCP server %q to agentpack.toml\n", name)
		}
		if err := runner.Service.RefreshMCPRecords(ctx, root, true, allowUnpinned); err != nil {
			return err
		}
		if !noSync {
			_, err = runner.Service.Sync(ctx, root, packSync.SyncOptions{})
		}
		return err
	case "remove":
		args, noSync := takeBool(args, "--no-sync")
		if len(args) != 1 {
			return fmt.Errorf("mcp remove requires one name")
		}
		removed, err := manifest.RemoveMCPServer(root, args[0])
		if err != nil {
			return err
		}
		if !removed {
			return fmt.Errorf("no MCP server named %q in agentpack.toml [mcp.servers]", args[0])
		}
		if err := runner.Service.RefreshMCPRecords(ctx, root, false, false); err != nil {
			return err
		}
		if !noSync {
			_, err = runner.Service.Sync(ctx, root, packSync.SyncOptions{})
		}
		return err
	case "list":
		if err := noArgs(args); err != nil {
			return err
		}
		project, err := manifest.Load(root)
		if err != nil {
			return err
		}
		lock, err := lockfile.Load(root)
		if err != nil {
			return err
		}
		entries, err := staging.CollectMCP(root, lock, project, nil)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Fprintln(runner.Stdout, "No MCP servers configured.")
			return nil
		}
		for _, name := range entries.Names() {
			entry := entries[name]
			shown := ""
			if entry.Server.URL != nil {
				shown = *entry.Server.URL
			} else if entry.Server.Command != nil {
				shown = strings.TrimSpace(*entry.Server.Command + " " + strings.Join(entry.Server.Args, " "))
			}
			disabled := ""
			if entry.Server.Disabled != nil && *entry.Server.Disabled {
				disabled = " (disabled)"
			}
			fmt.Fprintf(runner.Stdout, "  %s: %s [from %s]%s%s\n", name, shown, entry.Source, disabled, mcpPinLabel(lock, name, entry))
		}
		return nil
	default:
		return fmt.Errorf("unknown mcp action %q", action)
	}
}

// parseMCPAdd reads the flags of `mcp add NAME`: a local server (--command,
// --args, --env) or a remote one (--url, --type, --header), never both.
func parseMCPAdd(args []string) (server mcp.Server, noSync, allowUnpinned bool, err error) {
	command, args, hasCommand, err := takeFlag(args, "--command")
	if err != nil {
		return server, false, false, err
	}
	url, args, hasURL, err := takeFlag(args, "--url")
	if err != nil {
		return server, false, false, err
	}
	serverType, args, hasType, err := takeFlag(args, "--type")
	if err != nil {
		return server, false, false, err
	}
	args, noSync = takeBool(args, "--no-sync")
	args, allowUnpinned = takeBool(args, "--allow-unpinned")
	switch {
	case hasCommand == hasURL:
		return server, false, false, fmt.Errorf("mcp add requires exactly one of --command or --url")
	case hasURL && url == "":
		return server, false, false, fmt.Errorf("--url requires a non-empty value")
	case hasCommand && command == "":
		return server, false, false, fmt.Errorf("--command requires a non-empty value")
	case hasCommand && hasType:
		return server, false, false, fmt.Errorf("--type applies to --url servers only")
	}
	serverArgs, environment, headers, err := parseMCPLists(args)
	if err != nil {
		return server, false, false, err
	}
	if hasURL {
		if len(serverArgs) != 0 || len(environment) != 0 {
			return server, false, false, fmt.Errorf("--args and --env apply to --command servers only")
		}
		if !hasType {
			serverType = "http"
		}
		return mcp.Server{Type: &serverType, URL: &url, Headers: headers}, noSync, allowUnpinned, nil
	}
	if len(headers) != 0 {
		return server, false, false, fmt.Errorf("--header applies to --url servers only")
	}
	return mcp.Server{Command: &command, Args: serverArgs, Env: environment}, noSync, allowUnpinned, nil
}

func parseMCPLists(arguments []string) ([]string, map[string]string, map[string]string, error) {
	var commandArgs []string
	environment := make(map[string]string)
	headers := make(map[string]string)
	section := ""
	for index := 0; index < len(arguments); index++ {
		switch arguments[index] {
		case "--args":
			section = "args"
		case "--env":
			section = "env"
		case "--header":
			section = "header"
		default:
			if section == "args" {
				commandArgs = append(commandArgs, arguments[index])
				continue
			}
			if section == "env" || section == "header" {
				key, value, found := strings.Cut(arguments[index], "=")
				if !found || key == "" {
					return nil, nil, nil, fmt.Errorf("expected KEY=VALUE, got %q", arguments[index])
				}
				if section == "env" {
					environment[key] = value
				} else {
					headers[key] = value
				}
				continue
			}
			return nil, nil, nil, fmt.Errorf("unexpected argument %q", arguments[index])
		}
	}
	return commandArgs, environment, headers, nil
}

// mcpPinLabel describes what pack.lock records for a server as it is defined
// now; a record made for an older definition counts as not locked.
func mcpPinLabel(lock lockfile.PackLock, name string, entry mcp.Entry) string {
	record, found := lock.MCPServer(name)
	if !found || record.Definition != entry.Server.Definition() {
		return " (not locked)"
	}
	if record.Status == lockfile.MCPPinned && record.Version != "" {
		return fmt.Sprintf(" (%s %s@%s)", record.Status, record.Package, record.Version)
	}
	return " (" + record.Status + ")"
}
