package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func (runner Runner) runSharing(ctx context.Context, args []string) (int, error) {
	if len(args) == 0 {
		return 2, errors.New("expected gateway sharing on, off, status, allow <node-id|tag:name>, or revoke <node-id|tag:name>")
	}
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return 1, err
	}
	config, err := network.ReadSharingConfig(home)
	if err != nil {
		return 1, err
	}
	switch args[0] {
	case "status":
		if err = noArgs(args[1:]); err != nil {
			return 2, err
		}
		return 0, json.NewEncoder(runner.Stdout).Encode(config)
	case "on", "off":
		if err = noArgs(args[1:]); err != nil {
			return 2, err
		}
		config.Enabled = args[0] == "on"
		if config.Enabled {
			if err = network.CheckTailnet(ctx); err != nil {
				return 1, err
			}
		}
	case "allow", "revoke":
		if len(args) != 2 {
			return 2, errors.New("provide exactly one stable Tailscale node ID or tag:name")
		}
		if args[0] == "allow" && !slices.Contains(config.Allow, args[1]) {
			config.Allow = append(config.Allow, args[1])
		}
		if args[0] == "revoke" {
			config.Allow = slices.DeleteFunc(config.Allow, func(value string) bool { return value == args[1] })
		}
	default:
		return 2, fmt.Errorf("unknown sharing action %q", args[0])
	}
	if err = network.WriteSharingConfig(home, config); err != nil {
		return 1, err
	}
	if args[0] == "on" {
		if err := network.SetGatewayEnabled(home, true); err != nil {
			return 1, err
		}
		executable, err := os.Executable()
		if err != nil {
			return 1, err
		}
		if err = network.EnsureNode(ctx, home, executable); err != nil {
			return 1, err
		}
	}
	fmt.Fprintln(runner.Stdout, "Tailnet sharing configuration saved. The running gateway applies it within five seconds; CLI sessions keep running.")
	return 0, nil
}
