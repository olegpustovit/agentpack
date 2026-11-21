# Gateway user services

Run `ap` in an interactive terminal to start the gateway and open its browser UI.
Use these commands to control the gateway from a terminal or script:

```text
ap gateway start
ap gateway status
ap gateway stop
```

Both start commands install or refresh the login service for the current OS
user. The service uses the current executable and `AGENTPACK_HOME`. When a
service manager is unavailable, the gateway runs in a detached process and
starts again the next time you run `ap` after reboot.

- Linux uses `systemctl --user` and writes the unit under
  `$XDG_CONFIG_HOME/systemd/user` (default `~/.config/systemd/user`).
  Agentpack tries to enable lingering so the gateway starts at boot and
  survives logout. If it fails, it prints the `loginctl enable-linger` command.
- macOS uses a LaunchAgent at
  `~/Library/LaunchAgents/space.microapps.agentpack.gateway.plist`.
- Windows uses an interactive Scheduled Task named `Agentpack Gateway-<your SID>`.
  The task checks at login and every minute while you are logged in.

`ap gateway stop` disables login startup and leaves existing sessions running.
Use `ap gateway stop-agents` to end those sessions. Linux service logs are in the
user journal. macOS and Windows service logs are in
`$AGENTPACK_HOME/network/gateway-service.log`.

## Update the mesh

The machine list can show **Needs update** while a machine is reachable.
Topology shows the last known version and indicates where the state schema
differs from the local gateway. Use **Update** on a machine or **Update all**
in Topology to bring older machines up to the newest version in the mesh.
The gateway selects the newer of that mesh version and the latest stable
GitHub release. It refuses updates to an equal or older version.

Use `make ship-patch` for a stable patch or `make ship-dev` to publish a
development build with pinned installers and checksums. A development build
can participate in mesh updates as soon as its release assets are published.
For a machine whose gateway predates remote updates, run the release's pinned
installer on that machine once, then run `ap gateway start`.

## Personal Tailscale sharing

Sharing is on by default, like Agentnet discovery: every machine with Tailscale running
appears in the gateway's machine selector without any setup. Turn it off or check it with:

```text
agentpack gateway sharing off
agentpack gateway sharing status
```

The gateway listens only on its Tailscale address, port 7421. It discovers online peers
using the local Tailscale daemon and accepts your own untagged Tailscale identity by
default. Incoming connections are authenticated using Tailscale WhoIs against their
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
