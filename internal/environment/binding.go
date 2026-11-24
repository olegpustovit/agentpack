package environment

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/pelletier/go-toml/v2"
)

const bindingFileName = "environment.toml"

// Binding associates a workspace checkout with an external definition root.
type Binding struct {
	Environment    string `toml:"environment"`
	DefinitionRoot string `toml:"definition_root"`
	WorkspaceRoot  string `toml:"workspace_root"`
}

func bindingPath(workspaceRoot string) (string, error) {
	return paths.ProjectStateFile(workspaceRoot, bindingFileName)
}

// ProjectRequiresUserAccess checks what the gateway reads for a project's metadata.
// The pack can live in an ancestor or in a separate definition bound to the workspace.
func ProjectRequiresUserAccess(workspace, definition string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if paths.RequiresUserAccess(workspace) || paths.RequiresUserAccess(filepath.Join(paths.ProjectDotAgentsDir(workspace), "mcp.json")) {
		return true
	}
	if definition == "" {
		if file, err := bindingPath(workspace); err == nil && paths.RequiresUserAccess(file) {
			return true
		}
		if binding, found, _ := LoadBinding(workspace); found {
			definition = binding.DefinitionRoot
		}
	}
	if definition != "" {
		return paths.RequiresUserAccess(paths.ManifestPath(definition)) || paths.RequiresUserAccess(paths.LockPath(definition))
	}
	for dir := workspace; ; dir = filepath.Dir(dir) {
		manifest, lock := paths.ManifestPath(dir), paths.LockPath(dir)
		if paths.RequiresUserAccess(manifest) || paths.RequiresUserAccess(lock) {
			return true
		}
		if paths.IsRegularFile(manifest) || paths.IsRegularFile(lock) || dir == filepath.Dir(dir) {
			return false
		}
	}
}

// LoadBinding returns the binding for a workspace, if any.
func LoadBinding(workspaceRoot string) (Binding, bool, error) {
	path, err := bindingPath(workspaceRoot)
	if err != nil {
		return Binding{}, false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Binding{}, false, nil
	}
	if err != nil {
		return Binding{}, false, err
	}
	var binding Binding
	if err := toml.Unmarshal(data, &binding); err != nil {
		return Binding{}, false, fmt.Errorf("parse environment binding: %w", err)
	}
	binding.DefinitionRoot = strings.TrimSpace(binding.DefinitionRoot)
	binding.Environment = strings.TrimSpace(binding.Environment)
	binding.WorkspaceRoot = strings.TrimSpace(binding.WorkspaceRoot)
	if binding.DefinitionRoot == "" {
		return Binding{}, false, nil
	}
	return binding, true, nil
}

// SaveBinding writes the workspace → definition association.
func SaveBinding(binding Binding) error {
	if binding.WorkspaceRoot == "" || binding.DefinitionRoot == "" {
		return fmt.Errorf("environment binding requires workspace_root and definition_root")
	}
	workspace, err := filepath.Abs(binding.WorkspaceRoot)
	if err != nil {
		return err
	}
	definition, err := filepath.Abs(binding.DefinitionRoot)
	if err != nil {
		return err
	}
	binding.WorkspaceRoot = workspace
	binding.DefinitionRoot = definition
	path, err := bindingPath(workspace)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := toml.Marshal(binding)
	if err != nil {
		return err
	}
	return paths.WriteAtomic(path, data, 0o644)
}

// ClearBinding removes the association for a workspace.
func ClearBinding(workspaceRoot string) error {
	path, err := bindingPath(workspaceRoot)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
