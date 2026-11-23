package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/staging"
)

// mcpCredentialAction reports `mcp login|logout|connections|approve`, which talk to the gateway and need no project.
func mcpCredentialAction(invocation Invocation) bool {
	switch first(invocation.Args) {
	case "login", "logout", "connections", "approve":
		return invocation.Command == "mcp"
	}
	return false
}

func (runner Runner) runMCPBridge(ctx context.Context, invocation Invocation) (int, error) {
	args, dash := invocation.Args, invocation.Flags.ArgsLenAtDash()
	if len(args) == 0 || dash == -1 && len(args) != 1 || dash != -1 && dash != 1 {
		return 2, errors.New("mcp-bridge requires a credential ID, then --url or -- COMMAND")
	}
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return 1, err
	}
	options := network.MCPBridgeOptions{Home: home, ID: args[0], Command: args[1:], URL: stringFlag(invocation, "url"), SSE: boolFlag(invocation, "sse"), Headers: map[string]string{}, Stdin: runner.Stdin, Stdout: runner.Stdout, Stderr: runner.Stderr}
	for _, header := range stringsFlag(invocation, "header") {
		if key, value, ok := strings.Cut(header, "="); ok {
			options.Headers[key] = value
		}
	}
	if (options.URL == "") == (len(options.Command) == 0) {
		return 2, errors.New("mcp-bridge requires --url or -- COMMAND")
	}
	code, err := network.RunMCPBridge(ctx, options)
	if err != nil {
		fmt.Fprintf(runner.Stderr, "agentpack mcp-bridge: %v\n", err)
	}
	return code, err
}

type mcpCredentialList struct {
	Credentials []network.MCPCredentialInfo `json:"credentials"`
	CallbackURL string                      `json:"callback_url"`
}

func gatewayCall(ctx context.Context, method, path string, input, output any) error {
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return err
	}
	var body []byte
	if input != nil {
		if body, err = json.Marshal(input); err != nil {
			return err
		}
	}
	data, err := network.NodeRequest(ctx, home, method, path, bytes.NewReader(body))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("the agentpack gateway is not running; run `ap` first")
		}
		return err
	}
	if output == nil {
		return nil
	}
	return json.Unmarshal(data, output)
}

func (runner Runner) runMCPCredentials(ctx context.Context, invocation Invocation) (int, error) {
	action, args := invocation.Args[0], invocation.Args[1:]
	switch action {
	case "connections":
		if err := noArgs(args); err != nil {
			return 2, err
		}
		var list mcpCredentialList
		if err := gatewayCall(ctx, http.MethodGet, "/api/mcp/credentials", nil, &list); err != nil {
			return 1, err
		}
		if len(list.Credentials) == 0 {
			fmt.Fprintln(runner.Stdout, "No MCP connections. Add one with `agentpack mcp login <server|url>`.")
			return 0, nil
		}
		writer := tabwriter.NewWriter(runner.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(writer, "LABEL\tKIND\tSTATUS\tAPPLIES TO\tAPPROVED PLUGINS")
		for _, credential := range list.Credentials {
			target := credential.Match
			if target == "" {
				target = "credential = \"" + credential.Label + "\" (" + strings.Join(credential.Names, ", ") + ")"
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", credential.Label, credential.Kind, credential.Status, target, strings.Join(credential.Plugins, ", "))
		}
		return 0, writer.Flush()
	case "logout":
		if len(args) != 1 {
			return 2, errors.New("mcp logout requires one connection: a label, ID or URL")
		}
		credential, err := findMCPCredential(ctx, args[0])
		if err != nil {
			return 1, err
		}
		if err := gatewayCall(ctx, http.MethodDelete, "/api/mcp/credentials/"+credential.ID, nil, nil); err != nil {
			return 1, err
		}
		fmt.Fprintf(runner.Stdout, "Removed MCP connection %q on every node.\n", credential.Label)
		return 0, nil
	case "approve":
		if len(args) != 2 {
			return 2, errors.New("mcp approve requires a connection (label, ID or URL) and a plugin module")
		}
		credential, err := findMCPCredential(ctx, args[0])
		if err != nil {
			return 1, err
		}
		if err := gatewayCall(ctx, http.MethodPost, "/api/mcp/credentials/"+credential.ID+"/approvals", map[string]string{"module": args[1]}, nil); err != nil {
			return 1, err
		}
		fmt.Fprintf(runner.Stdout, "Plugin %s may now use MCP connection %q.\n", args[1], credential.Label)
		return 0, nil
	}
	// login
	label, header, hasHeader := stringFlag(invocation, "label"), stringFlag(invocation, "header"), invocation.Flags.Changed("header")
	envNames, clientID := stringsFlag(invocation, "var"), stringFlag(invocation, "client-id")
	var err error
	if len(envNames) != 0 {
		if len(args) != 0 || label == "" {
			return 2, errors.New("mcp login --var NAME... requires --label and no server; servers use it with credential = \"<label>\"")
		}
		values := map[string]string{}
		for _, name := range envNames {
			if values[name], err = runner.readMCPSecret(name); err != nil {
				return 1, err
			}
		}
		if err := gatewayCall(ctx, http.MethodPost, "/api/mcp/credentials", map[string]any{"kind": mcp.CredentialEnv, "label": label, "values": values}, nil); err != nil {
			return 1, err
		}
		fmt.Fprintf(runner.Stdout, "Saved MCP connection %q for every node. Local servers use it with credential = %q.\n", label, label)
		return 0, nil
	}
	if len(args) != 1 {
		return 2, errors.New("mcp login requires one server name or URL")
	}
	server, err := runner.mcpLoginTarget(invocation, args[0])
	if err != nil {
		return 1, err
	}
	if hasHeader {
		value, err := runner.readMCPSecret(header)
		if err != nil {
			return 1, err
		}
		if err := gatewayCall(ctx, http.MethodPost, "/api/mcp/credentials", map[string]any{"kind": mcp.CredentialHeader, "url": *server.URL, "label": label, "values": map[string]string{header: value}}, nil); err != nil {
			return 1, err
		}
		fmt.Fprintf(runner.Stdout, "Saved the %s header for %s on every node.\n", header, *server.URL)
		return 0, nil
	}
	input := map[string]any{"url": *server.URL, "label": label, "client_id": clientID}
	if oauth := server.OAuth; oauth != nil {
		if clientID == "" {
			input["client_id"], input["client_secret"] = oauth.ClientID, oauth.ClientSecret
		}
		input["scopes"] = oauth.Scopes
	}
	var started struct {
		Login        string `json:"login"`
		AuthorizeURL string `json:"authorize_url"`
	}
	if err := gatewayCall(ctx, http.MethodPost, "/api/mcp/logins", input, &started); err != nil {
		return 1, err
	}
	fmt.Fprintf(runner.Stdout, "Sign in to %s:\n  %s\n", *server.URL, started.AuthorizeURL)
	if os.Getenv("AGENTPACK_SESSION") != "1" {
		openInBrowser(started.AuthorizeURL)
	}
	for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case <-time.After(time.Second):
		}
		var status struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := gatewayCall(ctx, http.MethodGet, "/api/mcp/logins/"+started.Login, nil, &status); err != nil {
			return 1, err
		}
		switch status.Status {
		case "connected":
			fmt.Fprintf(runner.Stdout, "Connected %s for every node. Running agents pick it up on their next launch.\n", *server.URL)
			return 0, nil
		case "failed":
			return 1, errors.New(status.Error)
		}
	}
	return 1, errors.New("the sign-in was not completed in time")
}

// mcpLoginTarget resolves a URL, or a server name configured for the current project.
func (runner Runner) mcpLoginTarget(invocation Invocation, target string) (mcp.Server, error) {
	if strings.Contains(target, "://") {
		if _, err := mcp.CanonicalURL(target); err != nil {
			return mcp.Server{}, err
		}
		return mcp.Server{URL: &target}, nil
	}
	context, err := resolveInvocationContext(invocation, true)
	if err != nil {
		return mcp.Server{}, err
	}
	project, err := manifest.Load(context.DefinitionRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return mcp.Server{}, err
	}
	lock, err := lockfile.LoadUnguarded(context.DefinitionRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return mcp.Server{}, err
	}
	entries, err := staging.CollectMCP(context.WorkspaceRoot, lock, project, nil)
	if err != nil {
		return mcp.Server{}, err
	}
	entry, ok := entries[target]
	if !ok || !entry.Server.IsRemote() {
		return mcp.Server{}, fmt.Errorf("no remote MCP server named %q in this project; pass its URL instead", target)
	}
	return entry.Server, nil
}

func (runner Runner) readMCPSecret(name string) (string, error) {
	file, ok := runner.Stdin.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", errors.New("secret entry requires an interactive terminal")
	}
	fmt.Fprintf(runner.Stderr, "%s: ", name)
	value, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(runner.Stderr)
	if err != nil {
		return "", err
	}
	if len(value) == 0 {
		return "", fmt.Errorf("%s is empty", name)
	}
	defer clear(value)
	return string(value), nil
}

func findMCPCredential(ctx context.Context, reference string) (network.MCPCredentialInfo, error) {
	var list mcpCredentialList
	if err := gatewayCall(ctx, http.MethodGet, "/api/mcp/credentials", nil, &list); err != nil {
		return network.MCPCredentialInfo{}, err
	}
	canonical, _ := mcp.CanonicalURL(reference)
	var found []network.MCPCredentialInfo
	for _, credential := range list.Credentials {
		if credential.ID == reference || strings.EqualFold(credential.Label, reference) || canonical != "" && credential.Match == canonical {
			found = append(found, credential)
		}
	}
	switch len(found) {
	case 0:
		return network.MCPCredentialInfo{}, fmt.Errorf("no MCP connection %q; see `agentpack mcp connections`", reference)
	case 1:
		return found[0], nil
	}
	return network.MCPCredentialInfo{}, fmt.Errorf("%q matches %d connections; name one by label or ID", reference, len(found))
}
