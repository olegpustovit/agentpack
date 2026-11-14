package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/state"
	"golang.org/x/term"
)

func (runner Runner) runGatewayAccounts(ctx context.Context, home string, args []string) (int, error) {
	action := args[0]
	if action == "user" {
		if len(args) < 2 {
			return 2, errors.New("expected gateway user add EMAIL, list, or disable EMAIL")
		}
		if args[1] != "add" && args[1] != "list" && args[1] != "disable" {
			return 2, errors.New("unknown gateway user command")
		}
		action, args = args[1], args[1:]
	}
	want := 2
	if action == "list" || action == "reset-owner" {
		want = 1
	}
	if len(args) != want {
		return 2, fmt.Errorf("unexpected arguments for gateway %s", action)
	}
	if action != "passwd" && action != "add" && action != "list" && action != "disable" && action != "reset-owner" {
		return 2, errors.New("unknown gateway account command")
	}
	store, err := state.Open(home)
	if err != nil {
		return 1, err
	}
	defer store.Close()
	switch action {
	case "list":
		users, err := network.ListUsers(ctx, store)
		if err != nil {
			return 1, err
		}
		for _, user := range users {
			if _, err := fmt.Fprintf(runner.Stdout, "%s\t%s\tdisabled=%t\n", user.Email, user.Role, user.Disabled); err != nil {
				return 1, err
			}
		}
	case "disable":
		if err := network.DisableUser(ctx, store, args[1]); err != nil {
			return 1, err
		}
		_, err = fmt.Fprintln(runner.Stdout, "Disabled "+args[1])
	case "passwd", "add":
		password, err := runner.readAccountPassword()
		if err != nil {
			return 1, err
		}
		if action == "add" {
			err = network.AddUser(ctx, store, args[1], password)
		} else {
			err = network.SetPassword(ctx, store, args[1], password)
		}
		if err != nil {
			return 1, err
		}
		_, err = fmt.Fprintln(runner.Stdout, "Saved account "+args[1])
	case "reset-owner":
		if _, err := fmt.Fprint(runner.Stderr, "Type reset to remove every gateway account and revoke all logins: "); err != nil {
			return 1, err
		}
		answer, err := bufio.NewReader(runner.Stdin).ReadString('\n')
		if err != nil {
			return 1, err
		}
		if strings.TrimSpace(answer) != "reset" {
			return 1, errors.New("owner reset cancelled")
		}
		if err := network.ResetOwner(ctx, store, home); err != nil {
			return 1, err
		}
		_, err = fmt.Fprintln(runner.Stdout, "Owner reset. Open the gateway to claim it again.")
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

func (runner Runner) readAccountPassword() (string, error) {
	file, ok := runner.Stdin.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", errors.New("password entry requires an interactive terminal")
	}
	values := [2]string{}
	for i, prompt := range []string{"Password: ", "Confirm password: "} {
		if _, err := fmt.Fprint(runner.Stderr, prompt); err != nil {
			return "", err
		}
		value, err := term.ReadPassword(int(file.Fd()))
		_, newlineErr := fmt.Fprintln(runner.Stderr)
		if err != nil {
			return "", err
		}
		if newlineErr != nil {
			return "", newlineErr
		}
		values[i] = string(value)
		clear(value)
	}
	if values[0] != values[1] {
		return "", errors.New("passwords do not match")
	}
	return values[0], nil
}

func (runner Runner) printGatewayClaim(ctx context.Context, home string) error {
	status, err := network.GatewayAccountStatus(ctx, home)
	if err != nil {
		return err
	}
	if status.Claimed {
		return nil
	}
	address := strings.TrimSpace(os.Getenv("AGENTPACK_PUBLIC_URL"))
	if address == "" {
		address, err = network.GatewayURL(home)
		if err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(runner.Stdout, "UNCLAIMED: open "+address+" now")
	return err
}
