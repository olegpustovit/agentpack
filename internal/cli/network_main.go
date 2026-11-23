package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/state"
)

func (runner Runner) runGatewayMain(ctx context.Context, home string, args []string, empty bool) (int, error) {
	if len(args) > 0 && args[0] != "set" && args[0] != "archive-ready" {
		return 2, errors.New("expected gateway main, main set, or main archive-ready [--empty]")
	}
	action := "show"
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	if len(args) != 0 || empty && action != "archive-ready" {
		return 2, errors.New("unexpected gateway main arguments")
	}
	store, err := state.Open(home)
	if err != nil {
		return 1, err
	}
	defer store.Close()
	roles, err := store.Main(ctx)
	if err != nil {
		return 1, err
	}
	local, err := store.NodeID(ctx)
	if err != nil {
		return 1, err
	}
	switch action {
	case "show":
		if roles.MainNode == "" {
			_, err = fmt.Fprintln(runner.Stdout, "none · archive not ready")
		} else {
			ready := "archive not ready"
			if roles.MainNode == roles.ArchiveNode {
				ready = "archive ready"
			}
			_, err = fmt.Fprintf(runner.Stdout, "%s · %s\n", roles.MainName, ready)
		}
	case "set":
		if roles.MainNode != local && (roles.MainNode != "" || roles.ArchiveNode != "") {
			path, err := filepath.Abs(filepath.Join(home, "archive.db"))
			if err != nil {
				return 1, err
			}
			holder := roles.ArchiveName
			if holder == "" {
				holder = roles.MainName
			}
			if _, err := fmt.Fprintf(runner.Stderr, "archive.db on %s holds older chats and must be copied to this machine.\nFrom %s:%s\nTo %s\nStop the old gateway before copying. Copy archived network/sessions/<id>/blobs directories too, or restart both gateways to fetch attachments. Archiving is paused until you run ap gateway main archive-ready.\nType switch to change the main gateway: ", holder, holder, roles.ArchivePath, path); err != nil {
				return 1, err
			}
			answer, err := bufio.NewReader(runner.Stdin).ReadString('\n')
			if err != nil {
				return 1, err
			}
			if strings.TrimSpace(answer) != "switch" {
				return 1, errors.New("main gateway switch cancelled")
			}
		}
		if err := store.SetMain(ctx, roles, home); err != nil {
			return 1, err
		}
		_, err = fmt.Fprintln(runner.Stdout, "This machine is the main gateway.")
	case "archive-ready":
		if roles.MainNode != local {
			return 1, errors.New("run archive-ready on the main gateway")
		}
		if !empty {
			if err := network.PrepareArchive(ctx, store, home); err != nil {
				return 1, err
			}
		}
		if err := store.ArchiveReady(ctx, home, empty); err != nil {
			return 1, err
		}
		_, err = fmt.Fprintln(runner.Stdout, "Archive ready on the main gateway.")
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}
