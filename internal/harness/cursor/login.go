package cursor

import (
	"context"
	"os"
	"os/exec"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// LoginMethods are the ways the web UI signs Cursor in.
var LoginMethods = []base.LoginMethod{{ID: "cursor", Label: "Cursor account"}}

// Account looks for the token Cursor CLI keeps, the same one the usage probe reads.
func Account(context.Context) base.Account {
	if _, err := base.LookPathEnv(base.Cursor.Binary()); err != nil {
		return base.Account{}
	}
	account := base.Account{Installed: true}
	token, err := usageToken()
	if err != nil {
		account.Error = err.Error()
	}
	account.SignedIn = token != ""
	return account
}

// Logout runs `cursor-agent logout` with the real HOME, where Cursor keeps its login.
func Logout(ctx context.Context) error {
	exe, err := base.LookPathEnv(base.Cursor.Binary())
	if err != nil {
		return err
	}
	return base.RunLogout(ctx, exe, "logout")
}

// Login runs `cursor-agent login` with the real HOME, where Cursor keeps its login. With
// NO_OPEN_BROWSER it prints the link and waits for the browser sign-in; nothing is pasted back.
func Login(string) (base.LoginFlow, error) {
	exe, err := base.LookPathEnv(base.Cursor.Binary())
	if err != nil {
		return base.LoginFlow{}, err
	}
	command := exec.Command(exe, "login")
	command.Env = append(os.Environ(), "NO_OPEN_BROWSER=1")
	return base.LoginFlow{Command: command}, nil
}
