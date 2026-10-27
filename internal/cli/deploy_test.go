package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func deploymentELF(t *testing.T) (string, []byte) {
	t.Helper()
	header := make([]byte, 64)
	copy(header, "\x7fELF\x02\x01\x01")
	binary.LittleEndian.PutUint16(header[16:], 2)
	binary.LittleEndian.PutUint16(header[18:], 183)
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	path := filepath.Join(t.TempDir(), "gateway with spaces")
	if err := os.WriteFile(path, header, 0600); err != nil {
		t.Fatal(err)
	}
	return path, header
}

func TestDeployUploadAndCleanup(t *testing.T) {
	for _, failUpload := range []bool{false, true} {
		t.Run(fmt.Sprint(failUpload), func(t *testing.T) {
			path, data := deploymentELF(t)
			var commands []*exec.Cmd
			err := deployRemote(context.Background(), path, func(command *exec.Cmd) error {
				commands = append(commands, command)
				switch len(commands) {
				case 1:
					fmt.Fprintln(command.Stdout, "/tmp/agentpack-deploy.A1234567")
				case 2:
					if failUpload {
						return errors.New("upload failed")
					}
				case 3:
					if !failUpload {
						script, _ := io.ReadAll(command.Stdin)
						if string(script) != deployScript {
							t.Fatal("installer not passed on stdin")
						}
					}
				case 4:
					if !failUpload {
						want := fmt.Sprintf("bash /tmp/agentpack-deploy.A1234567/install.sh /tmp/agentpack-deploy.A1234567 %x", sha256.Sum256(data))
						if command.Args[len(command.Args)-1] != want {
							t.Fatalf("install arguments: %v", command.Args)
						}
					}
				}
				return nil
			})
			if (err != nil) != failUpload {
				t.Fatal(err)
			}
			upload := commands[1]
			want := []string{"scp", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "--", "./gateway with spaces", "oracle-vm:/tmp/agentpack-deploy.A1234567/agentpack"}
			if !reflect.DeepEqual(upload.Args, want) || upload.Dir != filepath.Dir(path) {
				t.Fatalf("upload: %v, cwd %s", upload.Args, upload.Dir)
			}
			last := commands[len(commands)-1]
			if last.Args[len(last.Args)-1] != "rm -rf -- /tmp/agentpack-deploy.A1234567" {
				t.Fatal("missing cleanup")
			}
		})
	}
}

func TestDeployRejectsUnsafeStageAndWrongBinary(t *testing.T) {
	path, _ := deploymentELF(t)
	calls := 0
	err := deployRemote(context.Background(), path, func(command *exec.Cmd) error {
		calls++
		fmt.Fprintln(command.Stdout, "/tmp/agentpack-deploy.A1234567; touch /tmp/unsafe")
		return nil
	})
	if err == nil || calls != 1 {
		t.Fatalf("unsafe output accepted: calls=%d, err=%v", calls, err)
	}
	if err := os.WriteFile(path, []byte("not executable"), 0600); err != nil {
		t.Fatal(err)
	}
	err = deployRemote(context.Background(), path, func(*exec.Cmd) error { t.Fatal("SSH called for invalid binary"); return nil })
	if err == nil {
		t.Fatal("invalid binary accepted")
	}
}

func TestDeployDryRun(t *testing.T) {
	var out bytes.Buffer
	runner := Runner{Stdout: &out, Stderr: io.Discard}
	code, err := runner.runDeploy(context.Background(), []string{"--dry-run"})
	if err != nil || code != 0 || !strings.Contains(out.String(), deployScript) {
		t.Fatalf("dry run: %d %v", code, err)
	}
	for _, args := range [][]string{{"--binary"}, {"--binary="}, {"--unknown"}} {
		code, err := runner.runDeploy(context.Background(), args)
		if err == nil || code != 2 {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
}

func TestDeployInstallerIdempotentAndRollback(t *testing.T) {
	for _, tool := range []string{"bash", "flock", "install", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("installer integration requires %s", tool)
		}
	}
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			root := t.TempDir()
			write := func(path, data string, mode os.FileMode) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(root+path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(root+path, []byte(data), mode); err != nil {
					t.Fatal(err)
				}
			}
			before := map[string]string{
				"/etc/caddy/Caddyfile":                            "agentpack.microapps.space { reverse_proxy 127.0.0.1:7420 }\n",
				"/.local/lib/agentpack/agentpack":                 "old gateway\n",
				"/.config/systemd/user/agentpack-gateway.service": "old service\n",
			}
			for path, data := range before {
				write(path, data, 0644)
			}
			payload := "#!/usr/bin/env bash\nexit 99\n"
			write("/tmp/agentpack-deploy.A1234567/agentpack", payload, 0700)
			write("/tools/uname", "#!/usr/bin/env bash\nif [[ $1 == -s ]]; then echo Linux; else echo aarch64; fi\n", 0700)
			write("/tools/loginctl", "#!/usr/bin/env bash\necho yes\n", 0700)
			write("/tools/curl", "#!/usr/bin/env bash\nif [[ $* == *127.0.0.1* && $* != *'Host: agentpack.microapps.space'* ]]; then exit 1; fi\necho '{\"setup_required\":true}'\n", 0700)
			write("/tools/systemctl", "#!/usr/bin/env bash\nif [[ ${DEPLOY_TEST_FAIL:-} == 1 && $2 == restart ]]; then exit 1; fi\nexit 0\n", 0700)
			script := deployScript
			// The shell validates the real remote upload path before mapping it into this fixture.
			script = strings.Replace(script, "work=\"$(mktemp", "upload='"+root+"'\"$upload\"\nwork=\"$(mktemp", 1)
			for attempt := 0; attempt < 2; attempt++ {
				command := exec.Command("bash", "-s", "--", "/tmp/agentpack-deploy.A1234567", fmt.Sprintf("%x", sha256.Sum256([]byte(payload))))
				command.Stdin = strings.NewReader(script)
				command.Env = append(os.Environ(), "PATH="+root+"/tools:"+os.Getenv("PATH"), "HOME="+root)
				if fail {
					command.Env = append(command.Env, "DEPLOY_TEST_FAIL=1")
				}
				out, err := command.CombinedOutput()
				if (err != nil) != fail {
					t.Fatalf("installer: %v\n%s", err, out)
				}
				if fail {
					for path, data := range before {
						got, _ := os.ReadFile(root + path)
						if string(got) != data {
							t.Fatalf("rollback did not restore %s: %s", path, got)
						}
					}
					if _, err := os.Stat(root + "/etc/caddy/agentpack.caddy"); !os.IsNotExist(err) {
						t.Fatal("rollback kept new virtual host")
					}
				} else {
					got, _ := os.ReadFile(root + "/etc/caddy/Caddyfile")
					if string(got) != before["/etc/caddy/Caddyfile"] {
						t.Fatalf("Caddy update: %s", got)
					}
				}
			}
		})
	}
}
