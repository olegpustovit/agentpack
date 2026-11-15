# Deploy the public gateway

`agentpack deploy` builds the current checkout for Linux ARM64 and deploys it over SSH to `oracle-vm`, serving `https://agentpack.microapps.space`. Run it from the Agentpack source repository with Go, Zig 0.17.1, Bash, Python 3.11+, curl, and OpenSSH installed. Source builds fetch the checksum-pinned cr-sqlite archive and link a static musl binary. Prebuilt deployment works from PowerShell, Bash, or other shells without the build toolchain.

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

Keep a new gateway behind private tailnet access or an access-controlled proxy until its owner is claimed. Open the gateway URL and choose **Claim gateway**: enter an email, a password of 12–72 bytes, and confirmation. An unclaimed gateway exposes only its claim/status endpoints, health check and static UI to browsers. `gateway start`, `gateway status`, and the hub print `UNCLAIMED: open <url> now` until an owner exists.

When upgrading a legacy gateway, claiming also requires its current password and preserves the existing password hash. The retained password is the login password; use `gateway passwd EMAIL` to change it later. Account and login state live in `state.db`. Browser logins survive restarts, last thirty days with sliding expiry, and use HttpOnly, SameSite=Strict cookies (Secure with the `__Host-` name on HTTPS).

Manage accounts from the server, including when the gateway is stopped:

```sh
agentpack gateway user add developer@example.com
agentpack gateway user list
agentpack gateway user disable developer@example.com
agentpack gateway passwd owner@example.com
agentpack gateway reset-owner
```

Password commands read hidden input twice from the terminal and accept no plaintext flags. Password changes and disabling revoke that account's logins. **Sign out everywhere** in Account settings also revokes its connected MCP assistants. `reset-owner` requires typing `reset`; it removes all accounts and revokes browser/OAuth logins, after which the gateway can be claimed again. Existing agents keep running.

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

Owners explicitly enroll each gateway in Settings → Replication on both machines.
Mesh members receive accounts, chat history, limits and Library records, so enroll
only machines you fully trust. Discovery alone never grants mesh access. Removed
machines stay removed until explicitly enrolled again; tailnet sharing must be on.

Objects are immutable and named by their SHA-256, and secrets are stripped before hashing, so machines converge on the same objects. Edits made on two machines merge three-way against their common ancestor; every machine computes the identical merge, so they agree without coordinating. When both changed one value differently, nothing is overwritten: the agentpack shows a conflict in the Library until someone chooses a value, and later captures continue their own project’s branch meanwhile. A deletion wins over a concurrent edit.

The gateway exposes last contact, schema version and sync progress. Shared state
travels in bounded atomic change pages. Public Library records are validated
before a page commits; raw local credential bindings, terminal output and
arbitrary project files are excluded. Deletion retains a tombstone, and CRR
deletion clocks let an offline machine learn collection after reconnecting.

Offline installs are saved drafts. Review rechecks the destination's identity,
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
in with the gateway account and approve the connection. Authorization uses PKCE;
there is no shared client secret to paste into ChatGPT, Grok, or another
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
