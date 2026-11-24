package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/gateway"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
)

// updateGateway installs version with the release installer and restarts the gateway service.
func (runner Runner) updateGateway(ctx context.Context, version string, progress func(gateway.UpdatePhase)) error {
	client := &http.Client{Timeout: 30 * time.Second}
	tag := "v" + version
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	installerURL := "https://github.com/olegpustovit/agentpack/releases/download/" + tag + "/agentpack-installer.sh"
	if runtime.GOOS == "windows" {
		installerURL = "https://github.com/olegpustovit/agentpack/releases/download/" + tag + "/agentpack-installer.ps1"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, installerURL, nil)
	if err != nil {
		return err
	}
	progress(gateway.UpdateDownloading)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download the release installer: %w", err)
	}
	installer, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		return errors.New("could not download the release installer")
	}
	installDir := filepath.Dir(executable)
	var command *exec.Cmd
	if runtime.GOOS == "windows" {
		command = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "-")
	} else {
		command = exec.CommandContext(ctx, "sh", "-s", "--", "-q")
	}
	command.Stdin = strings.NewReader(string(installer))
	command.Stdout, command.Stderr = runner.Stdout, runner.Stderr
	command.Env = append(os.Environ(), "AGENTPACK_VERSION="+version, "AGENTPACK_INSTALL_DIR="+installDir, "AGENTPACK_RESTART_SERVICE=0")
	if err := command.Run(); err != nil {
		return fmt.Errorf("install Agentpack %s: %w", version, err)
	}
	installed := filepath.Join(installDir, "ap")
	if runtime.GOOS == "windows" {
		installed += ".exe"
	}
	if filepath.Clean(installed) != filepath.Clean(executable) {
		if err := copyExecutable(installed, executable); err != nil {
			return err
		}
	}
	// Another process must wait for the old gateway to exit before restarting it.
	progress(gateway.UpdateRestarting)
	restart := exec.Command(executable, "gateway", "restart-after-update")
	network.ConfigureDetached(restart)
	restart.Stdout, restart.Stderr = runner.Stdout, runner.Stderr
	if err := restart.Start(); err != nil {
		return fmt.Errorf("schedule gateway restart after update: %w", err)
	}
	obs.Go("gateway.update.restart", func() {
		if err := restart.Wait(); err != nil {
			_, event := obs.Start(obs.WithRecording(context.Background(), ctx), "gateway", "restart after update")
			event.Fail(err)
			event.End()
		}
	})
	return nil
}

func copyExecutable(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	temporary := destination + ".update"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		backup := destination + ".update-old"
		_ = os.Remove(backup)
		if moveErr := os.Rename(destination, backup); moveErr != nil {
			os.Remove(temporary)
			return errors.Join(err, moveErr)
		}
		if moveErr := os.Rename(temporary, destination); moveErr != nil {
			_ = os.Rename(backup, destination)
			os.Remove(temporary)
			return errors.Join(err, moveErr)
		}
		_ = os.Remove(backup)
	}
	return nil
}
