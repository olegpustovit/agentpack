package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/olegpustovit/agentpack/internal/harness/codex"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/zalando/go-keyring"
)

const codexEndpoint = "https://chatgpt.com/backend-api/codex/responses"
const openAIEndpoint = "https://api.openai.com/v1/responses"

type AuthSnapshot struct{ AccessToken, AccountID, EndpointURL string }
type AuthManager interface {
	Snapshot() (AuthSnapshot, error)
	RefreshAfterUnauthorized() (bool, error)
}
type authSource uint8

const (
	apiKeySource authSource = iota
	chatGPTSource
)

type authState struct {
	Token, URL, AccountID, AuthFile, RefreshToken string
	Source                                        authSource
}
type UpstreamAuth struct {
	mu    sync.Mutex
	state authState
}

func LoadUpstreamAuth() (*UpstreamAuth, error) {
	state, err := loadAuthState()
	if err != nil {
		return nil, err
	}
	if override := strings.TrimSpace(os.Getenv("AGENTPACK_PROXY_UPSTREAM_URL")); override != "" {
		state.URL = override
	}
	return &UpstreamAuth{state: state}, nil
}
func (auth *UpstreamAuth) Snapshot() (AuthSnapshot, error) {
	auth.mu.Lock()
	defer auth.mu.Unlock()
	return AuthSnapshot{AccessToken: auth.state.Token, AccountID: auth.state.AccountID, EndpointURL: auth.state.URL}, nil
}
func (auth *UpstreamAuth) DefaultTransport() Transport {
	auth.mu.Lock()
	defer auth.mu.Unlock()
	if auth.state.Source == apiKeySource {
		return TransportHTTP
	}
	return TransportWebSocket
}
func (auth *UpstreamAuth) RefreshAfterUnauthorized() (bool, error) {
	auth.mu.Lock()
	defer auth.mu.Unlock()
	if auth.state.Source != chatGPTSource || auth.state.RefreshToken == "" || auth.state.AuthFile == "" {
		return false, nil
	}
	data, err := os.ReadFile(auth.state.AuthFile)
	if err != nil {
		return false, err
	}
	if data, err = codex.RefreshAuth(context.Background(), data); err != nil {
		return false, fmt.Errorf("refresh Codex OAuth token: %w", err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return false, err
	}
	tokens := object(value["tokens"])
	auth.state.Token, auth.state.RefreshToken = stringValue(tokens["access_token"]), stringValue(tokens["refresh_token"])
	return true, writeAtomicJSON(auth.state.AuthFile, value)
}

func loadAuthState() (authState, error) {
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		return authState{Token: key, URL: openAIEndpoint, Source: apiKeySource}, nil
	}
	path, err := resolveAuthJSON()
	if err != nil {
		return authState{}, fmt.Errorf("OPENAI_API_KEY is unset and no Codex auth.json is available for proxy auth: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return authState{}, err
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return authState{}, err
	}
	if token := stringValue(value["personal_access_token"]); strings.TrimSpace(token) != "" {
		return authState{Token: token, URL: codexEndpoint, AuthFile: path, Source: chatGPTSource}, nil
	}
	tokens := object(value["tokens"])
	if token := stringValue(tokens["access_token"]); strings.TrimSpace(token) != "" {
		return authState{Token: token, URL: codexEndpoint, AccountID: stringValue(tokens["account_id"]), AuthFile: path, RefreshToken: stringValue(tokens["refresh_token"]), Source: chatGPTSource}, nil
	}
	if key := stringValue(value["OPENAI_API_KEY"]); strings.TrimSpace(key) != "" {
		return authState{Token: key, URL: openAIEndpoint, AuthFile: path, Source: apiKeySource}, nil
	}
	return authState{}, fmt.Errorf("codex auth file did not contain an API key, ChatGPT access token, or personal access token")
}
func resolveAuthJSON() (string, error) {
	if path := os.Getenv("AGENTPACK_PROXY_AUTH_JSON"); regular(path) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		path := filepath.Join(home, ".codex", "auth.json")
		if regular(path) {
			return path, nil
		}
		account := codexKeyringAccount(filepath.Join(home, ".codex"))
		if raw, keyErr := keyring.Get("Codex Auth", account); keyErr == nil {
			var value any
			if json.Unmarshal([]byte(raw), &value) == nil {
				shared, _ := paths.SharedCodexAuthPath()
				if writeAtomicJSON(shared, value) == nil {
					return shared, nil
				}
			}
		}
	}
	shared, err := paths.SharedCodexAuthPath()
	if err == nil && regular(shared) {
		return shared, nil
	}
	return "", os.ErrNotExist
}
func codexKeyringAccount(home string) string {
	canonical, err := filepath.EvalSymlinks(home)
	if err != nil {
		canonical = home
	}
	sum := sha256.Sum256([]byte(canonical))
	return "cli|" + hex.EncodeToString(sum[:])[:16]
}
func writeAtomicJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), fmt.Sprintf(".auth.json.tmp.%d", os.Getpid()))
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
func regular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
