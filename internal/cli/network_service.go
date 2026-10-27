package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/olegpustovit/agentpack/internal/network"
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

func (runner Runner) runNetworkService(ctx context.Context, args []string) (int, error) {
	if len(args) != 1 {
		return 2, errors.New("expected service install, start, stop, status, or uninstall")
	}
	action := args[0]
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return 1, err
	}
	if action == "run" {
		enabled, err := network.GatewayEnabled(home)
		if err != nil {
			return 1, err
		}
		if !enabled {
			return 0, nil
		}
		return runner.runGateway(ctx, []string{"serve", "--listen", "127.0.0.1:0"})
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
		if err = network.SetGatewayEnabled(home, false); err != nil {
			return 1, err
		}
	}
	if action != "status" {
		// Graceful IPC leaves independent session workers alive on every platform.
		if _, err := network.NodeRequest(ctx, home, "GET", "/healthz"); err == nil {
			if _, err = network.NodeRequest(ctx, home, "POST", "/api/node/stop"); err != nil {
				return 1, err
			}
			deadline := time.NewTimer(8 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
		wait:
			for {
				select {
				case <-ctx.Done():
					return 1, ctx.Err()
				case <-deadline.C:
					return 1, errors.New("gateway did not stop; sessions were left running")
				case <-ticker.C:
					if _, err = network.NodeRequest(ctx, home, "GET", "/healthz"); err != nil {
						break wait
					}
				}
			}
		}
	}
	if action == "install" || action == "start" {
		if err = network.SetGatewayEnabled(home, true); err != nil {
			return 1, err
		}
	}
	if runtime.GOOS == "darwin" && (action == "install" || action == "uninstall") {
		target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + gatewayLaunchLabel
		if exec.CommandContext(ctx, "launchctl", "print", target).Run() == nil {
			if err := exec.CommandContext(ctx, "launchctl", "bootout", target).Run(); err != nil {
				return 1, err
			}
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
		data, err := network.NodeRequest(ctx, home, "GET", "/api/sessions")
		if err != nil {
			return 1, fmt.Errorf("gateway is stopped or unavailable: %w", err)
		}
		fmt.Fprintln(runner.Stdout, string(data))
	}
	if action != "status" {
		fmt.Fprintln(runner.Stdout, "Gateway service "+action+" complete. Existing CLI sessions keep running.")
	}
	return 0, nil
}

func makeNetworkServicePlan(goos, action, executable, home, configHome, userHome, uid string) (networkServicePlan, error) {
	var plan networkServicePlan
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
		case "start":
			plan.commands = [][]string{{"systemctl", "--user", "restart", gatewayServiceName}}
		case "stop":
			plan.commands = [][]string{{"systemctl", "--user", "stop", gatewayServiceName}}
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
		case "start":
			plan.commands = [][]string{{"launchctl", "enable", domain + "/" + gatewayLaunchLabel}, {"launchctl", "kickstart", "-k", domain + "/" + gatewayLaunchLabel}}
		case "stop":
			plan.commands = [][]string{{"launchctl", "disable", domain + "/" + gatewayLaunchLabel}}
		case "status":
			plan.commands = [][]string{{"launchctl", "print", domain + "/" + gatewayLaunchLabel}}
		case "uninstall":
			plan.remove = true
		}
	case "windows":
		script := "$ErrorActionPreference = 'Stop'\n$user = [Security.Principal.WindowsIdentity]::GetCurrent()\n$name = 'Agentpack Gateway-' + $user.User.Value\n"
		switch action {
		case "install":
			launch := "$ErrorActionPreference = 'Stop'\n$env:AGENTPACK_HOME = " + powerShellLiteral(home) + "\n& " + powerShellLiteral(executable) + " gateway status *> $null\nif ($LASTEXITCODE -ne 0) { & " + powerShellLiteral(executable) + " service run *>> " + powerShellLiteral(filepath.Join(home, "network", "gateway-service.log")) + " }\nexit $LASTEXITCODE\n"
			// WMI starts outside the Scheduled Task job, preserving detached workers.
			bootstrap := "$ErrorActionPreference = 'Stop'\n$command = '\"' + (Get-Command powershell.exe).Source + '\" -NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand " + encodedPowerShell(launch) + "'\n$environment = [string[]](Get-ChildItem Env: | ForEach-Object { $_.Name + '=' + $_.Value })\n$startup = New-CimInstance -ClassName Win32_ProcessStartup -ClientOnly -Property @{ EnvironmentVariables = $environment; CreateFlags = [uint32]1024 }\n$result = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{ CommandLine = $command; ProcessStartupInformation = $startup }\nif ($result.ReturnValue -ne 0) { throw 'Could not start gateway process' }\n"
			script += "$action = New-ScheduledTaskAction -Execute (Get-Command powershell.exe).Source -Argument " + powerShellLiteral("-NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand "+encodedPowerShell(bootstrap)) + "\n$logon = New-ScheduledTaskTrigger -AtLogOn -User $user.Name\n$retry = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) -RepetitionInterval (New-TimeSpan -Minutes 1)\n$principal = New-ScheduledTaskPrincipal -UserId $user.Name -LogonType Interactive -RunLevel Limited\n$settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit (New-TimeSpan -Minutes 1)\nRegister-ScheduledTask -TaskName $name -Action $action -Trigger @($logon,$retry) -Principal $principal -Settings $settings -Force | Out-Null\nStart-ScheduledTask -TaskName $name\n"
		case "start":
			script += "Enable-ScheduledTask -TaskName $name | Out-Null\nStart-ScheduledTask -TaskName $name\n"
		case "stop":
			script += "Disable-ScheduledTask -TaskName $name | Out-Null\n"
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
