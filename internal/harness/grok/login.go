package grok

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// LoginMethods are the ways the web UI signs Grok in.
var LoginMethods = []base.LoginMethod{{ID: "grok.com", Label: "Grok account"}}

// Account follows the order launches use: the shared login, else (before the first launch made
// one) a login from running grok directly. A recorded logout outranks that direct login.
func Account(context.Context) base.Account {
	account, _ := base.ProbeAccount(base.Grok)
	if !account.Installed {
		return account
	}
	shared, err := sharedCredentialsDir()
	if err != nil {
		account.Error = err.Error()
		return account
	}
	sharedAuth := filepath.Join(shared, "auth.json")
	if nonEmpty(sharedAuth) {
		account.SignedIn = true
		return account
	}
	if _, err := os.Stat(sharedAuth + ".initialized"); err == nil {
		return account
	}
	if home, ok := nativeHome(); ok {
		account.SignedIn = nonEmpty(filepath.Join(home, "auth.json"))
	}
	return account
}

func nonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 2
}

// Logout removes the shared login and marks the store as set up, so the next launch of any project
// drops its copy instead of importing one back.
func Logout(context.Context) error {
	shared, lock, err := lockSharedCredentials()
	if err != nil {
		return err
	}
	defer lock.Unlock()
	auth := filepath.Join(shared, "auth.json")
	if err := os.Remove(auth); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(auth+".initialized", nil, 0o600)
}

// Login runs grok's device-code login in an isolated home; success becomes the shared login that
// each project home picks up on its next launch.
func Login(string) (base.LoginFlow, error) {
	exe, err := base.Grok.Resolve()
	if err != nil {
		return base.LoginFlow{}, err
	}
	shared, err := sharedCredentialsDir()
	if err != nil {
		return base.LoginFlow{}, err
	}
	if err := os.MkdirAll(shared, 0o700); err != nil {
		return base.LoginFlow{}, err
	}
	home, err := os.MkdirTemp(filepath.Dir(shared), ".grok-login-")
	if err != nil {
		return base.LoginFlow{}, err
	}
	command := exec.Command(exe, "login", "--device-auth")
	command.Env = append(os.Environ(), "GROK_HOME="+home, "GROK_LOGIN_DEVICE_FLOW=true")
	return base.LoginFlow{
		Command: command,
		Finish: func() error {
			login := filepath.Join(home, "auth.json")
			if _, err := os.Stat(login); err != nil {
				return errors.New("Grok finished without saving a login")
			}
			shared, lock, err := lockSharedCredentials()
			if err != nil {
				return err
			}
			defer lock.Unlock()
			return copyCredential(login, filepath.Join(shared, "auth.json"))
		},
		Cleanup: func() { _ = os.RemoveAll(home) },
	}, nil
}

// CredentialFile is the shared Grok sign-in every project home loads before a launch.
func CredentialFile() string {
	shared, err := sharedCredentialsDir()
	if err != nil {
		return ""
	}
	return filepath.Join(shared, "auth.json")
}
