package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/paths"
)

const npmRegistry = "https://registry.npmjs.org"

// EnsureACPAdapterExecutable installs a pinned npm ACP adapter under
// $AGENTPACK_HOME/adapters/<name>@<version>/ and returns its launcher path.
func EnsureACPAdapterExecutable(ctx context.Context, packageSpec string) (string, error) {
	return ensureACPAdapterExecutable(ctx, packageSpec, npmRegistry)
}

func ensureACPAdapterExecutable(ctx context.Context, packageSpec, registry string) (string, error) {
	name, version, err := splitNPMPackageSpec(packageSpec)
	if err != nil {
		return "", err
	}
	if _, err := exec.LookPath("node"); err != nil {
		return "", fmt.Errorf("Node.js is required for ACP chat (%s); use Terminal view or install Node", name)
	}
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return "", err
	}
	cacheRoot := filepath.Join(home, "adapters", adapterSlot(name, version))
	specPath := filepath.Join(cacheRoot, "adapter.json")
	if spec, found, err := readACPAdapterSpec(specPath, name, version, registry); err != nil {
		return "", err
	} else if found {
		return mcp.InstallTree(ctx, http.DefaultClient, cacheRoot, spec)
	}
	resolved, integrity, err := mcp.ResolveNPM(ctx, http.DefaultClient, registry, name, version)
	if err != nil {
		return "", err
	}
	tree, bin, err := mcp.ResolveTree(ctx, registry, name, resolved, integrity)
	if err != nil {
		return "", err
	}
	spec := mcp.TreeSpec{
		Server:     name + "@" + resolved,
		Package:    name,
		Version:    resolved,
		Registry:   registry,
		Bin:        bin,
		TreeDigest: mcp.TreeDigest(tree),
		Tree:       tree,
	}
	executable, err := mcp.InstallTree(ctx, http.DefaultClient, cacheRoot, spec)
	if err != nil {
		return "", err
	}
	if err := writeACPAdapterSpec(specPath, spec); err != nil {
		return "", err
	}
	return executable, nil
}

func readACPAdapterSpec(path, name, version, registry string) (mcp.TreeSpec, bool, error) {
	var spec mcp.TreeSpec
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return spec, false, nil
	}
	if err != nil {
		return spec, false, err
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return spec, false, fmt.Errorf("read cached ACP adapter spec %s: %w", path, err)
	}
	if spec.Server != name+"@"+version || spec.Package != name || spec.Version != version || spec.Registry != registry || spec.Bin == "" || spec.TreeDigest != mcp.TreeDigest(spec.Tree) {
		return spec, false, fmt.Errorf("cached ACP adapter spec %s does not match %s@%s", path, name, version)
	}
	return spec, true, nil
}

func writeACPAdapterSpec(path string, spec mcp.TreeSpec) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".adapter-spec-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		if existing, found, readErr := readACPAdapterSpec(path, spec.Package, spec.Version, spec.Registry); readErr == nil && found && existing.TreeDigest == spec.TreeDigest {
			return nil
		}
		return err
	}
	return nil
}

func splitNPMPackageSpec(spec string) (name, version string, err error) {
	spec = strings.TrimSpace(spec)
	at := strings.LastIndex(spec, "@")
	if at <= 0 || at >= len(spec)-1 {
		return "", "", fmt.Errorf("invalid npm package spec %q", spec)
	}
	return spec[:at], spec[at+1:], nil
}

func adapterSlot(name, version string) string {
	return strings.NewReplacer("/", "_", "@", "_").Replace(name) + "@" + version
}
