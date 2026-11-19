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

The service sets `AGENTPACK_PUBLIC_URL=https://agentpack.microapps.space`, which pins **Public access** on with that origin (see below). The installer verifies the uploaded SHA-256, installs `~/.local/lib/agentpack/agentpack`, and runs `agentpack-gateway.service` as the SSH user. Application data stays in `~/.local/share/agentpack-gateway` with private permissions. Subsequent deployments require no sudo.

Keep a new gateway behind private tailnet access or an access-controlled proxy until its owner is claimed. Open the gateway URL and choose **Claim gateway**: enter an email, a password of 12–72 bytes, and confirmation. An unclaimed gateway exposes only its claim/status endpoints, health check and static UI to browsers. `gateway start`, `gateway status`, and bare `ap` print `UNCLAIMED: open <url> now` until an owner exists.

When upgrading a legacy gateway, claiming also requires its current password and preserves the existing password hash. The retained password is the login password; use `gateway passwd EMAIL` to change it later. Account and login state live in `state.db`. Browser logins survive restarts, last thirty days with sliding expiry, and use HttpOnly, SameSite=Strict cookies (Secure with the `__Host-` name on HTTPS).

## Public access

A gateway always answers its loopback address (`http://127.0.0.1:7420`). **Settings › Sharing › Public access** adds one HTTPS origin, such as a Caddy or nginx site or a Tailscale Funnel, without tokens or other configuration:

- **Detection.** While the switch is off, a request that a proxy on the same machine forwards with `X-Forwarded-Proto: https` gets a "Public access is off" page, and its host (`X-Forwarded-Host`, else `Host`) becomes the candidate the pane shows. A Tailscale Funnel that proxies to the gateway's port, read from Tailscale's serve configuration, is also a candidate. The newest forwarded host wins, so check that the detected name is yours before you turn it on.
- **Verification.** Only the owner of a claimed gateway can turn it on. The gateway then fetches `<origin>/.well-known/oauth-protected-resource/mcp` over verified TLS with a one-time nonce and enables the origin only if the answer comes from itself and names `<origin>/mcp`. Each check is reported in the pane as it completes; a failure leaves the switch off and says which check failed.
- **Serving.** The loopback origin keeps working. Host, CSRF, WebSocket origin checks and the OAuth metadata use whichever of the two origins the request addressed. Cookies on the HTTPS origin are `Secure` with the `__Host-` name; the loopback origin keeps the plain cookie, so each origin has its own login. `X-Forwarded-Host` and `X-Forwarded-For` are trusted only from a loopback peer and only while the switch is on. Login and OAuth rate limits then key on the last `X-Forwarded-For` hop, so one client cannot lock out the others.
- **Pinning.** `AGENTPACK_PUBLIC_URL=https://…` pins the switch on with that origin; the pane shows it locked. After `gateway reset-owner`, a switched-on (unpinned) public origin shows the "off" page until an owner is claimed again.

The proxy must preserve the original host in `Host` or `X-Forwarded-Host` and set `X-Forwarded-Proto`. Caddy's `reverse_proxy` and Tailscale Funnel do both by default.

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

Gateways pair automatically: every gateway on the tailnet sharing allow list joins
the mesh as soon as discovery reaches it, and an unclaimed one takes the peer's
accounts. Mesh members receive accounts, chat history, limits and Library records,
so keep the allow list to machines you trust. Removed machines stay removed until
enrolled again in Settings → Replication; tailnet sharing must be on.

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

With Public access on, **Settings › Sharing** shows the address to copy. The client
discovers OAuth metadata and either registers a public client automatically or, like
Claude.ai and ChatGPT, identifies itself with a Client ID Metadata Document: an HTTPS
URL as `client_id`. The gateway fetches that document (public addresses only, at most
16 KiB, no redirects, cached for an hour) and requires it to name its own URL and
carry no secret. A registration that asks for another `token_endpoint_auth_method`
is accepted as a public client (`none`). Scopes may be any set of `agentpack` and
`offline_access`. A loopback `http` redirect URI matches on any port (RFC 8252).
Each client address may make 120 OAuth requests a minute. Sign in with the gateway
account and approve the connection. Authorization uses PKCE;
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
