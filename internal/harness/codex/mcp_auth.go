package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/gofrs/flock"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/pelletier/go-toml/v2"
)

const credentialsFile = ".credentials.json"
const credentialsBaselineFile = ".agentpack-mcp-credentials-baseline.json"
const oauthLocksDirectory = "mcp-oauth-locks"
const oauthFileStoreLock = "file-store.lock"

func oauthRoot(projectRoot string) (string, error) {
	state, err := paths.ProjectStateDir(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "codex-mcp-oauth"), nil
}
func oauthCredentials(projectRoot string) (string, error) {
	root, err := oauthRoot(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, credentialsFile), nil
}
func oauthLocks(projectRoot string) (string, error) {
	root, err := oauthRoot(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, oauthLocksDirectory), nil
}

func withMCPOAuthLock(projectRoot string, run func() error) (runErr error) {
	locks, err := oauthLocks(projectRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(locks, oauthFileStoreLock))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	acquired, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock Codex MCP credential store: %w", err)
	}
	if !acquired {
		return fmt.Errorf("timed out locking Codex MCP credential store")
	}
	defer func() { runErr = errors.Join(runErr, lock.Unlock()) }()
	return run()
}

func prepareMCPAuth(projectRoot, staged string) error {
	credentials, err := oauthCredentials(projectRoot)
	if err != nil {
		return err
	}
	if err := withMCPOAuthLock(projectRoot, func() error {
		if _, err := os.Stat(credentials); os.IsNotExist(err) {
			if err := writeCredentialStore(credentials, map[string]any{}); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if _, err := readCredentialStore(credentials); err != nil {
			return err
		}
		data, err := os.ReadFile(credentials)
		if err != nil {
			return err
		}
		stagedFile := filepath.Join(staged, credentialsFile)
		if err := os.Remove(stagedFile); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(staged, 0o755); err != nil {
			return err
		}
		if err := os.Link(credentials, stagedFile); err != nil {
			// Codex refuses symlinks for this file. A regular copy is required
			// when staging and project state are on different filesystems.
			if err := atomicWriteAuth(stagedFile, data); err != nil {
				return fmt.Errorf("copy Codex MCP credentials into staging: %w", err)
			}
		}
		return atomicWriteAuth(filepath.Join(staged, credentialsBaselineFile), data)
	}); err != nil {
		return err
	}
	locks, err := oauthLocks(projectRoot)
	if err != nil {
		return err
	}
	if err := base.LinkDurableDirectory(locks, filepath.Join(staged, oauthLocksDirectory)); err != nil {
		return err
	}
	return updateConfig(filepath.Join(staged, "config.toml"), func(root map[string]any) { root["mcp_oauth_credentials_store"] = "file" })
}
func verifyMCPAuth(projectRoot, staged string) error {
	credentials, err := oauthCredentials(projectRoot)
	if err != nil {
		return err
	}
	stagedFile := filepath.Join(staged, credentialsFile)
	info, err := os.Lstat(stagedFile)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("codex MCP credential file is missing or not regular: %s", stagedFile)
	}
	if !base.DurablePathMatches(stagedFile, credentials) {
		baseline, err := os.ReadFile(filepath.Join(staged, credentialsBaselineFile))
		if err != nil {
			return err
		}
		current, err := os.ReadFile(stagedFile)
		if err != nil {
			return err
		}
		durable, err := os.ReadFile(credentials)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, durable) || !bytes.Equal(baseline, durable) {
			return fmt.Errorf("codex MCP credential copy needs reconciliation")
		}
	}
	locks, err := oauthLocks(projectRoot)
	if err != nil {
		return err
	}
	if !base.DurablePathMatches(filepath.Join(staged, oauthLocksDirectory), locks) {
		return fmt.Errorf("codex MCP OAuth lock directory does not resolve to %s", locks)
	}
	root := make(map[string]any)
	data, err := os.ReadFile(filepath.Join(staged, "config.toml"))
	if err != nil {
		return err
	}
	if err := jsonOrToml(data, &root); err != nil {
		return err
	}
	if root["mcp_oauth_credentials_store"] != "file" {
		return fmt.Errorf("codex staged config is not using the durable MCP OAuth file store")
	}
	return nil
}
func jsonOrToml(data []byte, target *map[string]any) error { return toml.Unmarshal(data, target) }

func reconcileMCPAuthHome(projectRoot, staged string) error {
	return withMCPOAuthLock(projectRoot, func() error {
		durable, err := oauthCredentials(projectRoot)
		if err != nil {
			return err
		}
		return reconcileMCPAuthModeLocked(staged, durable)
	})
}

func reconcileMCPAuthModeLocked(staged, durable string) error {
	baselinePath := filepath.Join(staged, credentialsBaselineFile)
	if info, err := os.Lstat(baselinePath); os.IsNotExist(err) {
		return nil // nothing was handed to this home
	} else if err != nil {
		return err
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("Codex MCP credential baseline is not a regular file: %s", baselinePath)
	}
	baseline, err := readCredentialStore(baselinePath)
	if err != nil {
		return err
	}
	stagedFile := filepath.Join(staged, credentialsFile)
	stagedInfo, err := os.Lstat(stagedFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && !stagedInfo.Mode().IsRegular() {
		return fmt.Errorf("Codex MCP credential file is not a regular file: %s", stagedFile)
	}
	current := make(map[string]any)
	if err == nil {
		current, err = readCredentialStore(stagedFile)
		if err != nil {
			return err
		}
	}
	latest := make(map[string]any)
	if _, err := os.Stat(durable); err == nil {
		latest, err = readCredentialStore(durable)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err == nil && base.DurablePathMatches(stagedFile, durable) {
		data, err := os.ReadFile(durable)
		if err != nil {
			return err
		}
		return atomicWriteAuth(baselinePath, data)
	}
	changed := false
	for key, before := range baseline {
		after, present := current[key]
		if present && reflect.DeepEqual(before, after) {
			continue
		}
		existing, exists := latest[key]
		if !exists || !reflect.DeepEqual(existing, before) {
			if present && exists && reflect.DeepEqual(existing, after) {
				continue
			}
			return fmt.Errorf("Codex MCP credential %q changed concurrently; staged copy preserved for recovery", key)
		}
		if present {
			latest[key] = after
		} else {
			delete(latest, key)
		}
		changed = true
	}
	for key, after := range current {
		if _, existed := baseline[key]; existed {
			continue
		}
		if existing, exists := latest[key]; exists && !reflect.DeepEqual(existing, after) {
			return fmt.Errorf("Codex MCP credential %q was added concurrently; staged copy preserved for recovery", key)
		}
		latest[key] = after
		changed = true
	}
	if changed {
		if err := writeCredentialStore(durable, latest); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(durable)
	if err != nil {
		return err
	}
	if err := atomicWriteAuth(stagedFile, data); err != nil {
		return err
	}
	return atomicWriteAuth(baselinePath, data)
}

func reconcileAllMCPAuth(projectRoot, currentMode string) error {
	return withMCPOAuthLock(projectRoot, func() error {
		return reconcileAllMCPAuthLocked(projectRoot, currentMode)
	})
}

func reconcileAllMCPAuthLocked(projectRoot, currentMode string) error {
	modeRoot, err := paths.StagingRootForMode(projectRoot, currentMode)
	if err != nil {
		return err
	}
	modes := filepath.Dir(modeRoot)
	durable, err := oauthCredentials(projectRoot)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(modes)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		staged := filepath.Join(modes, entry.Name(), "codex-home")
		if err := reconcileMCPAuthModeLocked(staged, durable); err != nil {
			return fmt.Errorf("reconcile Codex MCP credentials for mode %s: %w", entry.Name(), err)
		}
	}
	generations, err := publishedGenerationHomes(projectRoot)
	if err != nil {
		return err
	}
	for _, staged := range generations {
		if err := reconcileMCPAuthModeLocked(staged, durable); err != nil {
			return fmt.Errorf("reconcile Codex MCP credentials for generation %s: %w", staged, err)
		}
	}
	return nil
}
func readCredentialStore(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	store := make(map[string]any)
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("invalid Codex MCP OAuth credential store %s: %w", path, err)
	}
	return store, nil
}
func writeCredentialStore(path string, store map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(store)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
