package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
)

// Credential kinds stored by the gateway.
const (
	CredentialOAuth  = "oauth"
	CredentialHeader = "header"
	CredentialEnv    = "env"
)

// CredentialRef is one stored credential as staging sees it: never a secret value.
// The gateway writes them to the credential index for the account bound to this machine.
type CredentialRef struct {
	ID    string `json:"id"`
	Match string `json:"match,omitempty"` // canonical URL; empty for env credentials
	Label string `json:"label"`
	Kind  string `json:"kind"`
	// Plugins are the plugin modules approved to receive this credential.
	Plugins []string `json:"plugins,omitempty"`
}

type CredentialIndex struct {
	Credentials []CredentialRef `json:"credentials"`
}

// ReadCredentialIndex reads the gateway-written index; a missing file is an empty index.
func ReadCredentialIndex(path string) (CredentialIndex, error) {
	var index CredentialIndex
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return index, nil
	}
	if err != nil {
		return index, err
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return CredentialIndex{}, fmt.Errorf("MCP credential index %s: %w", path, err)
	}
	return index, nil
}

// CanonicalURL is the form credentials match on: lowercase scheme and host,
// no default port, no fragment and no trailing slash, so "…/mcp/" and "…/mcp" match.
func CanonicalURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("MCP server URL must be http(s) without credentials: %q", raw)
	}
	host := strings.ToLower(parsed.Hostname())
	if port := parsed.Port(); port != "" && !(scheme == "http" && port == "80" || scheme == "https" && port == "443") {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	canonical := scheme + "://" + host + path
	if parsed.RawQuery != "" {
		canonical += "?" + parsed.RawQuery
	}
	return canonical, nil
}

// LoginURL is the canonical URL a shared login for server would match, or "" when the server
// cannot use one: it is local, names its own credential or Authorization header, or its URL has
// a query, which may hold a secret and so never leaves the machine.
func LoginURL(server Server) string {
	if !server.IsRemote() || server.Credential != "" || hasHeader(server.Headers, "Authorization") {
		return ""
	}
	canonical, err := CanonicalURL(*server.URL)
	if err != nil || strings.Contains(canonical, "?") {
		return ""
	}
	return canonical
}

func hasHeader(headers map[string]string, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

// ApplyCredentials replaces each server a stored credential applies to with
// `agentpack mcp-bridge`, which adds the credential at run time, so no secret
// reaches staging. A remote server matches by canonical URL unless it already
// sends its own Authorization header; a server naming `credential` uses that
// one. A plugin's server matches only after its module was approved for the
// credential. The returned lines explain servers left unchanged.
func ApplyCredentials(entries Entries, index CredentialIndex, executable string) (Entries, []string) {
	if len(entries) == 0 || len(index.Credentials) == 0 && !namesCredential(entries) {
		return entries, nil
	}
	result := make(Entries, len(entries))
	var notes []string
	for _, name := range entries.Names() {
		entry := entries[name]
		result[name] = entry
		server := entry.Server
		if server.Disabled != nil && *server.Disabled {
			continue
		}
		var ref *CredentialRef
		if server.Credential != "" {
			for i, candidate := range index.Credentials {
				if candidate.ID == server.Credential || strings.EqualFold(candidate.Label, server.Credential) {
					ref = &index.Credentials[i]
					break
				}
			}
			if ref == nil {
				notes = append(notes, fmt.Sprintf("MCP server %s names credential %q, which is not connected; run `agentpack mcp login`", name, server.Credential))
				continue
			}
		} else if server.IsRemote() && !hasHeader(server.Headers, "Authorization") {
			canonical, err := CanonicalURL(*server.URL)
			if err != nil {
				continue
			}
			for i, candidate := range index.Credentials {
				if candidate.Kind != CredentialEnv && candidate.Match == canonical {
					ref = &index.Credentials[i]
					break
				}
			}
		}
		if ref == nil {
			continue
		}
		if entry.Source == Plugin && !slices.Contains(ref.Plugins, entry.Module) {
			notes = append(notes, fmt.Sprintf("MCP server %s from %s matches credential %q; approve it with `agentpack mcp approve %s %s`", name, entry.Module, ref.Label, ref.Label, entry.Module))
			continue
		}
		bridged := Server{Command: &executable, Disabled: server.Disabled}
		switch {
		case server.IsRemote() && ref.Kind != CredentialEnv:
			bridged.Args = []string{"mcp-bridge", ref.ID, "--url", *server.URL}
			if server.Type != nil && strings.EqualFold(*server.Type, "sse") {
				bridged.Args = append(bridged.Args, "--sse")
			}
			for _, key := range sortedKeys(server.Headers) {
				bridged.Args = append(bridged.Args, "--header", key+"="+server.Headers[key])
			}
		case server.Command != nil && ref.Kind == CredentialEnv:
			bridged.Args = append([]string{"mcp-bridge", ref.ID, "--", *server.Command}, server.Args...)
			bridged.Env, bridged.Cwd = server.Env, server.Cwd
		default:
			notes = append(notes, fmt.Sprintf("MCP server %s: credential %q (%s) does not apply to this kind of server", name, ref.Label, ref.Kind))
			continue
		}
		entry.Server = bridged
		result[name] = entry
	}
	return result, notes
}

func namesCredential(entries Entries) bool {
	for _, entry := range entries {
		if entry.Server.Credential != "" {
			return true
		}
	}
	return false
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
