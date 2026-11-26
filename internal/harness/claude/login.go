package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// LoginMethods are the ways the web UI signs Claude in.
var LoginMethods = []base.LoginMethod{{ID: "claudeai", Label: "Claude account"}, {ID: "console", Label: "Anthropic Console"}}

// Account reads `claude auth status --json`; it never reads the keychain itself.
func Account(ctx context.Context) base.Account {
	account, exe := base.ProbeAccount(base.Claude)
	if !account.Installed {
		return account
	}
	result, err := base.RunProbe(ctx, base.ProbePlan{Executable: exe, Argv: []string{"auth", "status", "--json"}, Timeout: 15 * time.Second, Categories: []string{"auth"}, WriteScope: "none"})
	if err != nil {
		account.Error = err.Error()
		return account
	}
	var status struct {
		LoggedIn         bool   `json:"loggedIn"`
		Email            string `json:"email"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if json.Unmarshal([]byte(result.Stdout), &status) != nil {
		account.Error = "claude auth status printed no status"
		return account
	}
	account.SignedIn, account.Label = status.LoggedIn, status.Email
	return account
}

// Logout runs `claude auth logout`.
func Logout(ctx context.Context) error {
	exe, err := base.Claude.Resolve()
	if err != nil {
		return err
	}
	return base.RunLogout(ctx, exe, "auth", "logout")
}

// Login runs `claude auth login`. With BROWSER=true it opens nothing and prints the link; the page
// it leads to shows a code, which the person pastes back.
func Login(method string) (base.LoginFlow, error) {
	exe, err := base.Claude.Resolve()
	if err != nil {
		return base.LoginFlow{}, err
	}
	flag := "--claudeai"
	if method == "console" {
		flag = "--console"
	}
	command := exec.Command(exe, "auth", "login", flag)
	command.Env = append(os.Environ(), "BROWSER=true")
	return base.LoginFlow{Command: command, Input: "code"}, nil
}

// CredentialFile is Claude Code's sign-in file. On macOS Claude prefers the keychain and reads this
// file when the keychain holds none, so a sign-in copied from another machine works there too.
func CredentialFile() string {
	path, _ := base.HomeFile(".claude/.credentials.json")
	return path
}

// ValidCredential requires a Claude account token; an expired one stays valid because it can be
// renewed. A Console sign-in (checked in the 2.1.330 bundle) writes this same claudeAiOauth key only
// when its grant carries user:inference; otherwise it keeps its API key as primaryApiKey in
// ~/.claude.json (the keychain on macOS), which is not shared.
func ValidCredential(data []byte) bool { return credentialOAuth(data).AccessToken != "" }

// Freshness is when the access token expires, zero when the file does not say.
func Freshness(data []byte) time.Time {
	if expires := credentialOAuth(data).ExpiresAt; expires > 0 {
		return time.UnixMilli(int64(expires))
	}
	return time.Time{}
}

type oauthCredential struct {
	AccessToken string  `json:"accessToken"`
	ExpiresAt   float64 `json:"expiresAt"`
}

func credentialOAuth(data []byte) oauthCredential {
	var file struct {
		OAuth oauthCredential `json:"claudeAiOauth"`
	}
	if json.Unmarshal(data, &file) != nil {
		return oauthCredential{}
	}
	return file.OAuth
}

// Claude Code's own OAuth client (from its bundle); tests point tokenURL at a fake.
var tokenURL = "https://platform.claude.com/v1/oauth/token"

const oauthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

// Claude Code renews five minutes before expiry. Renewing an hour ahead lets one machine renew for
// all of them, so no two machines ever spend the same rotating refresh token.
const refreshAhead = time.Hour

// RefreshCredential renews the sign-in in CredentialFile's contents when it is due, and returns
// nil when it is not (or the file holds no Claude account sign-in).
func RefreshCredential(ctx context.Context, data []byte) ([]byte, error) {
	var file map[string]any
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	oauth, _ := file["claudeAiOauth"].(map[string]any)
	refresh, _ := oauth["refreshToken"].(string)
	expires, _ := oauth["expiresAt"].(float64)
	if refresh == "" || time.Until(time.UnixMilli(int64(expires))) > refreshAhead {
		return nil, nil
	}
	var scopes []string
	if list, ok := oauth["scopes"].([]any); ok {
		for _, scope := range list {
			if text, ok := scope.(string); ok {
				scopes = append(scopes, text)
			}
		}
	}
	var reply struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := base.RefreshToken(ctx, tokenURL, map[string]string{"refresh_token": refresh, "client_id": oauthClientID, "scope": strings.Join(scopes, " ")}, &reply); err != nil {
		return nil, err
	}
	if reply.AccessToken == "" || reply.ExpiresIn <= 0 {
		return nil, errors.New("token refresh answered without a token")
	}
	oauth["accessToken"], oauth["expiresAt"] = reply.AccessToken, time.Now().Add(time.Duration(reply.ExpiresIn)*time.Second).UnixMilli()
	if reply.RefreshToken != "" {
		oauth["refreshToken"] = reply.RefreshToken
	}
	if reply.Scope != "" {
		oauth["scopes"] = strings.Fields(reply.Scope)
	}
	return json.Marshal(file)
}
