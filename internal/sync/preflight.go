package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/olegpustovit/agentpack/internal/artifacts"
	"github.com/olegpustovit/agentpack/internal/harness/claude"
	"github.com/olegpustovit/agentpack/internal/harness/codex"
	"github.com/olegpustovit/agentpack/internal/harness/grok"
	"github.com/olegpustovit/agentpack/internal/harness/omp"
	"github.com/olegpustovit/agentpack/internal/harness/opencode"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/environment"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/hooks"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
	library "github.com/olegpustovit/agentpack/internal/registry"
	"github.com/olegpustovit/agentpack/internal/staging"
	"github.com/pelletier/go-toml/v2"
)

// PreflightOptions selects the pure offline check surface.
type PreflightOptions struct {
	Mode           string
	Target         *base.Target
	Policy         environment.Policy
	StrictExternal bool
	WorkspaceRoot  string
	ContractPath   string // optional; empty loads <definition>/contract.json when present
	ReceiptID      string // optional; compare a prior native probe receipt (read-only)
}

// PreflightInputError marks malformed or inconsistent definition inputs.
// Filesystem failures remain their original errors so callers can keep them
// classified as operational failures.
type PreflightInputError struct{ Err error }

func (err *PreflightInputError) Error() string { return err.Err.Error() }
func (err *PreflightInputError) Unwrap() error { return err.Err }

func inputError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return err
	}
	return &PreflightInputError{Err: err}
}

// Preflight inspects an already-available definition without repairing cache,
// downloading, amending the lock, rebuilding staging, or writing overlays.
func (service Service) Preflight(definitionRoot string, options PreflightOptions) (environment.Report, error) {
	if options.Policy == "" {
		options.Policy = environment.PolicyLocal
	}
	workspace := options.WorkspaceRoot
	if workspace == "" {
		workspace = definitionRoot
	}
	report := environment.Report{
		SchemaVersion:  1,
		Phase:          "preflight",
		DefinitionRoot: definitionRoot,
		WorkspaceRoot:  workspace,
		Policy:         options.Policy,
		StrictExternal: options.StrictExternal,
		Capabilities:   map[string]string{},
		Coverage: []environment.CoverageNote{{
			Category: "skill", Scope: "native_catalog", Completeness: "none",
			Reason: "pure preflight does not execute native CLI discovery",
		}},
	}
	if options.Target != nil {
		report.Target = string(*options.Target)
	}
	for _, target := range base.AllTargets() {
		status, note := environment.ExternalCapability(target)
		report.Capabilities[string(target)] = status + ": " + note
	}

	home, err := paths.UserAgentpackHome()
	if err != nil {
		return report, err
	}
	pending, err := library.PendingRestore(home, definitionRoot)
	if err != nil {
		return report, fmt.Errorf("check interrupted library restore: %w", err)
	}
	if pending {
		return report, fmt.Errorf("library restore was interrupted; run agentpack sync to recover the definition before preflight")
	}
	project, err := manifest.Load(definitionRoot)
	if err != nil {
		return report, inputError(err)
	}
	lock, err := loadCheckedLock(definitionRoot)
	if err != nil {
		return report, inputError(err)
	}
	effective, err := resolveMode(definitionRoot, project, &lock, options.Mode)
	if err != nil {
		return report, inputError(err)
	}
	report.Mode = effective.Name()
	report.Skills = lock.SkillCount()
	report.Plugins = lock.PluginCount()
	plugins := lock.Plugins()
	for _, skill := range lock.Skills() {
		if staging.SkillIsShadowed(skill, plugins) {
			report.Shadowed++
		}
	}
	identity, err := sourceIdentity(definitionRoot, lock)
	if err != nil {
		return report, fmt.Errorf("identify source: %w", inputError(err))
	}
	report.SourceIdentity = identity
	if digest, err := lockDigest(definitionRoot); err == nil {
		report.LockDigest = digest
	}
	if binding, found, _ := environment.LoadBinding(workspace); found {
		report.Environment = binding.Environment
	}
	cacheReady := true
	if err := cache.VerifyLockCacheIntegrity(lock); err != nil {
		cacheReady = false
		report.Findings = append(report.Findings, environment.Finding{
			Code:     "CACHE_INTEGRITY",
			Severity: environment.SeverityError,
			Message:  err.Error(),
			Remedy:   "run `agentpack sync --repair` (mutates cache) then re-run preflight",
		})
	} else if err := cache.VerifyLockCacheLayout(lock); err != nil {
		cacheReady = false
		report.Findings = append(report.Findings, environment.Finding{
			Code:     "CACHE_MISSING",
			Severity: environment.SeverityError,
			Message:  err.Error(),
			Remedy:   "run `agentpack sync` or `agentpack env restore` to fetch locked inputs, then re-run preflight offline",
		})
	}
	report.Artifacts, err = plannedArtifacts(lock, effective, definitionRoot, workspace, options.Target, cacheReady)
	if err != nil {
		return report, fmt.Errorf("plan effective artifacts: %w", err)
	}
	report.Skills, report.Plugins = 0, 0
	for _, artifact := range report.Artifacts {
		if !artifact.Omitted {
			if artifact.Kind == "skill" {
				report.Skills++
			}
			if artifact.Kind == "plugin" {
				report.Plugins++
			}
		}
	}
	if err := populateFreshness(&report, options, effective); err != nil {
		return report, fmt.Errorf("fingerprint preflight inputs: %w", err)
	}
	appendAmbientFindings(&report, workspace, lock, options)
	for _, artifact := range report.Artifacts {
		if artifact.Omitted {
			continue
		}
		artifactTarget := report.Target
		if target, ok := artifact.Properties["target"].(string); ok {
			artifactTarget = target
		}
		severity := environment.SeverityWarning
		if options.Policy == environment.PolicyCI {
			severity = environment.SeverityViolation
		}
		if preserved, ok := artifact.Properties["scope_preserved"].(bool); ok && !preserved {
			report.Findings = append(report.Findings, environment.Finding{Code: "RULE_SCOPE_DEGRADED", Severity: severity, Source: artifact.ID, Target: artifactTarget, Message: "scoped rule rendered as an unscoped skill; native glob enforcement is not preserved", Evidence: environment.EvidenceGenerated, Remedy: "choose a native rule target, disable this rule in the selected mode, or explicitly accept conversion under policy local"})
		}
		if fields, ok := artifact.Properties["dropped_fields"].([]string); ok && len(fields) > 0 {
			report.Findings = append(report.Findings, environment.Finding{Code: "ARTIFACT_FIELDS_DROPPED", Severity: severity, Source: artifact.ID, Target: artifactTarget, Message: "native rendering drops fields: " + strings.Join(fields, ", "), Evidence: environment.EvidenceGenerated, Remedy: "remove unsupported fields or choose a target that preserves them"})
		}
		if warnings, ok := artifact.Properties["warnings"].([]string); ok {
			for _, warning := range warnings {
				report.Findings = append(report.Findings, environment.Finding{Code: "MCP_RENDER_WARNING", Severity: severity, Source: artifact.ID, Target: artifactTarget, Message: warning, Evidence: environment.EvidenceGenerated, Remedy: "remove unsupported MCP fields or choose a target that preserves them"})
			}
		}
	}

	report.Inheritance = inheritanceNotes(options.Target, options.Policy)
	for _, note := range report.Inheritance {
		report.Findings = append(report.Findings, environment.Finding{Code: "INHERITANCE_BOUNDARY", Severity: environment.SeverityInfo, Message: note, Evidence: environment.EvidenceDeclared})
	}
	appendDetectedInheritance(&report, options)

	if options.StrictExternal {
		report.Findings = append(report.Findings, environment.Finding{
			Code:     "STRICT_EXTERNAL",
			Severity: environment.SeverityInfo,
			Message:  "strict external mode refuses workspace overlay writes",
			Remedy:   "launch claude or opencode, or omit --strict-external for compatibility mode",
		})
		if options.Target != nil && options.Target.UsesWorkspaceOverlay() {
			status, note := environment.ExternalCapability(*options.Target)
			report.Findings = append(report.Findings, environment.Finding{
				Code:     "WORKSPACE_WRITE_REQUIRED",
				Severity: environment.SeverityViolation,
				Target:   string(*options.Target),
				Message:  fmt.Sprintf("%s requires a workspace write (%s)", *options.Target, note),
				Remedy:   "choose claude/opencode, or run without --strict-external (compatibility mode)",
				Winner:   status,
			})
			report.PlannedWrites = append(report.PlannedWrites, plannedOverlayWrite(*options.Target, workspace)...)
		}
	} else if options.Target != nil && options.Target.UsesWorkspaceOverlay() {
		writes := plannedOverlayWrite(*options.Target, workspace)
		report.PlannedWrites = append(report.PlannedWrites, writes...)
		for _, write := range writes {
			report.Findings = append(report.Findings, environment.Finding{
				Code:     "WORKSPACE_WRITE_PLANNED",
				Severity: environment.SeverityWarning,
				Target:   string(*options.Target),
				Source:   write,
				Message:  "compatibility mode will write a workspace overlay",
				Remedy:   "add the path to .gitignore, or use --strict-external with claude/opencode",
			})
		}
	}

	for _, server := range lock.MCPServers {
		switch server.Status {
		case lockfile.MCPUnpinned, lockfile.MCPUnpinnable:
			severity := environment.SeverityWarning
			if options.Policy == environment.PolicyCI {
				severity = environment.SeverityViolation
			}
			report.Findings = append(report.Findings, environment.Finding{
				Code:     "MCP_UNPINNED",
				Severity: severity,
				Source:   "mcp:" + server.Name,
				Message:  fmt.Sprintf("MCP server %s is %s", server.Name, server.Status),
				Remedy:   "pin with `agentpack lock`, or accept explicitly under policy local",
			})
		}
	}
	if project != nil {
		for name, server := range project.MCP.Servers {
			for key := range server.Env {
				if !looksLikeSecret(key) {
					continue
				}
				if os.Getenv(key) == "" {
					report.Findings = append(report.Findings, environment.Finding{
						Code:     "SECRET_NAME_MISSING",
						Severity: environment.SeverityWarning,
						Source:   "mcp:" + name,
						Message:  fmt.Sprintf("required environment variable %s is unset (value not reported)", key),
						Remedy:   "export " + key + " in the process environment before launch",
					})
				}
			}
		}
	}

	if options.Target != nil {
		if path, lookErr := lookPathForTarget(*options.Target); lookErr != nil {
			report.Findings = append(report.Findings, environment.Finding{
				Code:     "BINARY_MISSING",
				Severity: environment.SeverityError,
				Target:   string(*options.Target),
				Message:  lookErr.Error(),
				Remedy:   "install the native CLI or set the harness path env var",
			})
		} else {
			report.Findings = append(report.Findings, environment.Finding{
				Code:     "BINARY_FOUND",
				Severity: environment.SeverityInfo,
				Target:   string(*options.Target),
				Source:   path,
				Message:  "native CLI located",
			})
		}
		var hookFindings []environment.Finding
		if cacheReady {
			hookFindings, err = collectHookFindings(workspace, definitionRoot, lock, effective, *options.Target)
		}
		if err != nil {
			report.Findings = append(report.Findings, environment.Finding{
				Code:     "HOOK_COLLECT_FAILED",
				Severity: environment.SeverityWarning,
				Target:   string(*options.Target),
				Message:  err.Error(),
				Remedy:   "inspect hooks under packages and .agents/hooks",
			})
		} else {
			report.Findings = append(report.Findings, hookFindings...)
		}
	}

	pipeline := staging.Pipeline{
		ProjectRoot:    definitionRoot,
		WorkspaceRoot:  workspace,
		Lock:           lock,
		Manifest:       project,
		Mode:           effective,
		Target:         options.Target,
		StrictExternal: options.StrictExternal,
	}
	materializeRemedy := "run `agentpack sync` to materialize current inputs before probing"
	if filepath.Clean(definitionRoot) != filepath.Clean(workspace) {
		materializeRemedy = "run `agentpack env restore` to publish the selected frozen environment before probing"
	}
	materialized, materializedFound, materializedErr := readMaterializationDigest(definitionRoot, effective.Name())
	if materializedErr != nil {
		report.Findings = append(report.Findings, environment.Finding{Code: "STAGING_UNREADABLE", Severity: environment.SeverityError, Message: materializedErr.Error(), Evidence: environment.EvidenceUnknown, Remedy: materializeRemedy})
	} else if !materializedFound {
		report.Findings = append(report.Findings, environment.Finding{Code: "STAGING_INPUTS_UNKNOWN", Severity: environment.SeverityWarning, Message: "no materialization input identity is recorded", Evidence: environment.EvidenceUnknown, Remedy: materializeRemedy})
	} else if current, err := computeMaterializationDigest(definitionRoot, workspace, effective); err != nil {
		return report, err
	} else if materialized != current {
		report.Findings = append(report.Findings, environment.Finding{Code: "STAGING_INPUTS_STALE", Severity: environment.SeverityViolation, Message: "staged configuration was built from different definition, mode, workspace, or inherited inputs", Evidence: environment.EvidenceGenerated, Remedy: materializeRemedy})
	}
	drift, recorded, driftErr := pipeline.StagedDrift(true)
	if driftErr != nil {
		report.Findings = append(report.Findings, environment.Finding{
			Code:     "STAGING_UNREADABLE",
			Severity: environment.SeverityError,
			Message:  driftErr.Error(),
			Remedy:   materializeRemedy,
		})
	} else if !recorded {
		report.Findings = append(report.Findings, environment.Finding{
			Code:     "STAGING_NOT_RECORDED",
			Severity: environment.SeverityWarning,
			Message:  fmt.Sprintf("no staged record for mode %q", effective.Name()),
			Remedy:   materializeRemedy,
		})
	} else if len(drift) != 0 {
		limit := min(len(drift), 5)
		report.Findings = append(report.Findings, environment.Finding{
			Code:     "STAGING_DRIFT",
			Severity: environment.SeverityError,
			Message:  fmt.Sprintf("%d staged file(s) differ from the last recorded sync", len(drift)),
			Source:   strings.Join(drift[:limit], "; "),
			Remedy:   materializeRemedy,
		})
	}

	if options.ReceiptID != "" {
		applyReceipt(&report, workspace, options)
	}

	if options.ContractPath != "" {
		contract, err := environment.LoadContract(options.ContractPath)
		if err != nil {
			return report, fmt.Errorf("load contract: %w", inputError(err))
		}
		applyContract(&report, contract)
	} else if path := filepath.Join(definitionRoot, "contract.json"); regularFile(path) {
		contract, err := environment.LoadContract(path)
		if err != nil {
			return report, fmt.Errorf("load contract: %w", inputError(err))
		}
		applyContract(&report, contract)
	}

	report.OverallStatus = overallStatus(report)
	report.OK = report.OverallStatus == "ready" || report.OverallStatus == "ready_with_warnings"
	return report, nil
}

// RestoreFrozen ensures locked cache trees exist and rebuilds staging without
// re-resolving pins or rewriting pack.lock. Missing locked inputs fail hard.
func (service Service) RestoreFrozen(ctx context.Context, definitionRoot string) error {
	return service.RestoreFrozenOptions(ctx, definitionRoot, SyncOptions{})
}

// RestoreFrozenOptions is RestoreFrozen with workspace / strict-external controls.
func (service Service) RestoreFrozenOptions(ctx context.Context, definitionRoot string, options SyncOptions) error {
	options.Frozen = true
	options.UpdateLock = false
	options.Repair = false
	options.DryRun = false
	options.VerifyOnly = false
	_, err := service.Sync(ctx, definitionRoot, options)
	return err
}

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
	item := sourceMCPServer{Type: server.Type, Args: redactCredentialArgs(server.Args), Disabled: server.Disabled}
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

func redactCredentialArgs(args []string) []string {
	result := slices.Clone(args)
	redactNext := false
	for i, arg := range result {
		lower := strings.ToLower(arg)
		if redactNext {
			result[i] = "<redacted>"
			redactNext = false
			continue
		}
		if strings.HasPrefix(arg, "-") && credentialFlag(lower) {
			if strings.Contains(arg, "=") {
				result[i] = arg[:strings.IndexByte(arg, '=')+1] + "<redacted>"
			} else {
				redactNext = true
			}
			continue
		}
		result[i] = portablePathValue(arg)
	}
	return result
}

func portablePathValue(value string) string {
	if isHostAbsolutePath(value) {
		return filepath.Base(filepath.FromSlash(strings.ReplaceAll(value, `\`, "/")))
	}
	return value
}

func isHostAbsolutePath(value string) bool {
	return filepath.IsAbs(value) || strings.HasPrefix(value, "/") || isWindowsAbsolutePath(value)
}

func isWindowsAbsolutePath(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func credentialFlag(value string) bool {
	for _, marker := range []string{"token", "secret", "password", "passwd", "api-key", "api_key", "credential", "authorization"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func redactCredentialURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	if parsed.User != nil {
		parsed.User = url.UserPassword("<redacted>", "<redacted>")
	}
	if parsed.RawQuery != "" {
		parts := strings.Split(parsed.RawQuery, "&")
		for i, part := range parts {
			key, _, found := strings.Cut(part, "=")
			if found && credentialFlag(strings.ToLower(key)) {
				parts[i] = key + "=<redacted>"
			}
		}
		parsed.RawQuery = strings.Join(parts, "&")
	}
	return parsed.String()
}

func canonicalStrings(values []string) []string {
	result := slices.Clone(values)
	slices.Sort(result)
	return result
}

func lockDigest(definitionRoot string) (string, error) {
	data, err := os.ReadFile(paths.LockPath(definitionRoot))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func plannedArtifacts(lock lockfile.PackLock, effective mode.Effective, definitionRoot, workspace string, target *base.Target, cacheReady bool) ([]environment.ArtifactRecord, error) {
	var records []environment.ArtifactRecord
	plugins := lock.Plugins()
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].CacheKey < plugins[j].CacheKey })
	skills := lock.Skills()
	sort.Slice(skills, func(i, j int) bool { return skills[i].CacheKey < skills[j].CacheKey })
	appendTree := func(root, module, bareName string, enabled bool, dot bool) error {
		appendRecord := func(record environment.ArtifactRecord) {
			if !record.Omitted {
				for index := range records {
					prior := &records[index]
					if recordTarget, ok := record.Properties["target"].(string); ok {
						if priorTarget, priorHasTarget := prior.Properties["target"].(string); priorHasTarget && priorTarget != recordTarget {
							continue
						}
					}
					same := prior.Kind == record.Kind && prior.Name == record.Name
					if record.OutputPath != "" {
						same = prior.OutputPath == record.OutputPath
					}
					if same && !prior.Omitted {
						prior.Omitted = true
						prior.Winner = record.Winner
						prior.Reason = "overridden by " + record.ID
					}
					for _, output := range recordOutputPaths(record) {
						for _, priorOutput := range recordOutputPaths(*prior) {
							if output == priorOutput {
								if output == record.OutputPath && output == prior.OutputPath {
									continue
								}
								appendArtifactPropertyString(prior, "overwritten_outputs", output)
								appendArtifactPropertyString(&record, "overrides_outputs", output)
							}
						}
					}
				}
			}
			records = append(records, record)
		}
		if _, err := os.Stat(root); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		if dot {
			plan, err := staging.PlanDotAgents(root, effective, target)
			if err != nil {
				return err
			}
			for _, item := range plan.Artifacts {
				kind := map[artifacts.Kind]string{artifacts.Skill: "skill", artifacts.Command: "command", artifacts.Agent: "agent", artifacts.Rule: "rule"}[item.Kind]
				targetName := string(item.Target)
				record := environment.ArtifactRecord{
					ID: module + ":" + item.SourceRelative + ":" + targetName, Kind: kind, Name: item.Name,
					Module: module, Winner: "project", Evidence: environment.EvidenceGenerated,
					Properties: map[string]any{"source_digest": digestBytes(item.Source), "source_kind": kind, "target": targetName},
				}
				if item.NativeSource != "" {
					record.Evidence = environment.EvidenceDeclared
					record.Properties["native_source_path"] = filepath.ToSlash(filepath.Join(".agents", item.NativeSource))
				} else if item.Omitted {
					record.Omitted, record.Reason = true, item.Reason
				} else if item.OutputPath != "" {
					record.OutputPath = item.OutputPath
					record.Properties["output_path"] = item.OutputPath
					record.Properties["rendered_digest"] = digestBytes(item.Output)
				}
				if strings.HasPrefix(record.OutputPath, "skills/") {
					record.Kind = "skill"
				}
				appendRecord(record)
			}
			return nil
		}
		if !dot {
			if bareName == "" && !cache.HasPluginManifest(root) {
				return nil
			}
			if bareName != "" {
				if info, err := os.Stat(filepath.Join(root, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
					return nil
				}
			}
			var roots []staging.HarnessRoot
			if target != nil {
				roots = append(roots, staging.HarnessRoot{Target: *target})
			}
			plan, err := staging.PlanPackageTree(root, module, bareName, effective, roots, bareName == "")
			if err != nil {
				return err
			}
			for _, planned := range plan.Artifacts {
				kind := map[artifacts.Kind]string{artifacts.Skill: "skill", artifacts.Command: "command", artifacts.Agent: "agent", artifacts.Rule: "rule"}[planned.Kind]
				record := environment.ArtifactRecord{ID: module + ":" + planned.SourceRelative, Kind: kind, Name: planned.Parsed.StorageName, Module: module, Winner: "package", Evidence: environment.EvidenceGenerated, Properties: map[string]any{"source_digest": digestBytes(planned.Source), "source_kind": kind}}
				if len(planned.AdditionalOutputs) > 0 {
					record.Properties["additional_outputs"] = slices.Clone(planned.AdditionalOutputs)
				}
				if !enabled || planned.Omitted {
					record.Omitted = true
					record.Reason = planned.Reason
					if record.Reason == "" {
						record.Reason = "disabled by mode or lock"
					}
				}
				if target != nil && !record.Omitted {
					record.OutputPath = planned.OutputPath
					record.Properties["output_path"] = planned.OutputPath
					record.Properties["rendered_digest"] = digestBytes(planned.Output)
					if planned.Kind == artifacts.Rule && strings.HasPrefix(planned.OutputPath, "skills/") && len(planned.Parsed.Globs) > 0 {
						record.Properties["scope_preserved"] = false
					}
					projected, parseErr := artifacts.Parse(planned.OutputPath, string(planned.Output), "")
					if parseErr != nil {
						return parseErr
					}
					var dropped []string
					for key := range planned.Parsed.ExtraFrontmatter {
						if projected == nil {
							dropped = append(dropped, key)
						} else if _, ok := projected.ExtraFrontmatter[key]; !ok {
							dropped = append(dropped, key)
						}
					}
					sort.Strings(dropped)
					if len(dropped) > 0 {
						record.Properties["dropped_fields"] = dropped
					}
				}
				if strings.HasPrefix(record.OutputPath, "skills/") {
					record.Kind = "skill"
				}
				appendRecord(record)
			}
			return nil
		}
		// PackageFiles exposes the same normalized source tree used by staging.
		files, err := staging.PackageFiles(root, module, bareName == "")
		if err != nil {
			return err
		}
		markdown := func(file staging.PackageFile) bool {
			extension := strings.ToLower(filepath.Ext(file.Virtual))
			return extension == ".md" || extension == ".mdc"
		}
		// The package's own skills decide what a converted command, agent or
		// rule of the same name is called, exactly as in staging.
		skillNames := make(map[string]string)
		for _, file := range files {
			if !markdown(file) {
				continue
			}
			if allowed, err := effective.AllowsPackagePath(module, file.Real); err != nil || !allowed {
				continue
			}
			contents, err := os.ReadFile(file.Path)
			if err != nil {
				return err
			}
			if parsed, err := artifacts.Parse(file.Virtual, string(contents), bareName); err == nil && parsed != nil && parsed.Kind == artifacts.Skill {
				skillNames[parsed.StorageName] = file.Real
			}
		}
		converted := staging.NewConvertedNames(skillNames)
		visit := func(file staging.PackageFile) error {
			path, relative := file.Path, file.Real
			allowed, err := effective.AllowsPackagePath(module, relative)
			if err != nil {
				return err
			}
			if !markdown(file) {
				return nil
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			parseRelative := file.Virtual
			parsed, err := artifacts.Parse(parseRelative, string(contents), bareName)
			if err != nil {
				return fmt.Errorf("parse %s artifact: %w", module, err)
			}
			if parsed == nil {
				return nil
			}
			kind := map[artifacts.Kind]string{artifacts.Skill: "skill", artifacts.Command: "command", artifacts.Agent: "agent", artifacts.Rule: "rule"}[parsed.Kind]
			record := environment.ArtifactRecord{ID: module + ":" + relative, Kind: kind, Name: parsed.StorageName, Module: module, Winner: "package", Evidence: environment.EvidenceGenerated, Properties: map[string]any{"source_digest": digestBytes(contents)}}
			if !enabled || !allowed {
				record.Omitted = true
				record.Reason = "disabled by mode or lock"
			}
			if target != nil {
				staged := *parsed
				if !record.Omitted {
					staged = converted.For(parsed, *target)
					record.Name = staged.StorageName
				}
				rendered := staged.Render(*target)
				record.OutputPath = rendered.RelativePath
				record.Properties["output_path"] = rendered.RelativePath
				if rendered.RelativePath != "" {
					record.Properties["rendered_digest"] = digestBytes([]byte(rendered.Contents))
				}
				record.Properties["source_kind"] = kind
				if parsed.Kind == artifacts.Rule && strings.HasPrefix(rendered.RelativePath, "skills/") && len(parsed.Globs) > 0 {
					record.Properties["scope_preserved"] = false
				}
				projected, parseErr := artifacts.Parse(rendered.RelativePath, rendered.Contents, "")
				if parseErr != nil {
					return parseErr
				}
				var dropped []string
				for key := range parsed.ExtraFrontmatter {
					if rendered.RelativePath == "" {
						continue
					}
					if projected == nil {
						dropped = append(dropped, key)
					} else if _, ok := projected.ExtraFrontmatter[key]; !ok {
						dropped = append(dropped, key)
					}
				}
				sort.Strings(dropped)
				if len(dropped) > 0 {
					record.Properties["dropped_fields"] = dropped
				}

				if strings.HasPrefix(rendered.RelativePath, "skills/") {
					record.Kind = "skill"
				}
			}
			appendRecord(record)
			return nil
		}
		for _, file := range files {
			if err := visit(file); err != nil {
				return err
			}
		}
		return nil
	}
	disabled := func(key string) bool {
		for _, value := range lock.Config.DisabledPlugins {
			if value == key {
				return true
			}
		}
		return false
	}
	for _, plugin := range plugins {
		if !cacheReady {
			records = append(records, environment.ArtifactRecord{ID: plugin.Module, Kind: "plugin", Name: plugin.Name, Module: plugin.Module, Winner: "package", Evidence: environment.EvidenceDeclared, Omitted: true, Reason: "locked cache unavailable", Properties: map[string]any{"source_digest": plugin.ContentHash}})
			continue
		}
		root, err := cache.EntryDir(plugin.CacheKey)
		if err != nil {
			return nil, err
		}
		name := plugin.Name
		if name == "" {
			name = staging.SkillFolderName(plugin)
		}
		record := environment.ArtifactRecord{ID: plugin.Module, Kind: "plugin", Name: name, Module: plugin.Module, Winner: "package", Evidence: environment.EvidenceDeclared, Properties: map[string]any{"source_digest": plugin.ContentHash}}
		if disabled(plugin.CacheKey) {
			record.Omitted = true
			record.Reason = "disabled by lock"
		}
		records = append(records, record)
		if err := appendTree(root, plugin.Module, "", !disabled(plugin.CacheKey), false); err != nil {
			return nil, err
		}
	}
	for _, skill := range skills {
		if !cacheReady {
			name := staging.SkillFolderName(skill)
			records = append(records, environment.ArtifactRecord{ID: skill.Module, Kind: "skill", Name: name, Module: skill.Module, Winner: "package", Evidence: environment.EvidenceDeclared, Omitted: true, Reason: "locked cache unavailable", Properties: map[string]any{"source_digest": skill.ContentHash}})
			continue
		}
		root, err := cache.EntryDir(skill.CacheKey)
		if err != nil {
			return nil, err
		}
		if err := appendTree(root, skill.Module, staging.SkillFolderName(skill), !disabled(skill.CacheKey) && !staging.SkillIsShadowed(skill, plugins), false); err != nil {
			return nil, err
		}
	}
	if err := appendTree(paths.ProjectDotAgentsDir(workspace), ".agents", "", true, true); err != nil {
		return nil, err
	}
	if cacheReady {
		hookRecords, err := plannedHookArtifacts(lock, effective, definitionRoot, workspace, target)
		if err != nil {
			return nil, err
		}
		records = append(records, hookRecords...)
		mcpRecords, err := plannedMCPArtifacts(lock, effective, definitionRoot, workspace, target)
		if err != nil {
			return nil, err
		}
		records = append(records, mcpRecords...)
	}
	return records, nil
}

func plannedMCPArtifacts(lock lockfile.PackLock, effective mode.Effective, definitionRoot, workspace string, target *base.Target) ([]environment.ArtifactRecord, error) {
	project, err := manifest.Load(definitionRoot)
	if err != nil {
		return nil, err
	}
	collection, err := staging.CollectMCPPlan(workspace, lock, project, &effective)
	if err != nil {
		return nil, err
	}
	entries := collection.Entries
	bundle, err := paths.StagingPluginsDirForMode(definitionRoot, effective.Name())
	if err != nil {
		return nil, err
	}
	entries, treeFiles, err := mcp.PlanTrees(entries, lock.MCPServers, hooks.Executable(), filepath.Join(bundle, paths.StagedAgentpackBundleName, "mcp-trees"))
	if err != nil {
		return nil, err
	}
	entries = mcp.ApplyPins(entries, lock.MCPServers)
	pluginPlan, err := staging.PlanMCPPluginRoots(definitionRoot, effective.Name(), effective, entries)
	if err != nil {
		return nil, err
	}
	entries = pluginPlan.Entries
	var records []environment.ArtifactRecord
	for index, warning := range collection.Warnings {
		records = append(records, environment.ArtifactRecord{ID: fmt.Sprintf("mcp-omission:%d", index), Kind: "mcp_omission", Name: warning.Source, Winner: "omitted", Omitted: true, Reason: warning.Message, Evidence: environment.EvidenceGenerated})
	}
	for _, source := range pluginPlan.Sources {
		records = append(records, environment.ArtifactRecord{ID: "mcp-plugin-root:" + source.Module, Kind: "mcp_plugin_root", Name: source.Module, Module: source.Module, Winner: "package", Evidence: environment.EvidenceGenerated, Properties: map[string]any{"source_digest": source.Digest}})
	}
	for _, file := range pluginPlan.Files {
		path := filepath.ToSlash(filepath.Join("plugin-roots", filepath.Base(filepath.Dir(file.Path)), filepath.Base(file.Path)))
		for _, root := range pluginPlan.Roots {
			if relative, err := filepath.Rel(root, file.Path); err == nil && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
				path = filepath.ToSlash(filepath.Join("plugin-roots", filepath.Base(root), relative))
				break
			}
		}
		properties := map[string]any{"target": "mcp_plugin_root", "mode": file.Mode.Perm()}
		if file.LinkTarget != "" {
			properties["link_target"] = file.LinkTarget
		} else {
			properties["rendered_digest"] = digestBytes(file.Contents)
		}
		records = append(records, environment.ArtifactRecord{ID: "mcp-plugin-file:" + file.Module + ":" + path, Kind: "mcp_plugin_file", Name: filepath.Base(path), Module: file.Module, Winner: "package", OutputPath: path, Evidence: environment.EvidenceGenerated, Properties: properties})
	}
	for _, directory := range pluginPlan.DataDirs {
		records = append(records, environment.ArtifactRecord{ID: "mcp-plugin-data:" + filepath.Base(directory), Kind: "mcp_plugin_data_dir", Name: filepath.Base(directory), Winner: "plugin", OutputPath: filepath.ToSlash(filepath.Join("plugin-data", filepath.Base(directory))), Evidence: environment.EvidenceGenerated})
	}
	for _, file := range treeFiles {
		relative, err := filepath.Rel(filepath.Join(bundle, paths.StagedAgentpackBundleName), file.Path)
		if err != nil || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return nil, fmt.Errorf("planned MCP tree output escapes bundle: %s", file.Path)
		}
		relative = filepath.ToSlash(relative)
		records = append(records, environment.ArtifactRecord{ID: "mcp-tree:" + relative, Kind: "mcp_tree", Name: filepath.Base(relative), Winner: "lock", OutputPath: relative, Evidence: environment.EvidenceGenerated, Properties: map[string]any{"rendered_digest": digestBytes(file.Contents)}})
	}
	targets := base.AllTargets()
	if target != nil {
		targets = []base.Target{*target}
	}
	targetSeedConfigs := make(map[base.Target][]byte)
	for _, current := range targets {
		candidate, err := registry.ByTarget(current)
		if err != nil {
			return nil, err
		}
		ctx := base.StageContext{ProjectRoot: definitionRoot, WorkspaceRoot: workspace, Mode: effective}
		if seedPath, label := inheritedConfigSeed(current); seedPath != "" {
			seed, err := os.ReadFile(seedPath)
			if err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			properties := map[string]any{"source": label, "exists": err == nil, "merge_order": []string{"native user seed retained", "agentpack-owned sections overlaid"}}
			if err == nil {
				properties["source_digest"] = digestBytes(seed)
				shape, err := redactedSeedShape(seedPath, seed)
				if err != nil {
					return nil, err
				}
				properties["ordered_values"] = shape
			}
			records = append(records, environment.ArtifactRecord{ID: "settings-seed:" + string(current), Kind: "settings_seed", Name: label, Winner: "user_seed", Evidence: environment.EvidenceGenerated, Properties: properties})
		}
		if current == base.Grok {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			seed, err := base.ReadMCPSeed(filepath.Join(home, ".grok", "config.toml"))
			if err != nil {
				return nil, err
			}
			contents, err := grok.EnableBundleData(seed)
			if err != nil {
				return nil, fmt.Errorf("plan Grok plugin settings: %w", err)
			}
			digest, err := base.MCPTOMLScopeDigest(contents, "plugins")
			if err != nil {
				return nil, err
			}
			records = append(records, environment.ArtifactRecord{ID: "settings:grok:plugins", Kind: "settings_section", Name: "plugins", Winner: "agentpack_overlay", OutputPath: "config.toml", Evidence: environment.EvidenceGenerated, Properties: map[string]any{"target": string(current), "scope": "plugins", "scope_digest": digest, "merge_order": []string{"user ~/.grok/config.toml", "enable agentpack-bundle while retaining enabled plugins"}}})
		}
		projection := base.MCPProjection{}
		if len(entries) != 0 {
			projection, err = candidate.PlanMCP(entries, ctx)
			if err != nil {
				return nil, fmt.Errorf("plan MCP %s: %w", current, err)
			}
		}
		for _, file := range projection.Files {
			targetSeedConfigs[current] = append([]byte(nil), file.Contents...)
		}
		if current == base.Claude {
			names := make([]string, 0, len(entries))
			for _, name := range entries.Names() {
				server := entries[name].Server
				if server.Disabled == nil || !*server.Disabled {
					names = append(names, name)
				}
			}
			contents, err := claude.SettingsContents(names, claude.KeepAttribution())
			if err != nil {
				return nil, err
			}
			settingsPath, err := claude.SettingsPath(ctx)
			if err != nil {
				return nil, err
			}
			relative, err := filepath.Rel(filepath.Join(bundle, paths.StagedAgentpackBundleName), settingsPath)
			if err != nil || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
				return nil, fmt.Errorf("planned Claude settings escape bundle: %s", settingsPath)
			}
			records = append(records, environment.ArtifactRecord{ID: "settings:claude:agentpack-overlay", Kind: "settings_file", Name: "agentpack-settings.json", Winner: "agentpack_overlay", OutputPath: filepath.ToSlash(relative), Evidence: environment.EvidenceGenerated, Properties: map[string]any{"target": string(current), "rendered_digest": digestBytes(contents), "merge_order": []string{"agentpack_attribution_policy", "merged_mcp_allowlist"}}})
		}
		for _, server := range projection.Servers {
			properties := map[string]any{"target": string(current), "source": server.Source}
			if len(server.Dropped) != 0 {
				properties["dropped_fields"] = slices.Clone(server.Dropped)
			}
			var warnings []string
			for _, warning := range projection.Warnings {
				if strings.HasPrefix(warning, "MCP server "+server.Name+": ") {
					warnings = append(warnings, warning)
				}
			}
			if len(warnings) != 0 {
				properties["warnings"] = warnings
			}
			records = append(records, environment.ArtifactRecord{ID: "mcp:" + server.Name + ":" + string(current), Kind: "mcp_server", Name: server.Name, Module: server.Module, Winner: server.Winner, Evidence: environment.EvidenceGenerated, Properties: properties})
		}
		for _, file := range projection.Files {
			name := filepath.Base(file.Path)
			if strings.Contains(filepath.ToSlash(file.Path), "/.cursor/") {
				name = "fake-home/.cursor/" + name
			}
			records = append(records, environment.ArtifactRecord{ID: "mcp-file:" + string(current) + ":" + name, Kind: "mcp_file", Name: name, Winner: "merged", OutputPath: name, Evidence: environment.EvidenceGenerated, Properties: map[string]any{"target": string(current), "scope": file.Scope, "scope_digest": file.ScopeDigest, "merge_order": slices.Clone(file.MergeOrder)}})
		}
	}
	lspPlan, err := staging.CollectLSPPlan(lock, &effective)
	if err != nil {
		return nil, err
	}
	for index, warning := range lspPlan.Warnings {
		records = append(records, environment.ArtifactRecord{ID: fmt.Sprintf("lsp-source-omission:%d", index), Kind: "lsp_omission", Name: warning.Source, Winner: "omitted", Omitted: true, Reason: warning.Message, Evidence: environment.EvidenceGenerated})
	}
	lspServers, err := staging.PlanLSPRoots(definitionRoot, effective.Name(), lspPlan.Entries)
	if err != nil {
		return nil, err
	}
	for _, current := range targets {
		candidate, err := registry.ByTarget(current)
		if err != nil {
			return nil, err
		}
		projection, err := candidate.PlanLSP(lspServers, base.StageContext{ProjectRoot: definitionRoot, WorkspaceRoot: workspace, Mode: effective, SeedConfig: targetSeedConfigs[current]})
		if errors.Is(err, base.ErrNoLSP) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("plan LSP %s: %w", current, err)
		}
		for index, warning := range projection.Warnings {
			records = append(records, environment.ArtifactRecord{ID: fmt.Sprintf("lsp-warning:%s:%d", current, index), Kind: "lsp_omission", Name: string(current), Winner: "omitted", Omitted: true, Reason: warning, Evidence: environment.EvidenceGenerated})
		}
		for _, name := range projection.Omitted {
			records = append(records, environment.ArtifactRecord{ID: "lsp:" + string(current) + ":" + name, Kind: "lsp_server", Name: name, Winner: "user", Omitted: true, Reason: "existing target configuration wins", Evidence: environment.EvidenceGenerated})
		}
		for _, file := range projection.Files {
			records = append(records, environment.ArtifactRecord{ID: "lsp-file:" + string(current) + ":" + filepath.Base(file.Path), Kind: "lsp_file", Name: filepath.Base(file.Path), Winner: "merged", OutputPath: filepath.Base(file.Path), Evidence: environment.EvidenceGenerated, Properties: map[string]any{"target": string(current), "scope": file.Scope, "rendered_digest": digestBytes(file.Contents)}})
		}
	}
	return records, nil
}

func inheritedConfigSeed(target base.Target) (string, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	switch target {
	case base.Codex:
		return filepath.Join(home, ".codex", "config.toml"), "~/.codex/config.toml"
	case base.OpenCode:
		roots, err := opencode.NativeUserConfigRoots()
		if err != nil || len(roots) == 0 {
			return "", ""
		}
		path := filepath.Join(roots[0], "opencode.json")
		return path, path
	case base.Grok:
		return filepath.Join(home, ".grok", "config.toml"), "~/.grok/config.toml"
	case base.Cursor:
		return filepath.Join(home, ".cursor", "cli-config.json"), "~/.cursor/cli-config.json"
	default:
		return "", ""
	}
}

func redactedSeedShape(path string, data []byte) (any, error) {
	var value any
	if strings.EqualFold(filepath.Ext(path), ".toml") {
		if err := toml.Unmarshal(data, &value); err != nil {
			return nil, err
		}
	} else if len(data) != 0 {
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
	}
	return redactSeedValue(value), nil
}

func redactSeedValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			result[key] = redactSeedValue(child)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for index, child := range value {
			result[index] = redactSeedValue(child)
		}
		return result
	case string:
		return "<redacted:string>"
	case bool:
		return "<redacted:bool>"
	case int64, int, float64:
		return "<redacted:number>"
	case nil:
		return "<redacted:null>"
	default:
		return "<redacted>"
	}
}

func plannedHookArtifacts(lock lockfile.PackLock, effective mode.Effective, definitionRoot, workspace string, target *base.Target) ([]environment.ArtifactRecord, error) {
	seeded := ""
	if target == nil || *target == base.Codex {
		seeded = seededCodexHooksPath()
	}
	bundle, err := hooks.Collect(workspace, lock, seeded, effective)
	if err != nil {
		return nil, err
	}
	targets := base.AllTargets()
	if target != nil {
		targets = []base.Target{*target}
	}
	var records []environment.ArtifactRecord
	for _, current := range targets {
		renderer := registry.Renderer(current)
		if renderer == nil {
			continue
		}
		candidate, err := registry.ByTarget(current)
		if err != nil {
			return nil, err
		}
		ctx := base.StageContext{ProjectRoot: definitionRoot, WorkspaceRoot: workspace, Mode: effective}
		root, err := candidate.StagedRoot(ctx)
		if err != nil {
			return nil, err
		}
		plan, err := hooks.PlanTarget(bundle, current, renderer, hooks.RenderContext{ProjectRoot: definitionRoot, TargetRoot: root}, effective)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]int)
		for _, file := range plan.Files {
			relative, err := filepath.Rel(root, file.Path)
			if err != nil || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
				return nil, fmt.Errorf("planned hook output escapes %s: %s", root, file.Path)
			}
			relative = filepath.ToSlash(relative)
			record := environment.ArtifactRecord{
				ID: "hook-file:" + string(current) + ":" + relative, Kind: "hook_file", Name: filepath.Base(relative), Module: file.Module, Winner: "hook", OutputPath: relative, Evidence: environment.EvidenceGenerated,
				Properties: map[string]any{"target": string(current), "source_path": file.Source, "rendered_digest": digestBytes(file.Contents)},
			}
			if prior, found := seen[relative]; found {
				record.Winner = "hook"
				record.Properties["overrides"] = records[prior].ID
				records[prior].Omitted = true
				records[prior].Winner = record.ID
				records[prior].Reason = "overridden by later hook output"
			}
			seen[relative] = len(records)
			records = append(records, record)
		}
		for _, hook := range plan.Hooks {
			id := fmt.Sprintf("hook:%s:%s:%s:%d:%d:%d", current, hook.Origin.Module, hook.Origin.SourceRelative, hook.Origin.EventIndex, hook.Origin.MatcherGroupIndex, hook.Origin.HookIndex)
			records = append(records, environment.ArtifactRecord{ID: id, Kind: "hook", Name: string(hook.Event), Module: hook.Origin.Module, Winner: "hook", Evidence: environment.EvidenceGenerated, Properties: map[string]any{"target": string(current), "source_path": hook.Origin.SourceFile, "handler": string(hook.Handler.Kind)}})
		}
	}
	return records, nil
}

func seededCodexHooksPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "hooks.json")
}

func recordOutputPaths(record environment.ArtifactRecord) []string {
	paths := []string{}
	if record.OutputPath != "" {
		paths = append(paths, record.OutputPath)
	}
	if additional, ok := record.Properties["additional_outputs"].([]string); ok {
		paths = append(paths, additional...)
	}
	return paths
}

func appendArtifactPropertyString(record *environment.ArtifactRecord, key, value string) {
	values, _ := record.Properties[key].([]string)
	if !slices.Contains(values, value) {
		record.Properties[key] = append(values, value)
	}
}

func markArtifactWinner(report *environment.Report, name, winner, reason string) {
	for index := range report.Artifacts {
		artifact := &report.Artifacts[index]
		if artifact.Kind != "skill" || artifact.Module == "user" || artifact.Module == "project" || !strings.EqualFold(artifact.Name, name) {
			continue
		}
		artifact.Winner = winner
		artifact.Omitted = true
		artifact.Reason = reason
		artifact.Evidence = environment.EvidenceGenerated
	}
}

func appendAmbientFindings(report *environment.Report, workspace string, lock lockfile.PackLock, options PreflightOptions) {
	packSkills := map[string]string{}
	for _, artifact := range report.Artifacts {
		if artifact.Kind == "skill" && !artifact.Omitted {
			packSkills[strings.ToLower(artifact.Name)] = artifact.Name
		}
	}
	home, _ := os.UserHomeDir()
	userSkills := make(map[string]struct{})
	userTargets := make(map[string][]string)
	for _, candidate := range registry.All() {
		if options.Target != nil && candidate.ID() != *options.Target {
			continue
		}
		names, err := staging.UserSkillNames(candidate.NativeUserRoots(home))
		if err != nil {
			report.Findings = append(report.Findings, environment.Finding{
				Code: "AMBIENT_SCAN_FAILED", Severity: environment.SeverityWarning,
				Target: string(candidate.ID()), Message: err.Error(), Evidence: environment.EvidenceUnknown,
				Remedy: "fix permissions on the target's native user skill directories",
			})
			return
		}
		for name := range names {
			userSkills[name] = struct{}{}
			userTargets[name] = append(userTargets[name], string(candidate.ID()))
		}
	}
	var projectSkills map[string]struct{}
	if options.Target == nil || *options.Target == base.Claude {
		var err error
		projectSkills, err = staging.ProjectClaudeSkillNames(workspace)
		if err != nil {
			report.Findings = append(report.Findings, environment.Finding{Code: "AMBIENT_SCAN_FAILED", Severity: environment.SeverityError, Message: err.Error(), Evidence: environment.EvidenceUnknown})
			return
		}
	}
	for _, layer := range []struct {
		names  map[string]struct{}
		winner string
	}{{userSkills, "user"}, {projectSkills, "project"}} {
		keys := make([]string, 0, len(layer.names))
		for key := range layer.names {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, lower := range keys {
			display := lower
			report.Artifacts = append(report.Artifacts, environment.ArtifactRecord{ID: layer.winner + ":" + display, Kind: "skill", Name: display, Module: layer.winner, Winner: layer.winner, Evidence: environment.EvidenceGenerated})
			if _, packaged := packSkills[lower]; !packaged {
				severity := environment.SeverityInfo
				if options.Policy == environment.PolicyCI {
					severity = environment.SeverityViolation
				}
				report.Findings = append(report.Findings, environment.Finding{Code: "AMBIENT_ARTIFACT", Severity: severity, Source: display, Winner: layer.winner, Message: "undeclared skill exists in a native discovery root", Evidence: environment.EvidenceGenerated, Remedy: "use a controlled home/workspace for CI or narrow the policy to local"})
			}
		}
	}

	// Project skill roots beyond .claude/skills. .agents/skills is copied over a packaged skill of
	// the same name in the Claude bundle and listed next to the staged copy by Codex; the
	// agent-specific roots matter only to their own agent.
	projectRoots := []string{".agents/skills"}
	for _, extra := range []struct {
		target base.Target
		root   string
	}{{base.Codex, ".codex/skills"}, {base.OpenCode, ".opencode/skills"}} {
		if options.Target == nil || *options.Target == extra.target {
			projectRoots = append(projectRoots, extra.root)
		}
	}
	// Skills that .agents itself contributes are project content, not packaged skills.
	lockedSkills := map[string]string{}
	for _, artifact := range report.Artifacts {
		if artifact.Kind != "skill" || artifact.Module == ".agents" {
			continue
		}
		// The plan already omits a locked skill that .agents overrides; it is still a locked name.
		if !artifact.Omitted || strings.HasPrefix(artifact.Reason, "overridden by .agents:") {
			lockedSkills[strings.ToLower(artifact.Name)] = artifact.Name
		}
	}
	for _, root := range projectRoots {
		names, rootErr := staging.ProjectSkillNames(workspace, root)
		if rootErr != nil {
			report.Findings = append(report.Findings, environment.Finding{Code: "AMBIENT_SCAN_FAILED", Severity: environment.SeverityError, Message: rootErr.Error(), Evidence: environment.EvidenceUnknown})
			return
		}
		for lower, display := range lockedSkills {
			if _, hit := names[lower]; !hit {
				continue
			}
			severity := environment.SeverityWarning
			if options.Policy == environment.PolicyCI {
				severity = environment.SeverityViolation
			}
			alreadyProject := false
			for _, artifact := range report.Artifacts {
				alreadyProject = alreadyProject || (artifact.Kind == "skill" && artifact.Module == "project" && strings.EqualFold(artifact.Name, lower))
			}
			markArtifactWinner(report, display, "project", "project "+root+" has a skill with the packaged skill's name")
			if !alreadyProject {
				report.Artifacts = append(report.Artifacts, environment.ArtifactRecord{ID: "project:" + lower, Kind: "skill", Name: lower, Module: "project", Winner: "project", Evidence: environment.EvidenceGenerated})
			}
			report.Findings = append(report.Findings, environment.Finding{
				Code: "AMBIENT_ARTIFACT", Severity: severity, Source: display, Winner: "project",
				Message:  fmt.Sprintf("project %s/%s has the packaged skill's name", root, display),
				Remedy:   "rename or remove the project skill, disable the package in a mode, or accept under --policy local",
				Evidence: environment.EvidenceGenerated,
			})
		}
	}
	for lower, display := range packSkills {
		if _, hit := userSkills[lower]; hit {
			severity := environment.SeverityWarning
			if options.Policy == environment.PolicyCI || options.StrictExternal {
				severity = environment.SeverityViolation
			}
			if options.Target != nil {
				markArtifactWinner(report, display, "user", "user skill wins over packaged skill")
			}
			for _, target := range userTargets[lower] {
				report.Findings = append(report.Findings, environment.Finding{
					Code: "CONFIG_SHADOWED", Severity: severity, Source: display, Winner: "user", Target: target,
					Message:  fmt.Sprintf("user skill %q overrides the packaged skill for %s", display, target),
					Remedy:   "rename/remove the user skill, or accept under --policy local without --strict-external",
					Evidence: environment.EvidenceGenerated,
				})
			}
		}
		if _, hit := projectSkills[lower]; hit {
			severity := environment.SeverityWarning
			if options.Policy == environment.PolicyCI {
				severity = environment.SeverityViolation
			}
			markArtifactWinner(report, display, "project", "project .claude/skills wins over packaged skill")
			report.Findings = append(report.Findings, environment.Finding{
				Code: "AMBIENT_ARTIFACT", Severity: severity, Source: display, Winner: "project",
				Message:  fmt.Sprintf("project .claude/skills/%s shadows the packaged skill", display),
				Remedy:   "remove the project skill, disable the package in a mode, or accept under --policy local",
				Evidence: environment.EvidenceGenerated,
			})
		}
	}
}

func applyReceipt(report *environment.Report, workspace string, options PreflightOptions) {
	receipt, err := environment.LoadReceipt(workspace, options.ReceiptID)
	if errors.Is(err, environment.ErrNoReceipt) {
		// Nothing has been observed yet: required observations stay unknown, the evidence is not damaged.
		report.Findings = append(report.Findings, environment.Finding{
			Code: "RECEIPT_MISSING", Severity: environment.SeverityWarning,
			Message: "no probe receipt exists for this workspace", Evidence: environment.EvidenceUnknown,
			Remedy: "run `agentpack probe --agent <target>` to observe the native configuration",
		})
		return
	}
	if err != nil {
		report.Findings = append(report.Findings, environment.Finding{
			Code: "RECEIPT_STALE", Severity: environment.SeverityError,
			Message: err.Error(), Evidence: environment.EvidenceUnknown,
			Remedy: "run `agentpack probe --agent <target>` to create a fresh receipt",
		})
		return
	}
	stale := receipt.SourceIdentity != report.SourceIdentity || receipt.LockDigest != report.LockDigest || receipt.Mode != report.Mode || receipt.WorkspaceDigest != report.WorkspaceDigest || receipt.GenerationID != report.GenerationID || receipt.PolicyDigest != report.PolicyDigest
	expected := report.ExpectedTarget
	stale = stale || expected.Adapter == "" || receipt.Target.Adapter != expected.Adapter || receipt.Target.AdapterRevision != expected.AdapterRevision || receipt.Target.ExecutableIdentity != expected.ExecutableIdentity || receipt.Target.CapabilityRevision != expected.CapabilityRevision
	if stale {
		report.Findings = append(report.Findings, environment.Finding{Code: "RECEIPT_STALE", Severity: environment.SeverityViolation, Message: "receipt identities do not match current definition, workspace inputs, generation, policy, or native adapter", Evidence: environment.EvidenceUnknown, Remedy: "run `agentpack probe --agent <target>` again after sync/restore"})
		return
	}
	if receipt.Termination.Status != "completed" || receipt.Termination.NativeExitCode != 0 || (receipt.OverallStatus != "ready" && receipt.OverallStatus != "ready_with_warnings") {
		report.Findings = append(report.Findings, environment.Finding{Code: "PROBE_FAILED", Severity: environment.SeverityError, Message: "receipt does not describe a successful completed probe", Evidence: environment.EvidenceUnknown, Remedy: "resolve probe failures and run probe again"})
		return
	}
	for _, prop := range receipt.Properties {
		if prop.Evidence != environment.EvidenceObserved {
			continue
		}
		info, statErr := os.Stat(prop.EvidenceRef)
		digest := ""
		var err error
		if statErr == nil && info.Mode().IsRegular() && info.Size() <= 2<<20 {
			digest, err = environment.ExecutableIdentity(prop.EvidenceRef)
		} else {
			err = fmt.Errorf("native evidence must be a bounded regular file")
		}
		if prop.EvidenceRef == "" || prop.EvidenceDigest == "" || err != nil || digest != prop.EvidenceDigest {
			report.Findings = append(report.Findings, environment.Finding{Code: "OBSERVATION_UNAVAILABLE", Severity: environment.SeverityError, Message: "native evidence is unavailable or differs from its recorded digest", Evidence: environment.EvidenceUnknown, Remedy: "run probe again to retain current diagnostic evidence"})
			return
		}
	}
	// Complete coverage alone never manufactures positive observations.
	report.Coverage = receipt.Coverage
	report.Properties = receipt.Properties

	for _, prop := range receipt.Properties {
		if prop.Evidence != environment.EvidenceObserved {
			continue
		}
		report.Findings = append(report.Findings, environment.Finding{
			Code: "NATIVE_OBSERVED", Severity: environment.SeverityInfo,
			Source: prop.ArtifactID, Message: fmt.Sprintf("observed %s=%v via %s", prop.Property, prop.Value, prop.Method),
			Evidence: environment.EvidenceObserved,
		})
	}
}

func applyContract(report *environment.Report, contract environment.Contract) {
	results := environment.EvaluateContract(contract, *report)
	report.ContractResults = results
	unknownMode := strings.ToLower(strings.TrimSpace(contract.UnknownRequired))
	if unknownMode == "" {
		unknownMode = "fail"
	}
	requirements := map[string]environment.Requirement{}
	for _, req := range contract.Requirements {
		requirements[req.ID] = req
	}
	for _, result := range results {
		reqSeverity := requirements[result.RequirementID].Severity
		violationSeverity := environment.SeverityViolation
		if reqSeverity == environment.SeverityInfo || reqSeverity == environment.SeverityWarning {
			violationSeverity = reqSeverity
		}
		switch result.Result {
		case "violated":
			report.Findings = append(report.Findings, environment.Finding{
				Code: result.FindingCode, Severity: violationSeverity,
				Message: result.Message, Remedy: "adjust the environment or add a scoped allowance with rationale",
				Evidence: environment.EvidenceGenerated,
			})
		case "unknown":
			severity := environment.SeverityWarning
			if unknownMode == "fail" {
				severity = violationSeverity
			} else if unknownMode == "allow" {
				severity = environment.SeverityInfo
			}
			code := result.FindingCode
			if code == "" {
				code = "OBSERVATION_UNAVAILABLE"
			}
			report.Findings = append(report.Findings, environment.Finding{
				Code: code, Severity: severity, Message: result.Message,
				Remedy:   "run an explicit native probe, or narrow the contract to generated evidence",
				Evidence: environment.EvidenceUnknown,
			})
		}
	}
}

func overallStatus(report environment.Report) string {
	hasViolation, hasError, hasBlockingUnknown, hasWarning := false, false, false, false
	for _, finding := range report.Findings {
		switch finding.Severity {
		case environment.SeverityViolation:
			hasViolation = true
		case environment.SeverityError:
			hasError = true
		case environment.SeverityWarning:
			hasWarning = true
		}
		if finding.Evidence == environment.EvidenceUnknown &&
			(finding.Severity == environment.SeverityViolation || finding.Severity == environment.SeverityError) {
			hasBlockingUnknown = true
		}
	}
	switch {
	case hasError:
		return "error"
	case hasBlockingUnknown:
		return "unknown"
	case hasViolation:
		return "violated"
	case hasWarning:
		return "ready_with_warnings"
	default:
		return "ready"
	}
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func inheritanceNotes(target *base.Target, policy environment.Policy) []string {
	notes := []string{
		"credentials, remote MCP services, npm/npx installs, and model answers stay outside the portable closure",
	}
	if target == nil {
		return notes
	}
	switch *target {
	case base.Claude:
		notes = append(notes, "Claude still reads real ~/.claude.json and ~/.claude/settings.json; agentpack only adds --plugin-dir/--settings")
	case base.OpenCode:
		notes = append(notes, "OpenCode config root is redirected; user files may be seeded into the staged root when present")
	case base.Codex:
		notes = append(notes, "Codex keeps native history/auth continuity under CODEX_HOME bridging")
	case base.OMP:
		notes = append(notes, "OMP uses an additive --plugin-dir and keeps native profile settings, credentials, sessions and project discovery")
	case base.Grok:
		notes = append(notes, "Grok reads real ~/.grok and ~/.claude in addition to GROK_HOME")
	case base.Cursor:
		notes = append(notes, "Cursor fake HOME bridges user cli-config/mcp, seeds ~/.cursor/skills, and may write .cursor/agents in the workspace")
	case base.Agy:
		notes = append(notes, "Antigravity leaves HOME untouched and writes a workspace plugin overlay")
	}
	if policy == environment.PolicyCI {
		notes = append(notes, "CI policy treats undeclared inheritance as a violation")
	}
	return notes
}

func plannedOverlayWrite(target base.Target, workspace string) []string {
	switch target {
	case base.Cursor:
		return []string{filepath.Join(workspace, ".cursor", "agents")}
	case base.Agy:
		return []string{filepath.Join(workspace, ".agents", "plugins", "agentpack-bundle")}
	default:
		return nil
	}
}

func collectHookFindings(workspace, definitionRoot string, lock lockfile.PackLock, effective mode.Effective, target base.Target) ([]environment.Finding, error) {
	seeded := ""
	if target == base.Codex {
		seeded = seededCodexHooksPath()
	}
	bundle, err := hooks.Collect(workspace, lock, seeded, effective)
	if err != nil {
		return nil, err
	}
	renderer := registry.Renderer(target)
	if renderer == nil {
		return nil, nil
	}
	candidate, err := registry.ByTarget(target)
	if err != nil {
		return nil, err
	}
	ctx := base.StageContext{ProjectRoot: definitionRoot, WorkspaceRoot: workspace, Mode: effective}
	root, err := candidate.StagedRoot(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := hooks.PlanTarget(bundle, target, renderer, hooks.RenderContext{ProjectRoot: definitionRoot, TargetRoot: root}, effective)
	if err != nil {
		return nil, err
	}
	var findings []environment.Finding
	for _, diagnostic := range plan.Skipped {
		findings = append(findings, environment.Finding{Code: "HOOK_SKIPPED", Severity: environment.SeverityWarning, Source: diagnostic.Source, Target: string(target), Message: diagnostic.Message, Remedy: "correct the hook definition or remove the unsupported event/handler"})
	}
	for _, diagnostic := range plan.Diagnostics {
		severity := environment.SeverityWarning
		code := "HOOK_DIAGNOSTIC"
		switch strings.ToLower(diagnostic.Level) {
		case "error":
			severity = environment.SeverityError
			code = "HOOK_ERROR"
		case "omitted", "unsupported":
			code = "HOOK_UNSUPPORTED"
		case "degraded":
			code = "HOOK_DEGRADED"
		}
		findings = append(findings, environment.Finding{
			Code:     code,
			Severity: severity,
			Source:   diagnostic.Source,
			Target:   string(target),
			Message:  diagnostic.Message,
			Remedy:   "disable the hook, choose a target with native support, or accept the finding under policy local",
		})
	}
	if plan.Summary.Omitted != 0 || plan.Summary.Degraded != 0 || plan.Summary.Emulated != 0 {
		findings = append(findings, environment.Finding{
			Code:     "HOOK_SUMMARY",
			Severity: environment.SeverityInfo,
			Target:   string(target),
			Message:  fmt.Sprintf("hooks: native=%d emulated=%d degraded=%d omitted=%d", plan.Summary.Native, plan.Summary.Emulated, plan.Summary.Degraded, plan.Summary.Omitted),
		})
	}
	return findings, nil
}

func lookPathForTarget(target base.Target) (string, error) {
	envName, binary := "", string(target)
	switch target {
	case base.Claude:
		envName, binary = "CLAUDE_CODE_PATH", "claude"
	case base.OpenCode:
		envName, binary = "OPENCODE_PATH", "opencode"
	case base.Codex:
		envName, binary = "CODEX_PATH", "codex"
	case base.OMP:
		envName, binary = "OMP_PATH", "omp"
	case base.Grok:
		envName, binary = "GROK_PATH", "grok"
	case base.Cursor:
		envName, binary = "CURSOR_AGENT_PATH", "cursor-agent"
	case base.Agy:
		envName, binary = "AGY_PATH", "agy"
	}
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		info, err := os.Stat(value)
		if err != nil {
			return "", fmt.Errorf("native CLI configured by %s is unavailable: %w", envName, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("native CLI configured by %s is not a regular file", envName)
		}
		return value, nil
	}
	return exec.LookPath(binary)
}

func looksLikeSecret(key string) bool {
	upper := strings.ToUpper(key)
	return strings.Contains(upper, "KEY") || strings.Contains(upper, "TOKEN") ||
		strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD")
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func populateFreshness(report *environment.Report, options PreflightOptions, effective mode.Effective) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	inputs := workspaceConfigInputs(report.WorkspaceRoot, home)
	// Project skill roots that Codex and OpenCode discover on their own: a skill added there
	// changes what the agent lists, so it must retire earlier observations.
	inputs = append(inputs, filepath.Join(report.WorkspaceRoot, ".codex", "skills"), filepath.Join(report.WorkspaceRoot, ".opencode", "skills"))
	inputs = append(inputs, inheritedConfigPaths(options.Target, report.WorkspaceRoot)...)
	report.WorkspaceDigest, err = fingerprintInputs(inputs)
	if err != nil {
		return err
	}
	stagedPath, err := paths.StagedManifestPath(report.DefinitionRoot, effective.Name())
	if err != nil {
		return err
	}
	stagedBytes, err := os.ReadFile(stagedPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var stagedConfigs []string
	if options.Target != nil {
		candidate, err := registry.ByTarget(*options.Target)
		if err != nil {
			return err
		}
		ctx := base.StageContext{ProjectRoot: report.DefinitionRoot, WorkspaceRoot: report.WorkspaceRoot, Mode: effective}
		root, err := candidate.StagedRoot(ctx)
		if err != nil {
			return err
		}
		for _, name := range []string{"config.toml", "opencode.json", "cli-config.json", "AGENTS.md", "CLAUDE.md", "plugin.json", ".claude-plugin/plugin.json", "hooks.json", ".mcp.json", "mcp.json", "mcp_config.json"} {
			stagedConfigs = append(stagedConfigs, filepath.Join(root, filepath.FromSlash(name)))
		}
		if *options.Target == base.Claude {
			path, err := claude.SettingsPath(ctx)
			if err != nil {
				return err
			}
			stagedConfigs = append(stagedConfigs, path)
		}
	}
	stagedConfigDigest, err := fingerprintInputs(stagedConfigs)
	if err != nil {
		return err
	}
	report.GenerationID = digestBytes(append([]byte(report.SourceIdentity+"\x00"+effective.FingerprintMaterial()+"\x00"+report.WorkspaceDigest+"\x00"+stagedConfigDigest+"\x00"), stagedBytes...))
	contractPath := options.ContractPath
	if contractPath == "" {
		contractPath = filepath.Join(report.DefinitionRoot, "contract.json")
	}
	contractBytes, err := os.ReadFile(contractPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	report.PolicyDigest = digestBytes(append([]byte(fmt.Sprintf("policy=%s\nstrict=%t\n", options.Policy, options.StrictExternal)), contractBytes...))
	if options.Target != nil {
		report.ExpectedTarget.Adapter = string(*options.Target)
		var capability map[string]string
		switch *options.Target {
		case base.Claude:
			capability = claude.Capability()
		case base.Codex:
			capability = codex.Capability()
		case base.OpenCode:
			capability = opencode.Capability()
		}
		report.ExpectedTarget.AdapterRevision = capability["adapter_revision"]
		report.ExpectedTarget.CapabilityRevision = capability["capability_revision"]
		if path, err := lookPathForTarget(*options.Target); err == nil {
			identity, err := environment.ExecutableIdentity(path)
			if err == nil {
				report.ExpectedTarget.ExecutableIdentity = identity
			}
		}
	}
	return nil
}

// Select configuration explicitly: a home workspace also contains global runtime state.
func workspaceConfigInputs(workspace, home string) []string {
	inputs := []string{filepath.Join(workspace, ".agents"), filepath.Join(workspace, "AGENTS.md"), filepath.Join(workspace, "CLAUDE.md"), filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".grok", "skills")}
	for _, directory := range []string{".claude", ".cursor"} {
		for _, name := range []string{"settings.json", "settings.local.json", "cli-config.json", "mcp.json", ".mcp.json", "CLAUDE.md", "AGENTS.md", "skills", "commands", "agents", "rules", "hooks", "hooks.json", "plugins/marketplace.json"} {
			inputs = append(inputs, filepath.Join(workspace, directory, filepath.FromSlash(name)))
		}
	}
	return inputs
}

// Fingerprints include absence and file bytes; absolute paths stay local to this identity.
func fingerprintInputs(inputs []string) (string, error) {
	hash := sha256.New()
	active := map[string]bool{}
	var visit func(string, string) error
	visit = func(path, relative string) error {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(hash, "%q absent\n", relative)
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%q mode=%s size=%d\n", relative, info.Mode(), info.Size())
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(hash, "link=%q\n", link)
			resolved, err := filepath.EvalSymlinks(path)
			if os.IsNotExist(err) {
				fmt.Fprintln(hash, "target absent")
				return nil
			}
			if err != nil {
				return err
			}
			return visit(resolved, relative+"/resolved")
		}
		if info.IsDir() {
			absolute, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			if active[absolute] {
				return fmt.Errorf("cyclic configuration symlink")
			}
			active[absolute] = true
			defer delete(active, absolute)
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err := visit(filepath.Join(path, entry.Name()), relative+"/"+entry.Name()); err != nil {
					return err
				}
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("nonregular configuration input %s", relative)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(hash, file)
		hash.Write([]byte{0})
		return err
	}
	for index, root := range inputs {
		if err := visit(root, fmt.Sprintf("input-%d", index)); err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func inheritedConfigPaths(target *base.Target, workspace string) []string {
	if target == nil {
		return nil
	}
	home, _ := os.UserHomeDir()
	var names []string
	switch *target {
	case base.Claude:
		names = []string{".claude.json", ".claude/settings.json"}
	case base.OpenCode:
		roots, err := opencode.NativeUserConfigRoots()
		if err != nil {
			return nil
		}
		var result []string
		seen := make(map[string]bool)
		for _, root := range roots {
			for _, name := range []string{"opencode.json", "opencode.jsonc"} {
				path := filepath.Join(root, name)
				if !seen[path] {
					result = append(result, path)
					seen[path] = true
				}
			}
		}
		if explicit := os.Getenv("OPENCODE_CONFIG"); explicit != "" {
			if !filepath.IsAbs(explicit) {
				explicit = filepath.Join(workspace, explicit)
			}
			if !seen[explicit] {
				result = append(result, explicit)
			}
		}
		return result
	case base.OMP:
		return omp.ConfigInputs(workspace)
	case base.Codex:
		names = []string{".codex/config.toml", ".codex/AGENTS.md"}
	case base.Grok:
		names = []string{".grok/config.toml", ".grok/hooks", ".claude/settings.json"}
	case base.Cursor:
		names = []string{".cursor/cli-config.json", ".cursor/mcp.json", ".cursor/skills"}
	case base.Agy:
		names = []string{".gemini/settings.json", ".gemini/GEMINI.md"}
	}
	var result []string
	for _, name := range names {
		result = append(result, filepath.Join(home, filepath.FromSlash(name)))
	}
	return result
}

func appendDetectedInheritance(report *environment.Report, options PreflightOptions) {
	for _, path := range inheritedConfigPaths(options.Target, report.WorkspaceRoot) {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			report.Findings = append(report.Findings, environment.Finding{Code: "AMBIENT_SCAN_FAILED", Severity: environment.SeverityError, Source: path, Message: "cannot inspect inherited configuration", Evidence: environment.EvidenceUnknown})
			continue
		}
		if !info.IsDir() && info.Size() == 0 {
			continue
		}
		severity := environment.SeverityWarning
		if options.Policy == environment.PolicyCI {
			severity = environment.SeverityViolation
		}
		report.Findings = append(report.Findings, environment.Finding{Code: "INHERITED_CONFIG", Severity: severity, Source: path, Message: "native launch may read existing undeclared configuration", Evidence: environment.EvidenceGenerated, Remedy: "use a controlled account/configuration home for CI, or use policy local"})
	}
}
