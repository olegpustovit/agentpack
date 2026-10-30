# Gateway UI

Static frontend embedded by the Go gateway. `public/` is the only served directory:
`index.html` at `/`, other assets at `/static/`. It uses Agentnet's macOS system font,
OKLCH color and spacing tokens, with light/dark modes and responsive navigation.
Terminal bytes are rendered by locally bundled MIT-licensed xterm.js; the terminal grid stays
at the remote host dimensions while CSS scales the view.
The installable PWA manifest is `/static/manifest.webmanifest`; its SVG and PNG icons are
served from `/static/icon.svg`, `/static/icon-192.png`, and `/static/icon-512.png`. There is
no service worker, so API responses and registry data are never cached by the UI. No
external script, CDN, font, analytics, localStorage credentials, or runtime Node server.

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
  `{id, name?, directory, harness, mode, state: {running,shared_input,responder?,rows,columns,exit_code}}`.
- `GET /api/recent`: array (or `{recent: [...]}`) of `{directory,harness,mode,last_used?,available,unavailable_reason?}`.
  Missing or replaced folders remain visible but cannot start until a fresh local launch.
- `POST /api/sessions`: `{directory,harness,mode}`, returns created session including `id`.
- `PATCH /api/sessions/{id}`: `{name}` renames the session display label and returns the
  updated session. Names are trimmed and limited to 80 characters; an empty name clears
  the label. The label survives gateway restarts without changing the session ID or folder.
- `POST /api/sessions/{id}/stop`: `{}`. Stop is explicit; detach is separate.
- `POST /api/sessions/{id}/restart`: `{operation_id}`. Each user intent gets a UUID;
  transport failures and 5xx retry the same UUID, while success or 4xx ends that intent.
- `GET /api/limits`: `{limits: [...]}` with one snapshot per harness (`codex`, `claude`, `agy`, `grok`, `opencode`) of `{harness,state,source,observed_at,plan?,error?,windows: [{id,label,used_percent?,remaining_percent?,reset_at?,reset_text?}]}`. Results are cached for 60 seconds; a failing harness reports its own `error` and does not hide the others.
- `POST /api/limits/refresh`: `{}`, returns the same shape. Refresh is throttled to one collection per 10 seconds.
- `GET /api/packages?directory=...`: array (or `{packages: [...]}`) of `{module,commit?}`.
- `POST /api/packages`: `{directory,spec,restart?}`. `restart` defaults to `true`.
- `GET /api/library/packages`: registry package records, including exact module/commit,
  cache key, content hash, and package identity where available. `GET /api/library/agentpacks`
  returns saved agentpack identities; `GET /api/library/agentpacks/{id}` returns the identity
  and its revision history. Agentpack revisions expose their manifest, package pins, diff,
  mode, project, and creation metadata. `POST /api/library/agentpacks` captures the selected
  session/project; `PATCH /api/library/agentpacks/{id}` renames its Library label.
- Restore preview is `GET /api/library/agentpacks/{id}/revisions/{revision}/restore-preview?directory=...`;
  apply is `POST /api/library/agentpacks/{id}/revisions/{revision}/restore` with
  `{directory,restart?}`. Restore is limited to one trusted project per confirmation.
- `GET /api/library/drafts` returns locally held pending installs/restores. `POST` accepts a
  `DraftInput` containing the exact package pin or agentpack revision, destination node and
  directory, folder identity, reason, and expected pins. `POST /api/library/drafts/{id}/review`
  with `{}` rechecks destination reachability, authorization, folder identity, pins, and the
  current diff; it never applies changes. Only a ready review can be explicitly applied via
  the regular package install or agentpack restore endpoint. Successful apply or explicit
  discard removes the local draft; `DELETE /api/library/drafts/{id}` discards it.
- `GET /api/library/replication/peers` returns enrolled peers and their verified object
  digests, plus contact results. `POST /api/library/replication/peers` with `{peer}` enrolls
  a stable peer ID; `DELETE /api/library/replication/peers/{id}` removes enrollment;
  `POST /api/library/replication/sync` with `{peer}` syncs verified registry objects.
  Replication is opt-in, requires Tailscale sharing, and both gateways must enroll each other.
- The terminal grid is fixed to the host-reported `state.columns × state.rows`; browser
  resizing only changes its CSS scale.
- `GET /api/sessions/{id}/terminal`: authenticated WebSocket. Binary messages carry raw
  PTY bytes in either direction. Text notices carry `{type,attachment?,state?,offset?,error?}`.
  All live attachments may send input concurrently; each input frame is serialized into the PTY.
  Only the local attachment may resize it with `{action:"resize",rows,columns}`.
  The first attached notice must include attachment ID and current state. After replay bytes,
  the gateway sends `{type:"replay_complete"}`; the UI keeps the last frame dimmed and input
  disabled until replay has rendered and `shared_input` is true. Older workers remain
  view-only until the session is launched again with the updated executable. The `responder`
  attachment answers live terminal queries (local preferred, otherwise first attached browser);
  browser replay never answers queries. Every write validates a live attachment server-side.
  The UI records `performance.mark` entries named `agentpack:terminal:click`, `socketopen`,
  `attached`, `firstframe`, and `replay_complete` and emits matching `console.debug` timing
  entries for local troubleshooting.

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
waits up to 90 seconds, resets the emulator, and enables shared browser input after replay. A replacement that immediately exits still replays its output.
Antigravity desktop launcher presence cannot be launched or controlled remotely;
detached desktop windows are not tracked.

The Library shows each module once; select a version in its inspector when several pins are saved. Sessions is the initial active view. Gateway menu → Appearance settings selects System, Light, or Dark for this browser across views and reloads; the terminal background, default text, cursor, and fallback ANSI palette follow the choice. Explicit RGB colors emitted by agents remain intact.

Terminal attachment shows an accessible loading indicator until replay finishes. A socket that fails to complete replay within 30 seconds is closed and retried with the existing backoff. Session polling preserves the dimmed last frame throughout reconnect. The WebSocket handshake starts before emulator initialization and layout.

Initial session discovery and empty Library fetches show loading states instead of premature empty results. Gateway/browser WebSockets use 15-second pings and a 45-second pong deadline so silently lost connections close and enter the normal reconnect flow.
