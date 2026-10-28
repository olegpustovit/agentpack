package opencode

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/tailscale/hujson"
)

func MergeMCP(path string, entries mcp.Entries) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	output, warnings, _, err := MergeMCPData(data, entries)
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		log.Printf("warning: %s", warning)
	}
	if err := os.WriteFile(path, output, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func MergeMCPData(data []byte, entries mcp.Entries) ([]byte, []string, []string, error) {
	root := map[string]any{"$schema": "https://opencode.ai/config.json"}
	if len(data) != 0 {
		standard, err := hujson.Standardize(append(data, '\n'))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("parse MCP seed: %w", err)
		}
		if err := json.Unmarshal(standard, &root); err != nil {
			return nil, nil, nil, fmt.Errorf("parse MCP seed: %w", err)
		}
	}
	servers, ok := root["mcp"].(map[string]any)
	if root["mcp"] != nil && !ok {
		return nil, nil, nil, fmt.Errorf("MCP seed: mcp must be a JSON object")
	}
	if servers == nil {
		servers = make(map[string]any)
		root["mcp"] = servers
	}
	var warnings []string
	var shadowed []string
	for _, name := range entries.Names() {
		if _, exists := servers[name]; exists {
			shadowed = append(shadowed, name)
			continue
		}
		server := entries[name].Server
		value := map[string]any{"enabled": server.Disabled == nil || !*server.Disabled}
		var missing []string
		if server.IsRemote() {
			value["type"], value["url"] = "remote", *server.URL
			if len(server.Headers) != 0 {
				value["headers"] = server.Headers
			}
			// OpenCode spells the client oauth.clientId, clientSecret and scope.
			if oauth := server.OAuth; oauth != nil {
				client := make(map[string]any)
				if oauth.ClientID != "" {
					client["clientId"] = oauth.ClientID
				}
				if oauth.ClientSecret != "" {
					client["clientSecret"] = oauth.ClientSecret
				}
				if len(oauth.Scopes) != 0 {
					client["scope"] = strings.Join(oauth.Scopes, " ")
				}
				if len(client) != 0 {
					value["oauth"] = client
				}
				if oauth.CallbackPort != 0 {
					missing = append(missing, "an OAuth callback port")
				}
				if oauth.MetadataURL != "" {
					missing = append(missing, "an OAuth metadata URL")
				}
			}
		} else {
			if server.Cwd != nil {
				value["cwd"] = *server.Cwd
			}
			value["type"] = "local"
			command := append([]string(nil), server.Args...)
			if server.Command != nil {
				command = append([]string{*server.Command}, command...)
			}
			value["command"] = command
			if len(server.Env) != 0 {
				value["environment"] = server.Env
			}
		}
		if len(missing) != 0 {
			warnings = append(warnings, mcp.NotCarried("OpenCode", name, missing...))
		}
		servers[name] = value
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, warnings, nil, fmt.Errorf("encode MCP config: %w", err)
	}
	return data, warnings, shadowed, nil
}
