package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/olegpustovit/agentpack/internal/paths"
)

// Credential kinds stored by the gateway.
const (
	CredentialOAuth  = "oauth"
	CredentialHeader = "header"
	CredentialEnv    = "env"
)

// AgentsModule is the approval that gives an env credential's variables to the owning account's agents.
const AgentsModule = "agentpack:agents"

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

type ProjectLogins struct {
	URLs    map[string]string      // canonical URL to credential ID
	Plugins map[string]PluginLogin // server name to a project-only permission
}

// PluginLogin permits one declared server to use one connection in this project.
// An empty credential restores automatic selection, with no project permission.
type PluginLogin struct {
	Module     string `json:"module"`
	URL        string `json:"url"`
	Credential string `json:"credential"`
}

func (login PluginLogin) Matches(entry Entry) bool {
	return entry.Source == Plugin && login.Module != "" && login.Module == entry.Module &&
		login.URL != "" && login.URL == LoginURL(entry.Server)
}

// ForServer returns the preferred credential and whether it is specific to this plugin server.
func (logins ProjectLogins) ForServer(name string, entry Entry) (string, bool) {
	if login := logins.Plugins[name]; login.Matches(entry) {
		return login.Credential, true
	}
	return logins.URLs[LoginURL(entry.Server)], false
}

// ReadProjectLogins reads the selections and permissions used by staging.
func ReadProjectLogins(root string) (ProjectLogins, error) {
	urlPath, err := paths.ProjectMCPLoginsPath(root)
	if err != nil {
		return ProjectLogins{}, err
	}
	urls, err := readLoginFile[string](urlPath)
	if err != nil {
		return ProjectLogins{}, err
	}
	pluginPath, err := paths.ProjectMCPPluginLoginsPath(root)
	if err != nil {
		return ProjectLogins{}, err
	}
	plugins, err := readLoginFile[PluginLogin](pluginPath)
	return ProjectLogins{URLs: urls, Plugins: plugins}, err
}

func readLoginFile[T any](path string) (map[string]T, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var logins map[string]T
	if err := json.Unmarshal(data, &logins); err != nil {
		return nil, fmt.Errorf("MCP project logins %s: %w", path, err)
	}
	return logins, nil
}

// SaveProjectLogin records that the project uses credential id for the server at rawURL;
// an empty id drops the pick, and a file left with no picks is removed.
func SaveProjectLogin(path, rawURL, id string) error {
	canonical, err := CanonicalURL(rawURL)
	if err != nil {
		return err
	}
	logins, err := readLoginFile[string](path)
	if err != nil {
		return err
	}
	if id == "" {
		delete(logins, canonical)
	} else {
		if logins == nil {
			logins = make(map[string]string)
		}
		logins[canonical] = id
	}
	return writeLoginFile(path, logins)
}

func SaveProjectPluginLogins(path string, updates map[string]PluginLogin) error {
	logins, err := readLoginFile[PluginLogin](path)
	if err != nil {
		return err
	}
	if logins == nil {
		logins = make(map[string]PluginLogin, len(updates))
	}
	maps.Copy(logins, updates)
	return writeLoginFile(path, logins)
}

func writeLoginFile[T any](path string, logins map[string]T) error {
	if len(logins) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(logins, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return paths.WriteAtomic(path, append(data, '\n'), 0o600)
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
// one. A plugin needs module-wide approval or permission for this server in the
// project. A project's login choice takes precedence over the first login.
func ApplyCredentials(entries Entries, index CredentialIndex, logins ProjectLogins, executable string) (Entries, []string) {
	if len(entries) == 0 || len(index.Credentials) == 0 && len(logins.Plugins) == 0 && !namesCredential(entries) {
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
		var projectPermission string
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
			picked, scoped := logins.ForServer(name, entry)
			if scoped {
				projectPermission = picked
			} else {
				picked = logins.URLs[canonical]
			}
			for i, candidate := range index.Credentials {
				if candidate.Kind == CredentialEnv || candidate.Match != canonical ||
					projectPermission != "" && candidate.ID != projectPermission {
					continue
				}
				if ref == nil || candidate.ID == picked {
					ref = &index.Credentials[i]
				}
				if picked == "" || candidate.ID == picked {
					break
				}
			}
			if projectPermission != "" && ref == nil {
				disabled := true
				entry.Server.Disabled = &disabled
				result[name] = entry
				notes = append(notes, fmt.Sprintf("MCP server %s: its selected login is unavailable; choose a login in Project Settings", name))
				continue
			}
			if picked != "" && ref != nil && ref.ID != picked {
				notes = append(notes, fmt.Sprintf("MCP server %s: the login picked for this project is gone; using %q", name, ref.Label))
			}
		}
		if ref == nil {
			continue
		}
		if entry.Source == Plugin && (projectPermission == "" || ref.ID != projectPermission) && !slices.Contains(ref.Plugins, entry.Module) {
			notes = append(notes, fmt.Sprintf("MCP server %s from %s needs permission to use %q; allow it in Project Settings or with `agentpack mcp approve`", name, entry.Module, ref.Label))
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
