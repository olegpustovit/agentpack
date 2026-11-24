package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/olegpustovit/agentpack/internal/gateway"
	"github.com/olegpustovit/agentpack/internal/paths"
)

const gatewayServiceName = "agentpack-gateway.service"
const gatewayLaunchLabel = "space.microapps.agentpack.gateway"

type networkServicePlan struct {
	path     string
	content  string
	commands [][]string
	remove   bool
}

func gatewayServiceAvailable(ctx context.Context, goos string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	switch goos {
	case "linux":
		return exec.CommandContext(ctx, "systemctl", "--user", "show-environment").Run() == nil && userManagerSharesNetwork(ctx)
	case "darwin":
		return exec.CommandContext(ctx, "launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())).Run() == nil
	case "windows":
		return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "if ((Get-Service Schedule).Status -ne 'Running') { exit 1 }").Run() == nil
	default:
		return false
	}
}

// userManagerSharesNetwork reports whether the systemd user manager runs in this process's network
// namespace. Some WSL setups start it in another one, where the tailnet address and the loopback
// endpoint the CLI dials do not exist; the gateway then runs as a detached process instead.
// A namespace this process cannot read belongs to another user namespace, so it is not shared.
func userManagerSharesNetwork(ctx context.Context) bool {
	output, err := exec.CommandContext(ctx, "systemctl", "show", "-p", "MainPID", "--value", "user@"+strconv.Itoa(os.Getuid())+".service").Output()
	if err != nil {
		return false
	}
	manager, err := os.Readlink("/proc/" + strings.TrimSpace(string(output)) + "/ns/net")
	if err != nil {
		return false
	}
	own, err := os.Readlink("/proc/self/ns/net")
	return err == nil && own == manager
}

func (runner Runner) startGateway(ctx context.Context, home, executable string) error {
	if !gatewayServiceAvailable(ctx, runtime.GOOS) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := gateway.SetGatewayEnabled(home, true); err != nil {
			return err
		}
		return gateway.EnsureNode(ctx, home, executable)
	}
	if _, err := runner.runNetworkService(ctx, []string{"start"}); err != nil {
		return err
	}
	if err := gateway.WaitForGateway(ctx, home); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if username, err := osuser.Current(); err == nil {
			linger, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := exec.CommandContext(linger, "loginctl", "enable-linger", username.Username).Run()
			cancel()
			if err != nil {
				fmt.Fprintf(runner.Stderr, "To start the gateway at boot and keep it running after logout, run loginctl enable-linger %s\n", username.Username)
			}
		}
	}
	fmt.Fprintln(runner.Stdout, "Gateway starts at login (ap gateway stop to turn off)")
	return nil
}

func (runner Runner) runNetworkService(ctx context.Context, args []string) (int, error) {
	if len(args) != 1 {
		return 2, errors.New("expected service start, stop, status, or uninstall")
	}
	action := args[0]
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return 1, err
	}
	if action == "run" {
		enabled, err := gateway.GatewayEnabled(home)
		if err != nil {
			return 1, err
		}
		if !enabled {
			return 0, nil
		}
		executable, err := os.Executable()
		if err != nil {
			return 1, err
		}
		return runner.serveGateway(ctx, home, executable, "127.0.0.1:0")
	}
	executable, err := os.Executable()
	if err != nil {
		return 1, err
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return 1, err
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(userHome, ".config")
	}
	plan, err := makeNetworkServicePlan(runtime.GOOS, action, executable, home, configHome, userHome, strconv.Itoa(os.Getuid()))
	if err != nil {
		return 2, err
	}
	if action == "stop" || action == "uninstall" {
		if err = gateway.SetGatewayEnabled(home, false); err != nil {
			return 1, err
		}
	}
	if action != "status" {
		// Graceful IPC leaves independent session workers alive on every platform.
		if _, err := gateway.NodeRequest(ctx, home, "GET", "/healthz"); err == nil {
			if err = gateway.StopGateway(ctx, home); err != nil {
				return 1, err
			}
		}
	}
	if action == "start" {
		if err = gateway.SetGatewayEnabled(home, true); err != nil {
			return 1, err
		}
	}
	if runtime.GOOS == "darwin" && (action == "start" || action == "stop" || action == "uninstall") {
		target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + gatewayLaunchLabel
		if exec.CommandContext(ctx, "launchctl", "print", target).Run() == nil {
			if err := exec.CommandContext(ctx, "launchctl", "bootout", target).Run(); err != nil {
				return 1, err
			}
		}
	}
	if action == "stop" && runtime.GOOS == "linux" {
		if _, err := os.Stat(plan.path); errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
	}
	if plan.content != "" {
		if err = os.MkdirAll(filepath.Dir(plan.path), 0700); err != nil {
			return 1, err
		}
		if err = os.WriteFile(plan.path, []byte(plan.content), 0600); err != nil {
			return 1, err
		}
	}
	for _, arguments := range plan.commands {
		command := exec.CommandContext(ctx, arguments[0], arguments[1:]...)
		command.Stdin, command.Stdout, command.Stderr = runner.Stdin, runner.Stdout, runner.Stderr
		if err = command.Run(); err != nil {
			return 1, fmt.Errorf("%s %s: %w", arguments[0], action, err)
		}
	}
	if plan.remove && plan.path != "" {
		if err = os.Remove(plan.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 1, err
		}
	}
	if plan.remove && runtime.GOOS == "linux" {
		if err := exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run(); err != nil {
			return 1, err
		}
	}
	if action == "status" && runtime.GOOS == "windows" {
		data, err := gateway.NodeRequest(ctx, home, "GET", "/api/sessions")
		if err != nil {
			return 1, fmt.Errorf("gateway is stopped or unavailable: %w", err)
		}
		fmt.Fprintln(runner.Stdout, string(data))
	}
	if action != "status" && action != "start" {
		fmt.Fprintln(runner.Stdout, "Gateway service "+action+" complete. Existing CLI sessions keep running.")
	}
	return 0, nil
}

func makeNetworkServicePlan(goos, action, executable, home, configHome, userHome, uid string) (networkServicePlan, error) {
	var plan networkServicePlan
	if action == "start" {
		action = "install"
	}
	if action != "install" && action != "start" && action != "stop" && action != "status" && action != "uninstall" {
		return plan, fmt.Errorf("unknown service action %q", action)
	}
	for _, value := range []string{executable, home, configHome, userHome} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return plan, errors.New("service paths must not contain control characters")
		}
	}
	switch goos {
	case "linux":
		plan.path = filepath.Join(configHome, "systemd", "user", gatewayServiceName)
		switch action {
		case "install":
			plan.content = "[Unit]\nDescription=Agentpack gateway\nAfter=network.target\n\n[Service]\nType=simple\nExecStart=" + systemdWord(strings.ReplaceAll(executable, "$", "$$")) + " service run\nEnvironment=" + systemdWord("AGENTPACK_HOME="+home) + "\nRestart=on-failure\nRestartSec=3\nKillMode=process\nTimeoutStopSec=10\n\n[Install]\nWantedBy=default.target\n"
			plan.commands = [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", gatewayServiceName}, {"systemctl", "--user", "restart", gatewayServiceName}}
		case "stop":
			plan.commands = [][]string{{"systemctl", "--user", "disable", "--now", gatewayServiceName}}
		case "status":
			plan.commands = [][]string{{"systemctl", "--user", "status", "--no-pager", gatewayServiceName}}
		case "uninstall":
			plan.commands = [][]string{{"systemctl", "--user", "disable", "--now", gatewayServiceName}}
			plan.remove = true
		}
	case "darwin":
		plan.path = filepath.Join(userHome, "Library", "LaunchAgents", gatewayLaunchLabel+".plist")
		domain := "gui/" + uid
		switch action {
		case "install":
			plan.content = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict><key>Label</key><string>" + gatewayLaunchLabel + "</string><key>ProgramArguments</key><array><string>" + xmlText(executable) + "</string><string>service</string><string>run</string></array><key>EnvironmentVariables</key><dict><key>AGENTPACK_HOME</key><string>" + xmlText(home) + "</string></dict><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>AbandonProcessGroup</key><true/><key>ThrottleInterval</key><integer>3</integer><key>StandardOutPath</key><string>" + xmlText(filepath.Join(home, "network", "gateway-service.log")) + "</string><key>StandardErrorPath</key><string>" + xmlText(filepath.Join(home, "network", "gateway-service.log")) + "</string></dict></plist>\n"
			plan.commands = [][]string{{"launchctl", "enable", domain + "/" + gatewayLaunchLabel}, {"launchctl", "bootstrap", domain, plan.path}, {"launchctl", "kickstart", "-k", domain + "/" + gatewayLaunchLabel}}
		case "stop":
			plan.commands = [][]string{{"launchctl", "disable", domain + "/" + gatewayLaunchLabel}}
		case "status":
			plan.commands = [][]string{{"launchctl", "print", domain + "/" + gatewayLaunchLabel}}
		case "uninstall":
			plan.remove = true
		}
	case "windows":
		script := "$ErrorActionPreference = 'Stop'\n$ProgressPreference = 'SilentlyContinue'\n$user = [Security.Principal.WindowsIdentity]::GetCurrent()\n$name = 'Agentpack Gateway-' + $user.User.Value\n"
		switch action {
		case "install":
			// No ErrorActionPreference Stop here: Windows PowerShell turns the stopped gateway's status error
			// on stderr into a terminating error, which would end the script before service run.
			launch := "$env:AGENTPACK_HOME = " + powerShellLiteral(home) + "\n& " + powerShellLiteral(executable) + " gateway status *> $null\nif ($LASTEXITCODE -ne 0) { & " + powerShellLiteral(executable) + " service run *>> " + powerShellLiteral(filepath.Join(home, "network", "gateway-service.log")) + " }\nexit $LASTEXITCODE\n"
			// WMI starts outside the Scheduled Task job, preserving detached workers.
			bootstrap := "$ErrorActionPreference = 'Stop'\n$command = '\"' + (Get-Command powershell.exe).Source + '\" -NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand " + encodedPowerShell(launch) + "'\n$environment = [string[]](Get-ChildItem Env: | ForEach-Object { $_.Name + '=' + $_.Value })\n$startup = New-CimInstance -ClassName Win32_ProcessStartup -ClientOnly -Property @{ EnvironmentVariables = $environment; CreateFlags = [uint32]1024 }\n$result = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{ CommandLine = $command; ProcessStartupInformation = $startup }\nif ($result.ReturnValue -ne 0) { throw 'Could not start gateway process' }\n"
			script += "$action = New-ScheduledTaskAction -Execute (Get-Command powershell.exe).Source -Argument " + powerShellLiteral("-NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand "+encodedPowerShell(bootstrap)) + "\n$logon = New-ScheduledTaskTrigger -AtLogOn -User $user.Name\n$retry = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) -RepetitionInterval (New-TimeSpan -Minutes 1)\n$principal = New-ScheduledTaskPrincipal -UserId $user.Name -LogonType Interactive -RunLevel Limited\n$settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit (New-TimeSpan -Minutes 1)\nRegister-ScheduledTask -TaskName $name -Action $action -Trigger @($logon,$retry) -Principal $principal -Settings $settings -Force | Out-Null\nStart-ScheduledTask -TaskName $name\n"
		case "stop":
			script += "if (Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue) { Disable-ScheduledTask -TaskName $name | Out-Null }\n"
		case "status":
			script += "Get-ScheduledTask -TaskName $name | Format-List TaskName,State\nGet-ScheduledTaskInfo -TaskName $name | Format-List LastRunTime,LastTaskResult,NextRunTime\n"
		case "uninstall":
			script += "Unregister-ScheduledTask -TaskName $name -Confirm:$false\n"
		}
		plan.commands = [][]string{{"powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodedPowerShell(script)}}
	default:
		return plan, fmt.Errorf("gateway user services are unsupported on %s", goos)
	}
	return plan, nil
}
func systemdWord(value string) string {
	return strconv.Quote(strings.ReplaceAll(value, "%", "%%"))
}
func xmlText(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}
func powerShellLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func encodedPowerShell(value string) string {
	units := utf16.Encode([]rune(value))
	data := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[i*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func loginShell(ctx context.Context) (string, []string) {
	if runtime.GOOS == "windows" {
		return cmp.Or(os.Getenv("ComSpec"), "cmd.exe"), nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		// User services can start without SHELL, so ask the operating system for the account default.
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		switch runtime.GOOS {
		case "linux":
			if passwd, err := exec.CommandContext(ctx, "getent", "passwd", strconv.Itoa(os.Getuid())).Output(); err == nil {
				fields := strings.SplitN(strings.TrimSpace(string(passwd)), ":", 7)
				if len(fields) == 7 {
					shell = fields[6]
				}
			}
		case "darwin":
			if current, err := osuser.Current(); err == nil {
				if output, err := exec.CommandContext(ctx, "/usr/bin/dscl", ".", "-read", "/Users/"+current.Username, "UserShell").Output(); err == nil {
					_, value, _ := strings.Cut(string(output), "UserShell:")
					shell = strings.TrimSpace(value)
				}
			}
		}
	}
	return cmp.Or(shell, "/bin/sh"), []string{"-l"}
}

// loginShellPath asks the user's login shell for its PATH. The marker skips anything a profile
// prints. An empty result means the caller keeps the PATH it has.
func loginShellPath(ctx context.Context, shell string) string {
	if shell == "" || runtime.GOOS == "windows" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	const marker = "__agentpack_path__"
	output, err := exec.CommandContext(ctx, shell, "-l", "-c", `printf '`+marker+`%s' "$PATH"`).Output()
	if err != nil {
		return ""
	}
	_, path, found := strings.Cut(string(output), marker)
	if !found {
		return ""
	}
	return path
}
