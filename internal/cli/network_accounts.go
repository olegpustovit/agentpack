package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/olegpustovit/agentpack/internal/gateway"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/registry"
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
	if action != "whoami" && action != "passwd" && action != "add" && action != "list" && action != "disable" && action != "reset-owner" {
		return 2, errors.New("unknown gateway account command")
	}
	store, err := state.Open(home)
	if err != nil {
		return 1, err
	}
	defer store.Close()
	switch action {
	case "whoami":
		if err := gateway.BindLocalAccount(ctx, store, args[1]); err != nil {
			return 1, err
		}
		_, err = fmt.Fprintln(runner.Stdout, "Local gateway actions now use "+args[1])
	case "list":
		users, err := gateway.ListUsers(ctx, store)
		if err != nil {
			return 1, err
		}
		for _, user := range users {
			if _, err := fmt.Fprintf(runner.Stdout, "%s\t%s\tdisabled=%t\n", user.Email, user.Role, user.Disabled); err != nil {
				return 1, err
			}
		}
	case "disable":
		if err := gateway.DisableUser(ctx, store, args[1]); err != nil {
			return 1, err
		}
		_, err = fmt.Fprintln(runner.Stdout, "Disabled "+args[1])
	case "passwd", "add":
		password, err := runner.readAccountPassword()
		if err != nil {
			return 1, err
		}
		if action == "add" {
			err = gateway.AddUser(ctx, store, args[1], password)
		} else {
			err = gateway.SetPassword(ctx, store, args[1], password)
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
		if err := gateway.ResetOwner(ctx, store, home); err != nil {
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

// openGateway prints the running gateway's browser address and opens it; the web UI replaced the terminal hub.
func (runner Runner) openGateway(ctx context.Context) error {
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := runner.startGateway(ctx, home, executable); err != nil {
		return err
	}
	address := strings.TrimSpace(os.Getenv("AGENTPACK_PUBLIC_URL"))
	if address == "" {
		if address, err = gateway.GatewayURL(home); err != nil {
			return err
		}
	}
	fmt.Fprintln(runner.Stdout, address)
	if err := runner.printGatewayClaim(ctx, home); err != nil {
		return err
	}
	if os.Getenv("AGENTPACK_SESSION") != "1" {
		openInBrowser(address)
	}
	return nil
}

// openInBrowser is best effort: the printed address is the fallback when no desktop is available.
func openInBrowser(address string) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", address)
	case "windows":
		command = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", address)
	default:
		command = exec.Command("xdg-open", address)
	}
	if command.Start() == nil {
		go command.Wait()
	}
}

func (runner Runner) printGatewayClaim(ctx context.Context, home string) error {
	status, err := gateway.GatewayAccountStatus(ctx, home)
	if err != nil {
		return err
	}
	if status.Joining {
		_, err := fmt.Fprintln(runner.Stdout, "JOINING: waiting for mesh accounts; approve enrollment on the existing gateway.")
		return err
	}
	if status.Claimed {
		return nil
	}
	address := strings.TrimSpace(os.Getenv("AGENTPACK_PUBLIC_URL"))
	if address == "" {
		address, err = gateway.GatewayURL(home)
		if err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(runner.Stdout, "UNCLAIMED: open "+address+" now")
	return err
}

func (runner Runner) runGatewayJoin(ctx context.Context, home string, args []string) (int, error) {
	if len(args) != 1 {
		return 2, errors.New("expected gateway join NODE")
	}
	peer := args[0]
	if err := registry.ValidatePeer(peer); err != nil {
		return 2, err
	}
	if _, err := fmt.Fprintf(runner.Stderr, "Trust %s with account password hashes, chat history and shared state? Type trust to join: ", peer); err != nil {
		return 1, err
	}
	answer, err := bufio.NewReader(runner.Stdin).ReadString('\n')
	if err != nil {
		return 1, err
	}
	if strings.TrimSpace(answer) != "trust" {
		return 1, errors.New("mesh join cancelled")
	}
	store, err := state.Open(home)
	if err != nil {
		return 1, err
	}
	defer store.Close()
	if err := gateway.PrepareMeshJoin(ctx, store, peer); err != nil {
		return 1, err
	}
	library, err := registry.NewWithStore(store, filepath.Join(home, "registry"))
	if err != nil {
		return 1, err
	}
	if err := library.EnrollPeer(peer); err != nil {
		return 1, err
	}
	_, err = fmt.Fprintf(runner.Stdout, "Joining %s. Owner claiming is disabled while accounts sync.\nRun agentpack gateway start, then approve this machine in the existing gateway's Replication settings.\n", peer)
	if err != nil {
		return 1, err
	}
	return 0, nil
}
