package cli

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"github.com/olegpustovit/agentpack/internal/gateway"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/staging"
)

func (runner Runner) runMCP(ctx context.Context, root string, invocation Invocation) error {
	arguments, quiet := invocation.Args, invocation.Global.Quiet
	if len(arguments) == 0 {
		return fmt.Errorf("mcp requires add, remove, or list")
	}
	action, args := arguments[0], arguments[1:]
	switch action {
	case "add":
		if len(args) == 0 {
			return fmt.Errorf("mcp add requires a name")
		}
		server, err := parseMCPAdd(invocation)
		if err != nil {
			return err
		}
		return runner.addMCPServer(ctx, root, args[0], server, boolFlag(invocation, "allow-unpinned"), quiet)
	case "remove":
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
		return runner.Service.RefreshMCPRecords(ctx, root, false, false)
	case "list":
		if err := noArgs(args); err != nil {
			return err
		}
		project, err := manifest.Load(root)
		if err != nil {
			return err
		}
		lock, err := runner.Service.LoadLock(root)
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

// addMCPServer writes the server to agentpack.toml and records it in pack.lock;
// `mcp add` and the gateway's MCP install both use it.
func (runner Runner) addMCPServer(ctx context.Context, root, name string, server mcp.Server, allowUnpinned, quiet bool) error {
	if err := manifest.AddMCPServer(root, name, server); err != nil {
		return err
	}
	if !quiet {
		fmt.Fprintf(runner.Stdout, "Added MCP server %q to agentpack.toml\n", name)
	}
	return runner.Service.RefreshMCPRecords(ctx, root, true, allowUnpinned)
}

// mcpServerFrom applies parseMCPAdd's rules to the gateway's structured input.
func mcpServerFrom(input gateway.MCPServerInput) (mcp.Server, error) {
	switch {
	case (input.Command == "") == (input.URL == ""):
		return mcp.Server{}, fmt.Errorf("an MCP server needs exactly one of a command or a URL")
	case input.URL != "":
		if len(input.Args) != 0 {
			return mcp.Server{}, fmt.Errorf("arguments apply to command servers only")
		}
		serverType := cmp.Or(input.Type, "http")
		return mcp.Server{Type: &serverType, URL: &input.URL}, nil
	case input.Type != "":
		return mcp.Server{}, fmt.Errorf("a type applies to URL servers only")
	}
	return mcp.Server{Command: &input.Command, Args: input.Args}, nil
}

// parseMCPAdd reads `mcp add NAME`: a local server (--command, --env, and
// its arguments after --) or a remote one (--url, --type, --header), never both.
func parseMCPAdd(invocation Invocation) (mcp.Server, error) {
	flags := invocation.Flags
	command, url, serverType := stringFlag(invocation, "command"), stringFlag(invocation, "url"), stringFlag(invocation, "type")
	hasCommand, hasURL := flags.Changed("command"), flags.Changed("url")
	// Args is ["add", NAME, ...]; anything past the name must follow --.
	if dash := flags.ArgsLenAtDash(); dash == -1 && len(invocation.Args) > 2 || dash > 1 {
		return mcp.Server{}, fmt.Errorf("unexpected argument %q; pass server arguments after --", invocation.Args[2])
	}
	serverArgs := invocation.Args[2:]
	switch {
	case hasCommand == hasURL:
		return mcp.Server{}, fmt.Errorf("mcp add requires exactly one of --command or --url")
	case hasURL && url == "":
		return mcp.Server{}, fmt.Errorf("--url requires a non-empty value")
	case hasCommand && command == "":
		return mcp.Server{}, fmt.Errorf("--command requires a non-empty value")
	case hasCommand && flags.Changed("type"):
		return mcp.Server{}, fmt.Errorf("--type applies to --url servers only")
	}
	environment, err := keyValues(stringsFlag(invocation, "env"), true)
	if err != nil {
		return mcp.Server{}, err
	}
	headers, err := keyValues(stringsFlag(invocation, "header"), false)
	if err != nil {
		return mcp.Server{}, err
	}
	if hasURL {
		if len(serverArgs) != 0 || len(environment) != 0 {
			return mcp.Server{}, fmt.Errorf("server arguments and --env apply to --command servers only")
		}
		serverType = cmp.Or(serverType, "http")
		return mcp.Server{Type: &serverType, URL: &url, Headers: headers}, nil
	}
	if len(headers) != 0 {
		return mcp.Server{}, fmt.Errorf("--header applies to --url servers only")
	}
	return mcp.Server{Command: &command, Args: serverArgs, Env: environment}, nil
}

// keyValues reads KEY=VALUE flag values. A bare --env value names the
// agentpack environment instead, so skipBare leaves it out.
func keyValues(values []string, skipBare bool) (map[string]string, error) {
	pairs := map[string]string{}
	for _, value := range values {
		key, val, found := strings.Cut(value, "=")
		if !found && skipBare {
			continue
		}
		if !found || key == "" {
			return nil, fmt.Errorf("expected KEY=VALUE, got %q", value)
		}
		pairs[key] = val
	}
	return pairs, nil
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
