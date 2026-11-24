package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/paths"
)

var deviceCode = regexp.MustCompile(`\b[A-Z0-9]{4,5}-[A-Z0-9]{4,6}\b`)

// LoginMethods are the ways the web UI signs Codex in.
var LoginMethods = []base.LoginMethod{{ID: "chatgpt", Label: "ChatGPT account"}}

// Account reads the credential staged homes share, falling back to `codex login status` for a
// login kept only in the OS keychain.
func Account(ctx context.Context) base.Account {
	account, exe := base.ProbeAccount(base.Codex)
	if !account.Installed {
		return account
	}
	var sources []string
	if native, ok := nativeHome(); ok {
		sources = append(sources, filepath.Join(native, "auth.json"))
	}
	if shared, err := paths.SharedCodexAuthPath(); err == nil {
		sources = append(sources, shared)
	}
	for _, path := range sources {
		if data, err := os.ReadFile(path); err == nil && validAuthData(data) {
			account.SignedIn, account.Label = true, authEmail(data)
			return account
		}
	}
	result, err := base.RunProbe(ctx, base.ProbePlan{Executable: exe, Argv: []string{"login", "status"}, Timeout: 15 * time.Second, Categories: []string{"auth"}, WriteScope: "none"})
	if err != nil {
		account.Error = err.Error()
		return account
	}
	account.SignedIn = result.ExitCode == 0
	return account
}

// authEmail reads the email claim of the ChatGPT ID token; the token itself is never kept.
func authEmail(data []byte) string {
	var auth struct {
		Tokens struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &auth) != nil {
		return ""
	}
	parts := strings.Split(auth.Tokens.IDToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(payload, &claims)
	return claims.Email
}

// Logout signs Codex out of every project, as `agentpack codex logout` does.
func Logout(context.Context) error { return logoutShared() }

// Login runs `codex login --device-auth` in an isolated home, so an abandoned attempt never
// touches the current credential; success installs the new one as the shared source.
func Login(string) (base.LoginFlow, error) {
	exe, err := base.Codex.Resolve()
	if err != nil {
		return base.LoginFlow{}, err
	}
	shared, err := paths.SharedCodexAuthPath()
	if err != nil {
		return base.LoginFlow{}, err
	}
	if err := os.MkdirAll(filepath.Dir(shared), 0o700); err != nil {
		return base.LoginFlow{}, err
	}
	home, err := os.MkdirTemp(filepath.Dir(shared), ".codex-login-")
	if err != nil {
		return base.LoginFlow{}, err
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("cli_auth_credentials_store = \"file\"\n"), 0o600); err != nil {
		_ = os.RemoveAll(home)
		return base.LoginFlow{}, err
	}
	command := exec.Command(exe, "login", "--device-auth")
	command.Env = append(os.Environ(), "CODEX_HOME="+home)
	return base.LoginFlow{
		Command: command,
		Code:    deviceCode,
		Finish: func() error {
			data, err := os.ReadFile(filepath.Join(home, "auth.json"))
			if err != nil || !validAuthData(data) {
				return errors.New("Codex finished without saving a login")
			}
			_, err = persistLogin(data)
			return err
		},
		Cleanup: func() { _ = os.RemoveAll(home) },
	}, nil
}

// CredentialFile is the auth.json every staged Codex home links to: ~/.codex's when present, else
// agentpack's shared one (sharedAuthSource picks the same, without touching the keychain here).
func CredentialFile() string {
	if native, ok := nativeHome(); ok {
		if info, err := os.Stat(filepath.Join(native, "auth.json")); err == nil && info.Mode().IsRegular() {
			return filepath.Join(native, "auth.json")
		}
	}
	shared, err := paths.SharedCodexAuthPath()
	if err != nil {
		return ""
	}
	return shared
}
