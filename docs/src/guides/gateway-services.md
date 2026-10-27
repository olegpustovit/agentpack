# Gateway user services

Normal agent launches start a local node automatically. To have the gateway available
as a user service after login, run these commands from Bash, Zsh, or PowerShell:

```text
agentpack service install
agentpack service status
agentpack service stop
agentpack service start
agentpack service uninstall
```

Install captures the executable's current absolute path and `AGENTPACK_HOME`. Keep the
binary there, or run install again after moving it. No command changes your shell
profile, PATH, or requires a root account. The service runs as your current OS user.

- Linux uses `systemctl --user` and an `agentpack-gateway.service` unit under
  `$XDG_CONFIG_HOME/systemd/user` (default `~/.config/systemd/user`). It starts at user
  login; this command does not enable system-wide lingering. `KillMode=process` keeps
  separately owned session workers alive when stopping the gateway.
- macOS uses a LaunchAgent at `~/Library/LaunchAgents/space.microapps.agentpack.gateway.plist`.
  It starts at login and preserves independent workers with `AbandonProcessGroup`.
- Windows uses a Scheduled Task named `Agentpack Gateway-<your SID>`, with interactive
  login and limited privileges. PowerShell starts the gateway through WMI outside the
  scheduled task's job, so task lifetime does not kill sessions. The task checks at login
  and every minute while you are logged in. It stores no password. `service status`
  shows both task state and actual gateway availability; the bootstrap task normally
  returns to Ready while the gateway keeps running.

Stopping disables automatic node startup until an explicit `service start` or
`gateway start`. Existing agent sessions continue. `gateway stop-agents` is the separate
command to end them. Uninstall removes only the service definition; packages, recent
folders, root-account credentials, and session workers remain.

Service definitions invoke `agentpack service run`, an internal entry point that checks
the persistent stop marker before serving. Linux service logs are in the user journal;
macOS and Windows logs are in `$AGENTPACK_HOME/network/gateway-service.log`.

## Personal Tailscale sharing

Enable sharing once on each machine you want to access:

```text
agentpack gateway sharing on
agentpack gateway sharing status
```

The gateway listens only on its Tailscale address, port 7421. It discovers online peers
using the local Tailscale daemon and accepts your own untagged Tailscale identity by
default. Incoming connections are authenticated using `tailscale whois` against their
actual source address. Tailscale ACLs still apply.

Other accounts and tagged servers need explicit permission on each receiving machine:

```text
agentpack gateway sharing allow nYourStableNodeID
agentpack gateway sharing allow tag:my-server
agentpack gateway sharing revoke tag:my-server
agentpack gateway sharing off
```

Use stable node IDs from `tailscale status --json`; hostnames and arbitrary IP addresses
are not accepted as remote targets. A tagged public gateway must explicitly allow its
personal machines, and those machines must allow that gateway's node ID or tag.

Sharing off closes tailnet access and browser attachments within five seconds while
local CLI sessions continue. The public gateway's machine selector shows actual allowed,
reachable Agentpack nodes. Folder paths and package installs apply to the selected
machine. The public browser never receives private node credentials.
