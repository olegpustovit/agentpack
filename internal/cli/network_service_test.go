package cli

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

func decodeServiceScript(t *testing.T, value string) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	if len(data)%2 != 0 {
		t.Fatal("invalid UTF-16 command")
	}
	units := make([]uint16, len(data)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(data[index*2:])
	}
	return string(utf16.Decode(units))
}
func TestUserServiceDefinitionsProtectSessionLifetimeAndPaths(t *testing.T) {
	executable := "/apps/my agent'$%/agentpack"
	home := "/data/雪 & quote' $%"
	linux, err := makeNetworkServicePlan("linux", "install", executable, home, "/config", "/home/user", "123")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"KillMode=process", "service run", `"AGENTPACK_HOME=/data/雪 & quote' $%%"`, `ExecStart="/apps/my agent'$$%%/agentpack"`, "WantedBy=default.target"} {
		if !strings.Contains(linux.content, fragment) {
			t.Fatalf("missing %q in %s", fragment, linux.content)
		}
	}
	mac, err := makeNetworkServicePlan("darwin", "install", executable, home, "/config", "/home/user", "123")
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(mac.content))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, fragment := range []string{"<key>AbandonProcessGroup</key><true/>", "<key>SuccessfulExit</key><false/>", "<string>service</string><string>run</string>", "雪 &amp; quote&#39; $%"} {
		if !strings.Contains(mac.content, fragment) {
			t.Fatalf("missing %q", fragment)
		}
	}
	windows, err := makeNetworkServicePlan("windows", "install", executable, home, "/config", "/home/user", "123")
	if err != nil {
		t.Fatal(err)
	}
	script := decodeServiceScript(t, windows.commands[0][4])
	for _, fragment := range []string{"-LogonType Interactive -RunLevel Limited", "-AtLogOn", "-RepetitionInterval", "-EncodedCommand", "$user.User.Value"} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("missing %q", fragment)
		}
	}
	prefix := "-EncodedCommand "
	start := strings.Index(script, prefix) + len(prefix)
	end := strings.Index(script[start:], "'") + start
	bootstrap := decodeServiceScript(t, script[start:end])
	if !strings.Contains(bootstrap, "Invoke-CimMethod -ClassName Win32_Process") || !strings.Contains(bootstrap, "EnvironmentVariables = $environment") || !strings.Contains(bootstrap, "CreateFlags = [uint32]1024") {
		t.Fatal("gateway would inherit scheduled task job")
	}
	start = strings.Index(bootstrap, prefix) + len(prefix)
	end = strings.Index(bootstrap[start:], "'") + start
	launch := decodeServiceScript(t, bootstrap[start:end])
	for _, fragment := range []string{"$env:AGENTPACK_HOME = '/data/雪 & quote'' $%'", "& '/apps/my agent''$%/agentpack' service run", "gateway status *> $null"} {
		if !strings.Contains(launch, fragment) {
			t.Fatalf("missing %q in %s", fragment, launch)
		}
	}
}
func TestServiceLifecycleCommandsAndInvalidPaths(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows"} {
		for _, action := range []string{"install", "start", "stop", "status", "uninstall"} {
			plan, err := makeNetworkServicePlan(platform, action, "/bin/agentpack", "/data", "/config", "/home", "42")
			if err != nil {
				t.Fatal(platform, action, err)
			}
			if action != "install" && plan.content != "" {
				t.Fatal("unexpected definition rewrite")
			}
			if platform == "windows" && action == "stop" {
				script := decodeServiceScript(t, plan.commands[0][4])
				if strings.Contains(script, "Stop-ScheduledTask") || !strings.Contains(script, "Disable-ScheduledTask") {
					t.Fatal("task stop would terminate live workers")
				}
			}
		}
	}
	for _, path := range []string{"/bad\npath", "/bad\rpath", "/bad\x00path"} {
		if _, err := makeNetworkServicePlan("linux", "install", path, "/data", "/config", "/home", "42"); err == nil {
			t.Fatal("control-character path accepted")
		}
	}
	if _, err := makeNetworkServicePlan("other", "install", "/bin/agentpack", "/data", "/config", "/home", "42"); err == nil {
		t.Fatal("unknown platform accepted")
	}
}
