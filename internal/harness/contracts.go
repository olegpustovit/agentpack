package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	Command        *exec.Cmd
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

func ProjectedMCPServers(entries mcp.Entries) []MCPProjectedServer {
	servers := make([]MCPProjectedServer, 0, len(entries))
	for _, name := range entries.Names() {
		entry := entries[name]
		servers = append(servers, MCPProjectedServer{Name: name, Source: string(entry.Source), Module: entry.Module, Winner: string(entry.Source)})
	}
	return servers
}

func MarkMCPWarnings(servers []MCPProjectedServer, warnings []string) {
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

type Harness interface {
	ID() Target
	// NativeUserRoots returns user artifact roots available outside the staged bundle.
	NativeUserRoots(home string) []string
	StagedRoot(StageContext) (string, error)
	ResetPaths(StageContext) ([]string, error)
	PreReset(StageContext) error
	BeginRebuild(StageContext) (RebuildTransaction, error)
	Prepare(StageContext) error
	WriteMCP(mcp.Entries, StageContext) error
	PlanMCP(mcp.Entries, StageContext) (MCPProjection, error)
	PlanLSP(map[string]json.RawMessage, StageContext) (LSPProjection, error)
	// WriteLSP writes language servers (Claude Code format, by name); it
	// returns ErrNoLSP when the agent has no language server setting.
	WriteLSP(map[string]json.RawMessage, StageContext) error
	InjectGuidance(string, StageContext) error
	Finalize(mcp.Entries, StageContext) error
	FinalizeWorkspaceOverlay(StageContext) error
	Verify(StageContext) error
	LaunchCommand(LaunchContext) (*exec.Cmd, error)
	AfterLaunch(LaunchContext) error
}

type Definition struct {
	Target           Target
	UserRoots        func(home string) []string
	Root             func(StageContext) (string, error)
	Reset            func(StageContext) ([]string, error)
	BeforeReset      func(StageContext) error
	Begin            func(StageContext) (RebuildTransaction, error)
	Setup            func(StageContext) error
	MCP              func(mcp.Entries, StageContext) error
	ProjectMCP       func(mcp.Entries, StageContext) (MCPProjection, error)
	ProjectLSP       func(map[string]json.RawMessage, StageContext) (LSPProjection, error)
	LSP              func(map[string]json.RawMessage, StageContext) error
	Guidance         func(string, StageContext) error
	AfterStage       func(mcp.Entries, StageContext) error
	WorkspaceOverlay func(StageContext) error
	Check            func(StageContext) error
	Launch           func(LaunchContext) (*exec.Cmd, error)
	LaunchFinished   func(LaunchContext) error
}

func (definition Definition) ID() Target { return definition.Target }
func (definition Definition) NativeUserRoots(home string) []string {
	if home == "" || definition.UserRoots == nil {
		return nil
	}
	return definition.UserRoots(home)
}
func (definition Definition) StagedRoot(ctx StageContext) (string, error) {
	return definition.Root(ctx)
}
func (definition Definition) ResetPaths(ctx StageContext) ([]string, error) {
	if definition.Reset != nil {
		return definition.Reset(ctx)
	}
	root, err := definition.Root(ctx)
	if err != nil {
		return nil, err
	}
	return []string{root}, nil
}
func (definition Definition) PreReset(ctx StageContext) error {
	if definition.BeforeReset != nil {
		return definition.BeforeReset(ctx)
	}
	return nil
}
func (definition Definition) BeginRebuild(ctx StageContext) (RebuildTransaction, error) {
	if definition.Begin != nil {
		return definition.Begin(ctx)
	}
	return nil, nil
}
func (definition Definition) Prepare(ctx StageContext) error {
	if definition.Setup == nil {
		return fmt.Errorf("%s harness has no prepare function", definition.Target)
	}
	return definition.Setup(ctx)
}
func (definition Definition) WriteMCP(entries mcp.Entries, ctx StageContext) error {
	if definition.ProjectMCP != nil {
		projection, err := definition.ProjectMCP(entries, ctx)
		if err != nil {
			return err
		}
		return WriteMCPProjection(projection)
	}
	if definition.MCP != nil {
		return definition.MCP(entries, ctx)
	}
	return nil
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

func (definition Definition) PlanMCP(entries mcp.Entries, ctx StageContext) (MCPProjection, error) {
	if definition.ProjectMCP == nil {
		return MCPProjection{}, nil
	}
	return definition.ProjectMCP(entries, ctx)
}

func (definition Definition) PlanLSP(servers map[string]json.RawMessage, ctx StageContext) (LSPProjection, error) {
	if definition.ProjectLSP == nil {
		return LSPProjection{}, ErrNoLSP
	}
	return definition.ProjectLSP(servers, ctx)
}

// ErrNoLSP says an agent has no setting through which a language server can
// be configured for it.
var ErrNoLSP = errors.New("no language server setting")

func (definition Definition) WriteLSP(servers map[string]json.RawMessage, ctx StageContext) error {
	if definition.LSP == nil {
		return ErrNoLSP
	}
	return definition.LSP(servers, ctx)
}
func (definition Definition) InjectGuidance(blob string, ctx StageContext) error {
	if definition.Guidance != nil {
		return definition.Guidance(blob, ctx)
	}
	return nil
}
func (definition Definition) Finalize(entries mcp.Entries, ctx StageContext) error {
	if definition.AfterStage != nil {
		return definition.AfterStage(entries, ctx)
	}
	return nil
}
func (definition Definition) FinalizeWorkspaceOverlay(ctx StageContext) error {
	if definition.WorkspaceOverlay != nil {
		return definition.WorkspaceOverlay(ctx)
	}
	return nil
}
func (definition Definition) Verify(ctx StageContext) error {
	if definition.Check == nil {
		return fmt.Errorf("%s harness has no verifier", definition.Target)
	}
	return definition.Check(ctx)
}
func (definition Definition) LaunchCommand(ctx LaunchContext) (*exec.Cmd, error) {
	if definition.Launch == nil {
		return nil, fmt.Errorf("%s harness has no launcher", definition.Target)
	}
	return definition.Launch(ctx)
}
func (definition Definition) AfterLaunch(ctx LaunchContext) error {
	if definition.LaunchFinished != nil {
		return definition.LaunchFinished(ctx)
	}
	return nil
}
