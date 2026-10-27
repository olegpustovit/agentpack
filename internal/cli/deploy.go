package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed deploy_remote.sh
var deployScript string

var deployStagePattern = regexp.MustCompile(`^/tmp/agentpack-deploy\.[A-Za-z0-9]{8}$`)

func (runner Runner) runDeploy(ctx context.Context, arguments []string) (int, error) {
	binary, arguments, supplied, err := takeFlag(arguments, "--binary")
	if err != nil {
		return 2, err
	}
	arguments, dryRun := takeBool(arguments, "--dry-run")
	if err := noArgs(arguments); err != nil {
		return 2, err
	}
	if supplied && binary == "" {
		return 2, errors.New("--binary requires a Linux ARM64 executable")
	}
	if dryRun {
		fmt.Fprintln(runner.Stdout, "# Target: oracle-vm; public URL: https://agentpack.microapps.space")
		fmt.Fprintln(runner.Stdout, "# Uses persistent user services; Caddy must already forward the domain to 127.0.0.1:7420.")
		fmt.Fprintln(runner.Stdout, "# Remote installer; arguments are upload directory and SHA-256. No commands executed.")
		_, err := io.WriteString(runner.Stdout, deployScript)
		return 0, err
	}
	run := func(command *exec.Cmd) error {
		if command.Stdout == nil {
			command.Stdout = runner.Stdout
		}
		command.Stderr = runner.Stderr
		return command.Run()
	}
	if !supplied {
		manifest, err := os.ReadFile("go.mod")
		if err != nil || !strings.Contains(string(manifest), "module github.com/olegpustovit/agentpack\n") {
			return 2, errors.New("run deploy from the Agentpack source checkout, or pass --binary with a Linux ARM64 build")
		}
		directory, err := os.MkdirTemp("", "agentpack-deploy-")
		if err != nil {
			return 1, fmt.Errorf("create deployment build directory: %w", err)
		}
		defer os.RemoveAll(directory)
		binary = filepath.Join(directory, "agentpack")
		fmt.Fprintln(runner.Stderr, "Building gateway for Linux ARM64…")
		build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", binary, "./cmd/agentpack")
		build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")
		if err := run(build); err != nil {
			return 1, fmt.Errorf("build gateway: %w", err)
		}
	}
	if err := deployRemote(ctx, binary, run); err != nil {
		return 1, err
	}
	fmt.Fprintln(runner.Stdout, "Deployed https://agentpack.microapps.space")
	fmt.Fprintln(runner.Stdout, "First login: retrieve the one-time administrator setup link with:")
	fmt.Fprintln(runner.Stdout, "ssh oracle-vm 'env AGENTPACK_HOME=$HOME/.local/share/agentpack-gateway AGENTPACK_PUBLIC_URL=https://agentpack.microapps.space $HOME/.local/lib/agentpack/agentpack gateway setup'")
	return 0, nil
}

func deployRemote(ctx context.Context, binary string, run func(*exec.Cmd) error) error {
	binary, err := filepath.Abs(binary)
	if err != nil {
		return fmt.Errorf("resolve deployment binary: %w", err)
	}
	file, err := os.Open(binary)
	if err != nil {
		return fmt.Errorf("open deployment binary: %w", err)
	}
	defer file.Close()
	executable, err := elf.NewFile(file)
	if err != nil || executable.Machine != elf.EM_AARCH64 || executable.Class != elf.ELFCLASS64 {
		return errors.New("deployment binary must be a Linux ARM64 ELF executable")
	}
	if executable.Type != elf.ET_EXEC && executable.Type != elf.ET_DYN {
		return errors.New("deployment binary is not executable")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash deployment binary: %w", err)
	}
	digest := fmt.Sprintf("%x", hash.Sum(nil))
	ssh := func(ctx context.Context, arguments ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "ssh", append([]string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "oracle-vm"}, arguments...)...)
	}
	var output bytes.Buffer
	stage := ssh(ctx, "mktemp -d /tmp/agentpack-deploy.XXXXXXXX")
	stage.Stdout = &output
	if err := run(stage); err != nil {
		return fmt.Errorf("create remote upload directory: %w", err)
	}
	remote := strings.TrimSpace(output.String())
	if !deployStagePattern.MatchString(remote) {
		return errors.New("SSH returned an invalid deployment directory")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// The validated mktemp path contains no shell metacharacters.
		_ = run(ssh(cleanupCtx, "rm -rf -- "+remote))
	}()
	// A relative basename avoids scp interpreting a Windows drive letter as a remote host.
	upload := exec.CommandContext(ctx, "scp", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "--", "./"+filepath.Base(binary), "oracle-vm:"+remote+"/agentpack")
	upload.Dir = filepath.Dir(binary)
	if err := run(upload); err != nil {
		return fmt.Errorf("upload gateway: %w", err)
	}
	script := ssh(ctx, "cat > "+remote+"/install.sh")
	script.Stdin = strings.NewReader(deployScript)
	if err := run(script); err != nil {
		return fmt.Errorf("upload gateway installer: %w", err)
	}
	install := ssh(ctx, "bash "+remote+"/install.sh "+remote+" "+digest)
	if err := run(install); err != nil {
		return fmt.Errorf("install gateway: %w", err)
	}
	return nil
}
