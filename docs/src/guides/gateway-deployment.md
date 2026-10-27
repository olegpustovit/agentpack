# Deploy the public gateway

`agentpack deploy` builds the current checkout for Linux ARM64 and deploys it over SSH to `oracle-vm`, serving `https://agentpack.microapps.space`. Run it from the Agentpack source repository with Go, OpenSSH `ssh` and `scp` installed. It works from PowerShell, Bash, and other shells; the command does not depend on a local Bash installation.

```powershell
agentpack deploy --dry-run
agentpack deploy
```

To deploy a prebuilt Linux ARM64 executable without Go or a source checkout:

```powershell
agentpack deploy --binary .\dist\agentpack-linux-arm64
```

On macOS or Linux, use `--binary ./dist/agentpack-linux-arm64`. `--dry-run` prints the remote installation script without building, uploading, or changing the server.

The SSH alias `oracle-vm` must connect with an SSH key. The server needs Bash, systemd user services, curl, coreutils, and util-linux (`flock`). Enable persistent user services once with `sudo loginctl enable-linger "$USER"` on the VM. Caddy and DNS must already serve `agentpack.microapps.space`, forwarding to `127.0.0.1:7420`; deployment preserves your Caddy configuration.

The installer verifies the uploaded SHA-256, installs `~/.local/lib/agentpack/agentpack`, and runs `agentpack-gateway.service` as the SSH user. Application data stays in `~/.local/share/agentpack-gateway` with private permissions. Subsequent deployments require no sudo.

After the first deployment, retrieve the administrator setup URL and private one-time token:

```powershell
ssh oracle-vm 'env AGENTPACK_HOME=$HOME/.local/share/agentpack-gateway AGENTPACK_PUBLIC_URL=https://agentpack.microapps.space $HOME/.local/lib/agentpack/agentpack gateway setup'
```

Open the URL, enter the token, and choose the administrator password. This creates the application's root account; the gateway does not run as OS root. The setup token protects an unconfigured public gateway from takeover. Do not share the token.

Subsequent deployments retain administrator credentials and application data. The installer checks gateway health and the public HTTPS route, and restores the previous binary and service configuration if activation fails. The previous executable also remains at `~/.local/lib/agentpack/agentpack.previous`. Rollback does not undo application data changes. Deployment fails and rolls back if the public status endpoint is unreachable.

Inspect or stop the gateway from PowerShell or any SSH-capable terminal:

```powershell
ssh oracle-vm systemctl --user status agentpack-gateway
ssh oracle-vm journalctl --user -u agentpack-gateway -n 100 --no-pager
ssh oracle-vm systemctl --user stop agentpack-gateway
```

Stopping this gateway closes remote access. The unit uses `KillMode=process` so independent session workers survive service stops and restarts. Use the application's separate stop-agents action to end sessions.

Package changes automatically restart interactive sessions whose selected mode inputs changed. Restarting launches a fresh conversation unless the original agent command explicitly requested resume. Headless commands retain their pipes and continue running; they are not restarted because replaying a batch operation could duplicate work.

## Connect an MCP client

Use `https://agentpack.microapps.space/mcp` as the remote Streamable HTTP MCP endpoint
once the public gateway is deployed. This is the Agentpack management server, separate
from MCP servers installed into an agent's environment.

The client discovers OAuth metadata and registers a public client automatically. Sign
in with the gateway root account and approve the connection. Authorization uses PKCE;
there is no shared client secret or setup token to paste into ChatGPT, Grok, or another
MCP client. Consent grants control over all machines accessible to this gateway
identity, including terminal input and package installation.

Tools list machines, sessions, recent folders and packages; start, stop and restart
sessions; read and write terminal output/input; and install packages. Terminal input
succeeds only while no CLI or browser holds control. Release local control with
`Ctrl+]` first.

OAuth credentials persist privately across gateway restarts. Browser login sessions
expire and require signing in again after a gateway restart. OAuth clients can revoke
tokens through the advertised revocation endpoint.

REST restart requests and the MCP `restart_session` tool require an `operation_id`: a unique string of 16–128 letters, digits, hyphens, or underscores. Reuse it when retrying a request whose response was lost. The browser manages these IDs automatically. Each live session worker retains the last 128 operation results, including failures, so concurrent or repeated requests with the same retained ID do not restart an agent twice. Use a new ID for a new restart intent.
