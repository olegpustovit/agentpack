# Agentnet integration proposal

[Open the private proposal and interactive prototype](https://artifacts.microapps.space/a/qrj2b67pxmxq).

Source: [index.html](index.html). Open it directly in a browser. It contains the architecture, migration map, proposed commands, security boundaries, platform constraints, and release gates. All machines and terminal output are explicitly simulated; it never contacts a node or installs a package.

```sh
node docs/proposals/agentnet/check.cjs
```

Confirmed product decisions:

- Every normal agent launch automatically starts or joins the local node.
- Package installation automatically restarts sessions whose effective environment changed.
- Stopping sharing keeps existing CLI sessions running. Stopping agents is a separate action.
- Access defaults to the personal Tailscale identity plus explicitly permitted servers.
- Open existing sessions; remember recent folders for fresh starts. Repository cloning is deferred.
- Provide `agentpack deploy` for `oracle-vm`, serving the public gateway at `https://agentpack.microapps.space`, with one-time application administrator setup.
- Expose authenticated MCP for ChatGPT, Grok and other clients, following the OAuth/Streamable HTTP integration in `../mcp-icloud-email`.
- Reuse Agentnet's UI components and macOS-style design tokens consistently across the terminal, package manager, login/setup, and administration screens.
- Support Windows and PowerShell, including real ConPTY sharing and setup/deployment commands.

Inspected starting points: Agentpack `71facfd`, sibling Agentnet `71e9fca`. Agentnet's ordinary launches inherit stdio, its web interface consumes diagnostics, and detached PTYs write to logs. Live browser terminal multiplexing is not implemented there yet.

The implementation is in the working tree: owned PTY/ConPTY workers,
automatic node participation, native-stdio presence, authenticated browser access,
restart supervision, recent-folder APIs, locally bundled xterm.js, shared Agentnet macOS
tokens, Tailscale discovery/WhoIs policy, explicit user services, Oracle deployment, and
OAuth-protected Streamable HTTP MCP. The prototype remains simulated; the production
UI is under `ui/`.

Evidence recorded so far: network race tests and compiled CLI PTY integration passed,
including gateway shutdown/restart and final agent exit. A real browser against a live
gateway passed setup, Unicode terminal replay, input, ownership, restart reconnection,
and session survival after gateway stop. Two real scratch gateways on the development
host and `oracle-vm` passed personal-identity discovery, browser machine selection,
remote terminal input/output, sharing revocation closing WebSockets, and remote gateway
stop preserving the worker. These checks did not deploy the public gateway.

All 26 desktop/phone browser cases passed (24 in the full run and two recent-folder
cases in a focused rerun after correcting their ISO timestamp assertion). They cover
authentication, raw WebSocket input, ownership,
detachment, XSS text, machine selection, native metadata, restart reconnection including
failed replacements, idempotent restart retries, last-used timestamps, missing/replaced
folder availability, Settings and Forget. Service-template and mocked native lifecycle
checks passed. Windows executable and network-test cross-compilation passed; these are
not Windows runtime checks. A real transient `systemd --user` unit with
`KillMode=process` passed: a fresh session launched inside the gateway service cgroup
retained its worker and terminal input/output after the unit stopped. The isolated unit
and scratch directories were cleaned up. This verifies stop survival; permanent
service install/start/uninstall remains covered by mocked lifecycle checks.

The complete Go suite, `go vet ./...`, and affected race tests (network, CLI and sync)
passed. Regression coverage includes cross-process package edits, retry-safe restarts,
fast-launch fingerprint caching, reconnect reconciliation and replaced-workspace
rejection before stopping an agent. Linux, macOS ARM64 and Windows AMD64 executables
build; Windows network and CLI test binaries also compile. On `kyiv-win` (Windows NT 10.0.26200.0, Windows PowerShell 5.1.26100.9444),
all 25 top-level network tests and eight focused CLI tests passed. PowerShell 7 was
not installed, so that subtest skipped. Native checks cover ConPTY Unicode/ANSI,
resize, input ownership, deadlines, forced descendant cleanup, graceful wrapper
cleanup, console restoration, private ACLs, and OAuth/MCP. A unique limited-interactive Scheduled Task
using the production WMI bootstrap started a real gateway and a fake console agent
through the gateway API. The agent survived both task stop and authenticated gateway
stop, then accepted and echoed terminal input. The task was removed and the worker
reaped. Remote cleanup confirmed zero remaining test tasks or processes and removal
of the scratch directory. Local evidence is under `/tmp/agentpack-native-windows/`. Native macOS runtime, PowerShell 7 and real MCP client
connections remain unverified. Do not infer runtime support from cross-compilation
or from this simulated artifact.

Initial read-only deployment discovery: SSH to `oracle-vm` works; it runs Linux ARM64 as `snowbear`, with Caddy and `smdctl` installed. No remote state was changed. Caddy already serves other microapps with the existing certificate files; no Agentpack virtual host was found and the VM's DNS resolver returned no address for `agentpack.microapps.space`. Revalidate DNS and the existing reverse-proxy/service arrangement before deploying; never overwrite unrelated Caddy sites or private environment files. The email MCP project implements OAuth metadata, PKCE, dynamic client registration, consent, refresh, revocation and the official MCP SDK; reuse those semantics without importing mailbox-specific dependencies.

The artifact ID is `qrj2b67pxmxq`; publish subsequent versions to that ID rather than creating another artifact. This directory is the design deliverable and implementation checkpoint. Do not publish a deployment claim until the live deployment has been verified.

## Production deployment — 2026-10-27

The gateway is deployed on `oracle-vm` at `https://agentpack.microapps.space`, with the backend listening on `127.0.0.1:7420`. The SSH user's persistent systemd service is enabled; deployment preserves the existing Caddy configuration. The obsolete Agentnet daemon was stopped after confirming it had no live sessions. Public status returns `setup_required: true`; the administrator must retrieve the private token over SSH and choose a password. The full Go suite, vet, deployment rollback tests and all 26 desktop/mobile browser checks passed for this release candidate.
