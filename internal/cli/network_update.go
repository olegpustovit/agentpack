package cli

import (
	"context"
	"encoding/json"
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
)

func (runner Runner) updateGateway(ctx context.Context) error {
	client := &http.Client{Timeout: 30 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/olegpustovit/agentpack/releases/latest", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("check the latest Agentpack release: %w", err)
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&release)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !strings.HasPrefix(release.Tag, "v") {
		return errors.New("GitHub returned invalid latest release metadata")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	installerURL := "https://github.com/olegpustovit/agentpack/releases/download/" + release.Tag + "/agentpack-installer.sh"
	if runtime.GOOS == "windows" {
		installerURL = "https://github.com/olegpustovit/agentpack/releases/download/" + release.Tag + "/agentpack-installer.ps1"
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, installerURL, nil)
	if err != nil {
		return err
	}
	response, err = client.Do(request)
	if err != nil {
		return fmt.Errorf("download the release installer: %w", err)
	}
	installer, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		return errors.New("could not download the release installer")
	}
	version := strings.TrimPrefix(release.Tag, "v")
	installDir := filepath.Dir(executable)
	var command *exec.Cmd
	if runtime.GOOS == "windows" {
		command = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "-")
	} else {
		command = exec.CommandContext(ctx, "sh", "-s", "--", "-q")
	}
	command.Stdin = strings.NewReader(string(installer))
	command.Stdout, command.Stderr = runner.Stdout, runner.Stderr
	command.Env = append(os.Environ(), "AGENTPACK_VERSION="+version, "AGENTPACK_INSTALL_DIR="+installDir)
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
	code, err := runner.runNetworkService(ctx, []string{"start"})
	if err != nil {
		return fmt.Errorf("restart gateway after update: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("restart gateway after update exited with status %d", code)
	}
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
