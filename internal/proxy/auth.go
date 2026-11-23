package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/olegpustovit/agentpack/internal/harness/codex"
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
	Token, URL, AccountID string
	Source                authSource
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
	if auth.state.Source != chatGPTSource {
		return false, nil
	}
	path, err := resolveAuthJSON()
	if err != nil {
		return false, err
	}
	next, err := readAuthState(path)
	if err != nil {
		return false, err
	}
	if next.Source == chatGPTSource && next.Token != auth.state.Token {
		auth.state.Token, auth.state.AccountID = next.Token, next.AccountID
		return true, nil
	}
	return false, fmt.Errorf("Codex credentials were rejected and the local gateway has no Codex refresh endpoint; sign in again with agentpack codex login")
}

func loadAuthState() (authState, error) {
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		return authState{Token: key, URL: openAIEndpoint, Source: apiKeySource}, nil
	}
	path, err := resolveAuthJSON()
	if err != nil {
		return authState{}, fmt.Errorf("OPENAI_API_KEY is unset and no Codex auth.json is available for proxy auth: %w", err)
	}
	return readAuthState(path)
}

func readAuthState(path string) (authState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return authState{}, err
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return authState{}, err
	}
	if token := stringValue(value["personal_access_token"]); strings.TrimSpace(token) != "" {
		return authState{Token: token, URL: codexEndpoint, Source: chatGPTSource}, nil
	}
	tokens := object(value["tokens"])
	if token := stringValue(tokens["access_token"]); strings.TrimSpace(token) != "" {
		return authState{Token: token, URL: codexEndpoint, AccountID: stringValue(tokens["account_id"]), Source: chatGPTSource}, nil
	}
	if key := stringValue(value["OPENAI_API_KEY"]); strings.TrimSpace(key) != "" {
		return authState{Token: key, URL: openAIEndpoint, Source: apiKeySource}, nil
	}
	return authState{}, fmt.Errorf("codex auth file did not contain an API key, ChatGPT access token, or personal access token")
}
func resolveAuthJSON() (string, error) {
	if path := os.Getenv("AGENTPACK_PROXY_AUTH_JSON"); regular(path) {
		return path, nil
	}
	return codex.SharedAuthFile()
}

func regular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
