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

Closing the terminal or tmux pane that launched an interactive agent stops that session and lets its cleanup finish. Closing a browser tab or temporarily losing its connection leaves the agent running. Detaching a tmux client keeps the pane and its agent running.

Package changes automatically restart interactive sessions whose selected mode inputs changed. Restarting launches a fresh conversation unless the original agent command explicitly requested resume. Headless commands retain their pipes and continue running; they are not restarted because replaying a batch operation could duplicate work.

## Library, installs, and offline work

Save an agentpack from a session's menu to record its manifest, exact package pins,
mode, and revision history under `$AGENTPACK_HOME/registry`. Enable automatic capture
for that project to append a revision after successful lock, add, remove, update,
or sync commands when its normalized content changes. MCP credentials stay in
private local bindings; snapshots that depend on them show “Needs local configuration”.

Install and restore sheets show the current pins before confirmation. Install can
target several trusted recent folders, with at most two requests running per machine.
Clear the restart checkbox to leave current agents running with their existing
configuration. Restore previews additions, changes, and removals, backs up both
definition files, and rolls them back if preparation fails. An interrupted restore
is recovered under the definition lock on the next mutation or launch; preflight
reports it without changing files.

Library replication requires explicit enrollment on both peers as well as the
existing Tailscale sharing policy. The gateway checks enrolled peers every 30 seconds
and exposes the last contact/error and durable object receipts. It transfers only
validated registry objects, never local credential bindings, terminal output, or
arbitrary project files. Transfers currently accept up to 64 MiB of metadata per
batch. Deletion creates a retained tombstone; physical collection waits for every
enrolled peer to acknowledge it. Concurrent revisions remain separate heads.

Offline installs are local drafts. Review rechecks the destination's identity,
pins, and current diff before showing a confirmation; reconnect never installs
automatically. If exact local-only package bytes are unavailable, the draft names
their recorded holder. Registry replication carries records, not cache file trees.
An imported revision that needs credentials must be configured locally before restore.

The browser app can be installed as a PWA. Its API responses are never cached.
Fit and zoom scale the host terminal grid; browser resizing does not resize the
remote PTY. Terminal colors use the labeled fallback palette. Reconnect replays
retained terminal bytes, which may be truncated; it is not a server screen snapshot.

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
sessions; read and write terminal output/input; and install packages. Local terminal,
browser, and MCP input share the same PTY. Each write is serialized; typing in two
places at once can mix keystrokes. The local terminal controls the grid size.

Existing session workers keep the behavior of the executable that launched them.
After upgrading, launch a new session to enable shared input. Updating the gateway
does not interrupt existing conversations; older workers remain view-only in the browser.

OAuth credentials persist privately across gateway restarts. Browser login sessions
expire and require signing in again after a gateway restart. OAuth clients can revoke
tokens through the advertised revocation endpoint.

REST restart requests and the MCP `restart_session` tool require an `operation_id`: a unique string of 16–128 letters, digits, hyphens, or underscores. Reuse it when retrying a request whose response was lost. The browser manages these IDs automatically. Each live session worker retains the last 128 operation results, including failures, so concurrent or repeated requests with the same retained ID do not restart an agent twice. Use a new ID for a new restart intent.
