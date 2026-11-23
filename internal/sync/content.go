package sync

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/olegpustovit/agentpack/internal/artifacts"
	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/environment"
	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/hooks"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/staging"
	toml "github.com/pelletier/go-toml/v2"
)

// Content is the host-path-free section of Inputs: what the definition and
// mode select, never where they live, so it compares across checkouts and
// machines. It hashes parsed content, so launches never read it.
type Content struct {
	// Source is the manifest's semantics, contract.json and the lock's pins.
	Source string
	// Selected is the mode-filtered package files, .agents, hooks and MCP servers one target receives.
	Selected string
}

// generation is preflight's generation ID: the source, the mode, the
// workspace configuration and what the selected staging holds.
func (in Inputs) generation(effective mode.Effective, workspaceDigest, stagedConfigDigest string, staged []byte) string {
	return digestBytes(append([]byte(in.Content.Source+"\x00"+effective.FingerprintMaterial()+"\x00"+workspaceDigest+"\x00"+stagedConfigDigest+"\x00"), staged...))
}

// EnvironmentDigest is Content.Selected for a definition, mode and target: it
// compares selected runtime inputs across a package mutation. It excludes lock bookkeeping and generation paths.
// The MCP connections that reroute a selected server (their ID and URL, never a secret) influence
// the digest, so persist it only in private local state and never publish it.
func EnvironmentDigest(definitionRoot, workspace, selectedMode string, target harness.Target) (string, error) {
	digest, _, err := SelectedEnvironment(definitionRoot, workspace, selectedMode, target)
	return digest, err
}

// SelectedEnvironment is EnvironmentDigest with the sorted names of what it selects ("skill x",
// "command y", "MCP z", "MCP z connected"), which a reload notice compares.
func SelectedEnvironment(definitionRoot, workspace, selectedMode string, target harness.Target) (string, []string, error) {
	file, err := loadLockOrEmpty(definitionRoot)
	if err != nil {
		return "", nil, err
	}
	lock := file.lock
	for _, pkg := range lock.Packages {
		if err := cache.VerifyPackageIncremental(pkg); err != nil {
			return "", nil, err
		}
	}
	project, err := manifest.Load(definitionRoot)
	if err != nil {
		return "", nil, err
	}
	effective, err := resolveMode(workspace, project, &lock, selectedMode)
	if err != nil {
		return "", nil, err
	}
	return selectedContent(definitionRoot, workspace, project, lock, effective, target)
}

// LaunchFingerprint is the launch identity of a project's current inputs, as a launch compares it
// with the one its last sync stored. It costs what the launch fast path does, never a content hash.
func LaunchFingerprint(definitionRoot, workspace, selectedMode string, target harness.Target) (string, error) {
	file, err := loadLockOrEmpty(definitionRoot)
	if err != nil {
		return "", err
	}
	project, err := manifest.Load(definitionRoot)
	if err != nil {
		return "", err
	}
	effective, err := resolveMode(workspace, project, &file.lock, selectedMode)
	if err != nil {
		return "", err
	}
	return launchDigest(definitionRoot, workspace, effective, target, file)
}

// environmentItem names what a selected file gives the agent, as a reload notice lists it; "" for support files.
func environmentItem(module, name, virtual string) string {
	parts := strings.Split(artifacts.StripHarnessPrefix(virtual), "/")
	switch {
	case len(parts) == 1 && parts[0] == "SKILL.md":
		return "skill " + cmp.Or(name, path.Base(module))
	case len(parts) >= 3 && parts[0] == "skills" && parts[len(parts)-1] == "SKILL.md":
		return "skill " + parts[len(parts)-2]
	case len(parts) == 2 && slices.Contains([]string{"commands", "agents", "rules"}, parts[0]):
		return strings.TrimSuffix(parts[0], "s") + " " + strings.TrimSuffix(parts[1], path.Ext(parts[1]))
	}
	return ""
}

// selectedContent computes Content.Selected and the names of what it selects.
func selectedContent(definitionRoot, workspace string, project *manifest.Manifest, lock lockfile.PackLock, effective mode.Effective, target harness.Target) (string, []string, error) {
	items := map[string]bool{}
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	file := func(module, name, virtual, path string) error {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if item := environmentItem(module, name, virtual); item != "" {
			items[item] = true
		}
		return encoder.Encode(struct {
			Module, Name, Path string
			Executable         bool
			Digest             [32]byte
		}{module, name, virtual, info.Mode()&0111 != 0, sha256.Sum256(data)})
	}
	slices.SortFunc(lock.Packages, func(a, b lockfile.Package) int { return strings.Compare(a.Module, b.Module) })
	for _, pkg := range lock.Packages {
		if pkg.Kind == lockfile.PackagePlugin && (slices.Contains(lock.Config.DisabledPlugins, pkg.CacheKey) || len(pkg.CacheKey) >= 16 && slices.Contains(lock.Config.DisabledPlugins, pkg.CacheKey[:16])) {
			continue
		}
		root, err := cache.EntryDir(pkg.CacheKey)
		if err != nil {
			return "", nil, err
		}
		files, err := staging.PackageFiles(root, pkg.Module, pkg.Kind == lockfile.PackagePlugin)
		if err != nil {
			return "", nil, err
		}
		// These definitions are represented by their filtered effective values below.
		definitions := map[string]bool{"mcp.json": true, ".mcp.json": true}
		for _, plugin := range cache.ReadPluginManifests(root) {
			definitions[plugin.Relative] = true
			declared, _ := plugin.Declared("mcpServers")
			for _, path := range declared {
				resolved, err := cache.ResolveInside(root, path)
				if err != nil {
					continue
				} // CollectMCP reports invalid declarations using staging's policy.
				relative, err := filepath.Rel(root, resolved)
				if err != nil {
					return "", nil, err
				}
				definitions[filepath.ToSlash(relative)] = true
			}
		}
		for _, input := range files {
			allowed, err := effective.AllowsPackagePath(pkg.Module, input.Real)
			if err != nil {
				return "", nil, err
			}
			if !allowed || definitions[input.Real] {
				continue
			}
			if err := file(pkg.Module, pkg.Name, input.Virtual, input.Path); err != nil {
				return "", nil, err
			}
		}
	}
	err := walkDotAgents(paths.ProjectDotAgentsDir(workspace), func(relative, path string, _ os.DirEntry) error {
		relative = filepath.ToSlash(relative)
		if relative == "mcp.json" {
			return nil
		}
		allowed, err := effective.AllowsDotAgentsPath(relative)
		if err != nil || !allowed {
			return err
		}
		return file(".agents", "", relative, path)
	})
	if err != nil {
		return "", nil, err
	}
	bundle, err := hooks.Collect(workspace, lock, "", effective)
	if err != nil {
		return "", nil, err
	}
	bundle = bundle.ForTarget(target)
	for _, hook := range bundle.Hooks {
		hook.Origin.CacheKey, hook.Origin.SourceRoot, hook.Origin.SourceFile, hook.Origin.PackageKey = "", "", "", ""
		if err := encoder.Encode(hook); err != nil {
			return "", nil, err
		}
	}
	entries, err := staging.CollectMCP(workspace, lock, project, &effective)
	if err != nil {
		return "", nil, err
	}
	pinned := mcp.ApplyPins(entries, lock.MCPServers)
	indexPath, err := paths.MCPCredentialIndexPath()
	if err != nil {
		return "", nil, err
	}
	index, err := mcp.ReadCredentialIndex(indexPath)
	if err != nil {
		return "", nil, err
	}
	logins, err := mcp.ReadProjectLogins(definitionRoot)
	if err != nil {
		return "", nil, err
	}
	// Staging's own matcher: a connection or project login pick that reroutes a server through the bridge changes what the agent runs.
	bridged, _ := mcp.ApplyCredentials(pinned, index, logins, "agentpack")
	for _, name := range entries.Names() {
		server := pinned[name].Server
		if err := encoder.Encode(struct {
			Name   string
			Server mcp.Server
			OAuth  *mcp.OAuth
		}{name, server, server.OAuth}); err != nil {
			return "", nil, err
		}
		items["MCP "+name] = true
		bridge := bridged[name].Server
		if bridge.Disabled != server.Disabled {
			if err := encoder.Encode(struct{ Disabled string }{name}); err != nil {
				return "", nil, err
			}
		}
		if !slices.Equal(bridge.Args, server.Args) {
			items["MCP "+name+" connected"] = true
			if err := encoder.Encode(struct {
				Name   string
				Bridge []string
			}{name, bridge.Args}); err != nil {
				return "", nil, err
			}
		}
		for _, record := range lock.MCPServers {
			if record.Name != name || record.Definition != entries[name].Server.Definition() || !mcp.StartsWithNPX(entries[name].Server) {
				continue
			}
			if spec, ok := mcp.SpecFor(record); ok {
				if mcp.TreeDigest(spec.Tree) != spec.TreeDigest {
					return "", nil, fmt.Errorf("MCP server %q has an invalid locked tree digest", name)
				}
				if err := encoder.Encode(spec); err != nil {
					return "", nil, err
				}
			}
		}
	}
	names := slices.Sorted(maps.Keys(items))
	return hex.EncodeToString(hash.Sum(nil)), names, nil
}

// walkDotAgents visits the regular files under a project's .agents in walk
// order, with their native relative paths. Antigravity's launch puts the
// staged bundle there (a link, or a copy where links are refused); it is
// Agentpack's own output, not an input. A missing root has no files.
func walkDotAgents(root string, visit func(relative, path string, entry os.DirEntry) error) error {
	overlay := filepath.Join(root, "plugins", paths.StagedAgentpackBundleName)
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) && path == root {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if path == overlay {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return visit(relative, path, entry)
	})
}

// sourceIdentity computes Content.Source.
func sourceIdentity(definitionRoot string, lock lockfile.PackLock) (string, error) {
	manifestPath := paths.ManifestPath(definitionRoot)
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", err
	}
	project, err := manifest.Load(definitionRoot)
	if err != nil {
		return "", err
	}
	if project == nil {
		return "", fmt.Errorf("source identity requires %s", paths.ManifestName)
	}
	var rawManifest map[string]any
	if err := toml.Unmarshal(manifestBytes, &rawManifest); err != nil {
		return "", fmt.Errorf("parse manifest for source identity: %w", err)
	}

	semanticManifest := sourceManifest{
		Name: project.Name, Version: project.Version, Description: project.Description,
		Dependencies: make(map[string]sourceDependency, len(project.Dependencies)),
		MCPServers:   make(map[string]sourceMCPServer, len(project.MCP.Servers)),
		Modes:        project.Modes,
	}
	// manifest.Load defaults an omitted name from DefinitionRoot, which is a host path.
	if _, declared := rawManifest["name"]; !declared {
		semanticManifest.Name = ""
	}
	for module, dependency := range project.Dependencies {
		item := sourceDependency{}
		if dependency.Short != nil {
			item.Short = *dependency.Short
		}
		if dependency.Table != nil {
			item.Commit, item.Tag, item.Branch, item.Version = dependency.Table.Commit, dependency.Table.Tag, dependency.Table.Branch, dependency.Table.Version
			// A local path is deliberately omitted; its actual package bytes are in the lock.
			item.Local = dependency.Table.Path != nil
		}
		semanticManifest.Dependencies[module] = item
	}
	for name, server := range project.MCP.Servers {
		semanticManifest.MCPServers[name] = sourceMCPServerFrom(server)
	}
	contract := json.RawMessage(nil)
	contractPath := filepath.Join(definitionRoot, "contract.json")
	if _, err := os.Stat(contractPath); err == nil {
		validated, err := environment.LoadContract(contractPath)
		if err != nil {
			return "", fmt.Errorf("load contract for source identity: %w", err)
		}
		contract, err = json.Marshal(validated)
		if err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	packages := make([]sourcePackage, 0, len(lock.Packages))
	for _, pkg := range lock.Packages {
		packages = append(packages, sourcePackage{Module: pkg.Module, Kind: pkg.Kind, Direct: pkg.Direct, Commit: pkg.Commit, ContentHash: pkg.ContentHash})
	}
	slices.SortFunc(packages, func(a, b sourcePackage) int { return strings.Compare(a.Module, b.Module) })
	disabled := slices.Clone(lock.Config.DisabledPlugins)
	slices.Sort(disabled)
	servers := make([]sourceLockedMCP, 0, len(lock.MCPServers))
	for _, server := range lock.MCPServers {
		item := sourceLockedMCP{Name: server.Name, Source: server.Source, Launcher: server.Launcher, Status: server.Status, Package: server.Package, Version: server.Version, Integrity: server.Integrity, AllowUnpinned: server.AllowUnpinned, Bin: server.Bin, TreeDigest: server.TreeDigest}
		item.Tree = make([]sourceNPMTreeEntry, 0, len(server.Tree))
		for _, entry := range server.Tree {
			item.Tree = append(item.Tree, sourceNPMTreeEntry{Path: entry.Path, Version: entry.Version, Integrity: entry.Integrity, Optional: entry.Optional, OS: canonicalStrings(entry.OS), CPU: canonicalStrings(entry.CPU)})
		}
		slices.SortFunc(item.Tree, func(a, b sourceNPMTreeEntry) int { return strings.Compare(a.Path, b.Path) })
		servers = append(servers, item)
	}
	slices.SortFunc(servers, func(a, b sourceLockedMCP) int { return strings.Compare(a.Name, b.Name) })

	envelope := struct {
		Version    string            `json:"version"`
		Manifest   sourceManifest    `json:"manifest"`
		Contract   json.RawMessage   `json:"contract,omitempty"`
		Packages   []sourcePackage   `json:"packages"`
		Disabled   []string          `json:"disabled_plugins,omitempty"`
		MCPServers []sourceLockedMCP `json:"mcp_servers,omitempty"`
	}{Version: "agentpack-source-v1", Manifest: semanticManifest, Contract: contract, Packages: packages, Disabled: disabled, MCPServers: servers}
	data, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256-source-v1:" + hex.EncodeToString(digest[:]), nil
}

type sourceManifest struct {
	Name         string                      `json:"name"`
	Version      string                      `json:"version"`
	Description  string                      `json:"description"`
	Dependencies map[string]sourceDependency `json:"dependencies"`
	MCPServers   map[string]sourceMCPServer  `json:"mcp_servers"`
	Modes        map[string]mode.Definition  `json:"modes"`
}

type sourceDependency struct {
	Short   string  `json:"short,omitempty"`
	Commit  *string `json:"commit,omitempty"`
	Tag     *string `json:"tag,omitempty"`
	Branch  *string `json:"branch,omitempty"`
	Version *string `json:"version,omitempty"`
	Local   bool    `json:"local,omitempty"`
}

type sourceMCPServer struct {
	Type       *string      `json:"type,omitempty"`
	Command    *string      `json:"command,omitempty"`
	Args       []string     `json:"args,omitempty"`
	EnvNames   []string     `json:"env_names,omitempty"`
	URL        *string      `json:"url,omitempty"`
	Disabled   *bool        `json:"disabled,omitempty"`
	HeaderKeys []string     `json:"header_keys,omitempty"`
	Cwd        string       `json:"cwd,omitempty"`
	OAuth      *sourceOAuth `json:"oauth,omitempty"`
}

type sourceOAuth struct {
	ClientID     string   `json:"client_id,omitempty"`
	CallbackPort int      `json:"callback_port,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	MetadataURL  string   `json:"metadata_url,omitempty"`
}

type sourcePackage struct {
	Module      string               `json:"module"`
	Kind        lockfile.PackageKind `json:"kind"`
	Direct      bool                 `json:"direct,omitempty"`
	Commit      string               `json:"commit,omitempty"`
	ContentHash string               `json:"content_hash,omitempty"`
}

type sourceNPMTreeEntry struct {
	Path      string   `json:"path"`
	Version   string   `json:"version"`
	Integrity string   `json:"integrity"`
	Optional  bool     `json:"optional,omitempty"`
	OS        []string `json:"os,omitempty"`
	CPU       []string `json:"cpu,omitempty"`
}

type sourceLockedMCP struct {
	Name          string               `json:"name"`
	Source        string               `json:"source,omitempty"`
	Launcher      string               `json:"launcher,omitempty"`
	Status        string               `json:"status,omitempty"`
	Package       string               `json:"package,omitempty"`
	Version       string               `json:"version,omitempty"`
	Integrity     string               `json:"integrity,omitempty"`
	AllowUnpinned bool                 `json:"allow_unpinned,omitempty"`
	Bin           string               `json:"bin,omitempty"`
	TreeDigest    string               `json:"tree_digest,omitempty"`
	Tree          []sourceNPMTreeEntry `json:"tree,omitempty"`
}

func sourceMCPServerFrom(server mcp.Server) sourceMCPServer {
	item := sourceMCPServer{Type: server.Type, Args: RedactCredentialArgs(server.Args), Disabled: server.Disabled}
	if server.Command != nil {
		command := portablePathValue(*server.Command)
		item.Command = &command
	}
	for key := range server.Env {
		item.EnvNames = append(item.EnvNames, key)
	}
	for key := range server.Headers {
		item.HeaderKeys = append(item.HeaderKeys, key)
	}
	slices.Sort(item.EnvNames)
	slices.Sort(item.HeaderKeys)
	if server.URL != nil {
		clean := redactCredentialURL(*server.URL)
		item.URL = &clean
	}
	if server.Cwd != nil && !isHostAbsolutePath(*server.Cwd) {
		item.Cwd = filepath.ToSlash(filepath.Clean(*server.Cwd))
	}
	if server.OAuth != nil {
		item.OAuth = &sourceOAuth{ClientID: server.OAuth.ClientID, CallbackPort: server.OAuth.CallbackPort, Scopes: slices.Clone(server.OAuth.Scopes), MetadataURL: redactCredentialURL(server.OAuth.MetadataURL)}
	}
	return item
}
