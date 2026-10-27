# Gateway UI

Static frontend embedded by the Go gateway. `public/` is the only served directory:
`index.html` at `/`, other assets at `/static/`. It uses Agentnet's macOS system font,
OKLCH color and spacing tokens, with light/dark modes and responsive navigation.
Terminal bytes are rendered by the locally bundled MIT-licensed xterm.js and fit addon.
No external script, CDN, font, analytics, localStorage credentials, or runtime Node server.

Build with `npm ci && npm run build`; commit the generated public assets. Run browser
checks with `npx playwright install chromium && npm test`. Node/npm are build tools only.
The gateway should set a same-origin CSP (scripts/styles from self, WebSocket connect
from self; xterm dynamically sets style attributes) and disable HTTP caching for APIs.

## API contract

All requests use same-origin session cookies. Mutations send JSON and require origin
validation. Non-2xx errors may be `{ "error": "message" }` or plain text. Credentials and
private worker capabilities never appear in any response. An unauthenticated API returns
401. Logout invalidates the server session and active terminal access.

- `GET /api/status`: `{setup_required, authenticated}`.
- `POST /api/setup`: `{password, token}`. The token is obtained on the server with
  `agentpack gateway setup`. Root can be created once. Minimum password length 12.
- `POST /api/login`: `{password}`; `POST /api/logout`: `{}`.
- `GET /api/sessions`: array (or `{sessions: [...]}`) of
  `{id, directory, harness, mode, node_name?, state: {running,owner,epoch,rows,columns,exit_code}}`.
- `GET /api/recent`: array (or `{recent: [...]}`) of `{directory,harness,mode,last_used?,available,unavailable_reason?}`.
  Missing or replaced folders remain visible but cannot start until a fresh local launch.
- `POST /api/sessions`: `{directory,harness,mode}`, returns created session including `id`.
- `POST /api/sessions/{id}/stop`: `{}`. Stop is explicit; detach is separate.
- `POST /api/sessions/{id}/restart`: `{operation_id}`. Each user intent gets a UUID;
  transport failures and 5xx retry the same UUID, while success or 4xx ends that intent.
- `GET /api/packages?directory=...`: array (or `{packages: [...]}`) of `{module,commit?}`.
- `POST /api/packages`: `{directory,spec}`. Returns after install and affected-agent restart.
- `GET /api/sessions/{id}/terminal`: authenticated WebSocket. Binary messages carry raw
  PTY bytes in either direction. Text notices carry `{type,attachment?,state?,offset?,error?}`.
  Browser control messages are `{action:"take"|"release"|"resize",epoch,rows?,columns?}`.
  The first attached notice must include attachment ID and current state. State notices
  update ownership and epoch; ownership checks happen server-side for every mutation.

Remote nodes use Tailscale identity and require sharing enabled on both sides. The machine selector lists live allowed peers from `GET /api/nodes`. Requests and
WebSocket connections for the selected peer append `?node=<stable-id>`. Start-folder
paths refer to that machine. No fabricated nodes are displayed.
Repository cloning is intentionally omitted. Recent-folder start creates a fresh session.

Native-stdio sessions set `state.interactive=false`; their metadata and stop action
remain available, while terminal/restart controls are disabled. Login may redirect
only to a same-origin `/oauth/authorize` return URL for MCP authorization.

Gateway settings use `GET /api/settings` (`running`, `platform`, `sharing_enabled`,
`allowed_peers`) and `POST /api/settings/sharing` (`{enabled}`). Settings applies to the
gateway machine even when another machine is selected for sessions. Stop-all and
stop-gateway call `/api/node/stop-agents` and `/api/node/stop`. Forget sends
`DELETE /api/recent` with `{directory,harness,mode}` to the selected node.
A terminal `ended` notice with `state.restarting=true` triggers bounded reconnect,
waits up to 90 seconds, resets the emulator, and keeps browser input as an observer
until explicit handoff. A replacement that immediately exits still replays its output.
Antigravity desktop launcher presence cannot be launched or controlled remotely;
detached desktop windows are not tracked.
