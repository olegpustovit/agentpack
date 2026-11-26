package cursor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// LoginMethods are the ways the web UI signs Cursor in.
var LoginMethods = []base.LoginMethod{{ID: "cursor", Label: "Cursor account"}}

// Account looks for the token Cursor CLI keeps, the same one the usage probe reads.
func Account(context.Context) base.Account {
	account, _ := base.ProbeAccount(base.Cursor)
	if !account.Installed {
		return account
	}
	token, err := usageToken()
	if err != nil {
		account.Error = err.Error()
	}
	account.SignedIn = token != ""
	return account
}

// Logout runs `cursor-agent logout` with the real HOME, where Cursor keeps its login.
func Logout(ctx context.Context) error {
	exe, err := base.Cursor.Resolve()
	if err != nil {
		return err
	}
	if _, err := os.Stat(CredentialFile()); runtime.GOOS != "darwin" || err != nil {
		return base.RunLogout(ctx, exe, "logout")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, exe, "logout")
	command.Env = append(os.Environ(), fileCredentialStore)
	if err := command.Run(); err != nil {
		return fmt.Errorf("sign out of Cursor's shared login: %w", err)
	}
	return nil
}

// Login runs `cursor-agent login` with the real HOME, where Cursor keeps its login. With
// NO_OPEN_BROWSER it prints the link and waits for the browser sign-in; nothing is pasted back.
func Login(string) (base.LoginFlow, error) {
	exe, err := base.Cursor.Resolve()
	if err != nil {
		return base.LoginFlow{}, err
	}
	command := exec.Command(exe, "login")
	command.Env = append(os.Environ(), "NO_OPEN_BROWSER=1")
	if runtime.GOOS == "darwin" {
		command.Env = append(command.Env, fileCredentialStore)
	}
	return base.LoginFlow{Command: command}, nil
}

const fileCredentialStore = "AGENT_CLI_CREDENTIAL_STORE=file"

// CredentialFile is Cursor's native file store. The gateway never reads the macOS keychain.
func CredentialFile() string {
	if os.Getenv("AGENT_CLI_CREDENTIAL_STORE") == "memory" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return credentialFile(runtime.GOOS, home, os.Getenv("XDG_CONFIG_HOME"), os.Getenv("APPDATA"))
}

func credentialFile(goos, home, config, appData string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, ".cursor", "auth.json")
	case "windows":
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Cursor", "auth.json")
	default:
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		return filepath.Join(config, "cursor", "auth.json")
	}
}

type fileLogin struct {
	AccessToken string `json:"accessToken"`
	APIKey      string `json:"apiKey"`
}

func (auth fileLogin) usable() bool {
	if auth.APIKey != "" {
		return true
	}
	if auth.AccessToken == "" {
		return false
	}
	// Some Cursor tokens are opaque. Only exclude a token if it declares an expired lifetime.
	expires := auth.expires()
	return expires.IsZero() || time.Now().Before(expires)
}

// expires is the lifetime the access token declares, zero when it declares none.
func (auth fileLogin) expires() time.Time {
	_, payload, ok := strings.Cut(auth.AccessToken, ".")
	payload, _, jwt := strings.Cut(payload, ".")
	if !ok || !jwt {
		return time.Time{}
	}
	body, err := base64.RawURLEncoding.DecodeString(payload)
	var claims struct {
		Expires int64 `json:"exp"`
	}
	if err != nil || json.Unmarshal(body, &claims) != nil || claims.Expires == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Expires, 0)
}

// Freshness is when the access token expires, zero for an opaque token or an API key.
func Freshness(data []byte) time.Time {
	var auth fileLogin
	if json.Unmarshal(data, &auth) != nil {
		return time.Time{}
	}
	return auth.expires()
}

// ValidCredential prevents a stale native login from replacing a usable shared login.
func ValidCredential(data []byte) bool {
	var auth fileLogin
	return json.Unmarshal(data, &auth) == nil && auth.usable()
}

func hasFileLogin() bool {
	path := CredentialFile()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && ValidCredential(data)
}

// Cursor's macOS file store follows HOME, including for launches using an existing generation.
func useMacFileLogin(fakeHome string) (bool, error) {
	if !hasFileLogin() {
		return false, nil
	}
	source := CredentialFile()
	dest := filepath.Join(fakeHome, ".cursor", "auth.json")
	if existing, err := os.Readlink(dest); err == nil && existing == source {
		return true, nil
	}
	if err := os.Symlink(source, dest); err != nil {
		// A simultaneous launch can install the link, and Cursor can replace it when it renews.
		if existing, readErr := os.Readlink(dest); readErr == nil && existing == source {
			return true, nil
		}
		if info, statErr := os.Lstat(dest); statErr == nil && info.Mode().IsRegular() {
			return true, nil
		}
		return false, fmt.Errorf("connect Cursor's shared login to its staged home: %w", err)
	}
	return true, nil
}
