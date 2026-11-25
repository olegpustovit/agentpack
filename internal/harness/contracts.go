package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/olegpustovit/agentpack/internal/artifacts"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/pelletier/go-toml/v2"
)

type StageContext struct {
	ProjectRoot    string
	WorkspaceRoot  string
	Mode           mode.Effective
	LaunchTarget   *Target
	StagedRoots    map[Target]string
	SeedConfig     []byte
	StrictExternal bool
}

type LaunchContext struct {
	ProjectRoot    string
	WorkspaceRoot  string
	Arguments      []string
	Mode           mode.Effective
	Yolo           bool
	StrictExternal bool
	// SessionID is the agentpack terminal session this launch runs in (a worker launch only), so
	// the agent's hooks can report its turns; empty for a plain terminal.
	SessionID string
	// AgentpackTools is set when the chat's agent has agentpack tools, which replace its own subagent tool.
	AgentpackTools bool
}

type MCPProjectedFile struct {
	Path        string
	Contents    []byte
	Scope       string
	ScopeDigest string
	MergeOrder  []string
}

func MCPJSONScopeDigest(data []byte, key string) (string, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return "", err
	}
	return digestMCPSection(root[key]), nil
}

func MCPTOMLScopeDigest(data []byte, key string) (string, error) {
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return "", err
	}
	section, err := json.Marshal(root[key])
	if err != nil {
		return "", err
	}
	return digestMCPSection(section), nil
}

func digestMCPSection(data []byte) string {
	if len(data) == 0 {
		data = []byte("null")
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

type MCPProjection struct {
	Files    []MCPProjectedFile
	Servers  []MCPProjectedServer
	Warnings []string
	Inputs   []MCPProjectedInput
}

// SettingsProjection describes a generated file or a section of a native config.
type SettingsProjection struct {
	RelativePath  string
	Contents      []byte
	Section       string
	SectionDigest string
	MergeOrder    []string
}

// MCPMergeOrder lists the pack sources after any native configuration.
func MCPMergeOrder(before ...string) []string {
	return append(before, "plugin definitions", "agentpack.toml", ".agents/mcp.json")
}

// NewMCPProjection records the staged file, its inputs and the source of each server.
func NewMCPProjection(entries mcp.Entries, file MCPProjectedFile, warnings, shadowed []string, inputs []MCPProjectedInput) (MCPProjection, error) {
	projection := MCPProjection{Warnings: warnings, Inputs: inputs}
	if err := projection.AddFile(file); err != nil {
		return MCPProjection{}, err
	}
	projection.Servers = make([]MCPProjectedServer, 0, len(entries))
	for _, name := range entries.Names() {
		entry := entries[name]
		server := MCPProjectedServer{Name: name, Source: string(entry.Source), Module: entry.Module, Winner: string(entry.Source)}
		if slices.Contains(shadowed, name) {
			server.Winner = "user"
		}
		projection.Servers = append(projection.Servers, server)
	}
	markMCPWarnings(projection.Servers, warnings)
	return projection, nil
}

// AddFile records the digest of the MCP section in a staged JSON or TOML file.
func (projection *MCPProjection) AddFile(file MCPProjectedFile) error {
	digest := MCPJSONScopeDigest
	if filepath.Ext(file.Path) == ".toml" {
		digest = MCPTOMLScopeDigest
	}
	value, err := digest(file.Contents, file.Scope)
	if err != nil {
		return err
	}
	file.ScopeDigest = value
	if file.MergeOrder == nil {
		file.MergeOrder = MCPMergeOrder()
	}
	projection.Files = append(projection.Files, file)
	return nil
}

type LSPProjection struct {
	Files    []MCPProjectedFile
	Warnings []string
	Omitted  []string
}

type MCPProjectedInput struct {
	Path   string
	Digest string
	Exists bool
}

type MCPProjectedServer struct {
	Name, Source, Module, Winner string
	Dropped                      []string
}

func markMCPWarnings(servers []MCPProjectedServer, warnings []string) {
	for index := range servers {
		prefix := "MCP server " + servers[index].Name + ": "
		for _, warning := range warnings {
			if !strings.HasPrefix(warning, prefix) {
				continue
			}
			if _, suffix, found := strings.Cut(warning, " has no setting for "); found {
				fields, _, _ := strings.Cut(suffix, ";")
				servers[index].Dropped = strings.Split(fields, ", ")
			}
		}
	}
}

func ReadMCPSeed(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

func MCPInput(path string, contents []byte) (MCPProjectedInput, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return MCPProjectedInput{Path: path}, nil
	}
	if err != nil {
		return MCPProjectedInput{}, err
	}
	if !info.Mode().IsRegular() {
		return MCPProjectedInput{}, fmt.Errorf("MCP seed is not a regular file: %s", path)
	}
	digest := sha256.Sum256(contents)
	return MCPProjectedInput{Path: path, Digest: hex.EncodeToString(digest[:]), Exists: true}, nil
}

func ReadMCPInput(path string) (MCPProjectedInput, error) {
	contents, err := ReadMCPSeed(path)
	if err != nil {
		return MCPProjectedInput{}, err
	}
	return MCPInput(path, contents)
}

func ValidateMCPProjectionInputs(projections []MCPProjection) error {
	for _, projection := range projections {
		for _, input := range projection.Inputs {
			data, err := os.ReadFile(input.Path)
			if os.IsNotExist(err) && !input.Exists {
				continue
			}
			if err != nil {
				return fmt.Errorf("MCP seed changed after planning: %s: %w", input.Path, err)
			}
			if !input.Exists {
				return fmt.Errorf("MCP seed appeared after planning: %s", input.Path)
			}
			digest := sha256.Sum256(data)
			if hex.EncodeToString(digest[:]) != input.Digest {
				return fmt.Errorf("MCP seed changed after planning: %s", input.Path)
			}
		}
	}
	return nil
}

// RebuildTransaction keeps a newly staged root private until all staging and
// verification succeeds. Abort must leave the previously published root intact.
type RebuildTransaction interface {
	Root() string
	Commit() error
	Abort() error
}

// Harness is one coding agent: how agentpack stages, launches and runs it. A nil function means
// the agent has no such step; the methods below supply those defaults.
type Harness struct {
	Target              Target
	Artifacts           *artifacts.Layout
	DotAgents           *DotAgents
	ProjectSkillRoot    string                     // workspace-relative skills the harness reads directly
	ProjectSkillsShadow bool                       // native project skills take precedence over staged skills
	UserRoots           func(home string) []string // user artifact roots available outside the staged bundle
	Root                func(StageContext) (string, error)
	Reset               func(StageContext) ([]string, error)
	BeforeReset         func(StageContext) error
	Begin               func(StageContext) (RebuildTransaction, error)
	SeededHooks         string // home-relative native hook file staged for this harness
	PlannedSettings     func(mcp.Entries) (SettingsProjection, error)
	StagedConfigFiles   []string // extra staged files that preflight fingerprints
	Setup               func(StageContext) error
	ProjectMCP          func(mcp.Entries, StageContext) (MCPProjection, error)
	ProjectLSP          func(map[string]json.RawMessage, StageContext) (LSPProjection, error)
	Guidance            func(string, StageContext) error
	AfterStage          func(mcp.Entries, StageContext) error
	WorkspaceOverlay    func(StageContext) error
	Check               func(StageContext) error
	// Launch builds the agent command. The caller runs finish once the command has exited or
	// failed to start, on every path; it is never nil when err is nil.
	Launch            func(LaunchContext) (cmd *exec.Cmd, finish func() error, err error)
	ACPAdapter        string
	ACPStageAtSession bool
	LaunchACP         func(LaunchContext) (ACPLaunch, error)
	// Capability names the adapter and capability revisions a probe receipt must match; nil
	// when the agent cannot be probed.
	Capability  func() map[string]string
	Inheritance Inheritance
	// LaunchInputs digests the native configuration that changes what a rebuild
	// stages for this agent, so a change ends the launch fast path; nil when
	// staging reads none. Unlike Inheritance.Paths it leaves out files the
	// agent rewrites on every run.
	LaunchInputs func(projectRoot, workspace string) (string, error)
	// ResumeArgs are the arguments that relaunch the agent on its own session agentSessionID, for
	// a terminal restart; nil when the agent cannot resume one by ID.
	ResumeArgs func(agentSessionID string) []string
}

// DotAgents describes the workspace artifact sources this harness stages.
type DotAgents struct {
	NativeDirectory string
	RawTrees        []string
	ConvertedTrees  []string // skill files in these trees remain native discoveries
	FlattenRules    bool
	Guidance        string
}

// Inheritance is what a native launch of the agent reads or writes outside agentpack's staging,
// as preflight reports it.
type Inheritance struct {
	// Note says how the launch relates to the user's native configuration.
	Note string
	// Paths lists the user configuration files a native launch may read.
	Paths func(workspace string) []string
	// Seed is the user configuration file seeded into staging and the label preflight shows for it.
	Seed func() (path, label string)
	// Overlay lists the workspace-relative paths the launcher writes in the checkout.
	Overlay []string
}

// HomeFile is a file under the user's home directory and the ~/ label preflight shows for it.
func HomeFile(name string) (string, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	return filepath.Join(home, filepath.FromSlash(name)), "~/" + name
}

// HomeFiles lists files under the user's home directory.
func HomeFiles(names ...string) []string {
	home, _ := os.UserHomeDir()
	result := make([]string, len(names))
	for index, name := range names {
		result[index] = filepath.Join(home, filepath.FromSlash(name))
	}
	return result
}

// Done is the finish function of a launch with nothing to do after the agent exits.
func Done() error { return nil }

func (harness Harness) NativeUserRoots(home string) []string {
	if home == "" || harness.UserRoots == nil {
		return nil
	}
	return harness.UserRoots(home)
}
func (harness Harness) ResetPaths(ctx StageContext) ([]string, error) {
	if harness.Reset != nil {
		return harness.Reset(ctx)
	}
	root, err := harness.Root(ctx)
	if err != nil {
		return nil, err
	}
	return []string{root}, nil
}
func (harness Harness) PreReset(ctx StageContext) error {
	if harness.BeforeReset != nil {
		return harness.BeforeReset(ctx)
	}
	return nil
}
func (harness Harness) BeginRebuild(ctx StageContext) (RebuildTransaction, error) {
	if harness.Begin != nil {
		return harness.Begin(ctx)
	}
	return nil, nil
}
func WriteMCPProjection(projection MCPProjection) error {
	for _, file := range projection.Files {
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file.Path, file.Contents, 0o644); err != nil {
			return err
		}
	}
	for _, warning := range projection.Warnings {
		log.Printf("warning: %s", warning)
	}
	return nil
}

func (harness Harness) PlanMCP(entries mcp.Entries, ctx StageContext) (MCPProjection, error) {
	if harness.ProjectMCP == nil {
		return MCPProjection{}, nil
	}
	return harness.ProjectMCP(entries, ctx)
}

func (harness Harness) PlanLSP(servers map[string]json.RawMessage, ctx StageContext) (LSPProjection, error) {
	if harness.ProjectLSP == nil {
		return LSPProjection{}, ErrNoLSP
	}
	return harness.ProjectLSP(servers, ctx)
}

// ErrNoLSP says an agent has no setting through which a language server can
// be configured for it.
var ErrNoLSP = errors.New("no language server setting")

func (harness Harness) InjectGuidance(blob string, ctx StageContext) error {
	if harness.Guidance != nil {
		return harness.Guidance(blob, ctx)
	}
	return nil
}
func (harness Harness) Finalize(entries mcp.Entries, ctx StageContext) error {
	if harness.AfterStage != nil {
		return harness.AfterStage(entries, ctx)
	}
	return nil
}
func (harness Harness) FinalizeWorkspaceOverlay(ctx StageContext) error {
	if harness.WorkspaceOverlay != nil {
		return harness.WorkspaceOverlay(ctx)
	}
	return nil
}

// PrepareACP installs and verifies a pinned adapter without reading project staging.
func (harness Harness) PrepareACP(ctx context.Context) error {
	if harness.ACPAdapter == "" {
		return nil
	}
	_, err := EnsureACPAdapterExecutable(ctx, harness.ACPAdapter)
	return err
}
