# Gateway UI

Static frontend embedded by the Go gateway. The Vite build in `ui/dist` is staged into
`internal/network/webdist` by `make ui-stage` and served at `/`. It uses Agentnet's macOS system font,
OKLCH color and spacing tokens, with light/dark modes and responsive navigation.
Terminal bytes are rendered by locally bundled MIT-licensed xterm.js; the terminal grid stays
at the remote host dimensions while fit changes the font size. It never uses a CSS transform.
The installable PWA manifest is `/manifest.webmanifest`; its SVG and PNG icons are
served from `/icon.svg`, `/icon-192.png`, and `/icon-512.png`. There is
no service worker, so API responses and registry data are never cached by the UI. No
external script, CDN, font, analytics, localStorage credentials, or runtime Node server.

Build with `bun install --frozen-lockfile && bun run build`; `make ui-stage` publishes it.
Install browsers with `bunx playwright install chromium webkit`, then run `make ui-e2e` from the repository root. This also prepares the Go test fixture that verifies browser errors reach SQLite. Direct `bun run e2e` skips that integration case unless `AGENTPACK_TELEMETRY_TEST_BINARY` points to the compiled fixture. The test budgets are in the repository `AGENTS.md`.
The gateway should set a same-origin CSP (scripts/styles from self, WebSocket connect
from self; xterm dynamically sets style attributes) and disable HTTP caching for APIs.

## Bundle sizes and regression budgets

`bun run build` runs `bun run size` (`scripts/check-size.ts`), which fails over budget. Sizes are gzip bytes from `make artifact` on this branch. "Before" is the plan's measurement of the old single-file UI (`app.js`); the plan gives no before value for the CSS or the chunks.

| Bundle | Before | After | Budget |
|---|---|---|---|
| Initial JS (entry and preloaded chunks) | 97.7 KB, one `app.js` | 156,226 B | 170 KB |
| Initial CSS | — | 12,094 B | 30 KB |
| Terminal chunk (xterm, `terminal-pane-*.js`) | — | 98,642 B | 100 KB |
| WebGL addon chunk (`addon-webgl-*.js`) | — | 30,984 B | 100 KB |

Regression budgets are enforced by `tests/budgets.spec.ts` on the same fixture as the audit (3 machines, 4 projects, 3 harnesses per project). Measured values come from the T7.1 and CloseGaps reports on that fixture. The 24 px and 90,30 "before" values were not reproduced in the current app; the tests measure the stated after values directly.

| Budget | Before | Measured | Required |
|---|---|---|---|
| Idle Sessions requests per 60 s, events connected | 92 | 3 | ≤ 6 |
| Idle Library requests per 30 s, events connected | 146 | 0 | ≤ 3 |
| Sidebar node changes per 60 s, unchanged data | 960 | 0 | 0 |
| Return to Library within staleTime: requests | 75 | 0 | 0 |
| Return to Library within staleTime: loader | shown | not shown | not shown |
| First Library open requests | 56 | 9 | ≤ 9 |
| Install rows for the fixture | 36 | 12 | 12 |
| New Session folder options per machine | 12 | 4 | 4 |
| Peer answering after several polls appears | never | appears | appears |
| Sockets when returning to a recent session | 4 | 0 new | 0 new |
| Click at cell 61,21 at any fit | reports 90,30 | reports 61,21 | 61,21 |
| Content height after a notice | 24 px | 570 px before and after | unchanged |

## API contract

Machine inventory and topology include the replicated Main role. Archived chats
are permanently read-only and fetch history through Main; an unavailable archive
names that machine in a banner. Archived sessions expose no resume, fork,
composer or permission-answer controls.

All requests use same-origin session cookies. Mutations send JSON and require origin
validation. Non-2xx errors may be `{ "error": "message" }` or plain text. Credentials and
private worker capabilities never appear in any response. An unauthenticated API returns
401 once claimed; an unclaimed gateway refuses protected APIs with 403 `unclaimed`.
Logout invalidates the server session and active terminal access.

- `GET /api/status`: `{claimed, legacy, authenticated, email}`.
- `POST /api/claim`: `{email, password, current_password?}`. The first owner claims
  once; a legacy gateway requires its current password and preserves it.
- `POST /api/login`: `{email,password}`; `POST /api/logout`: `{}`.
- `POST /api/logout-all`: revokes all browser and OAuth logins for that account.
  Cookies survive restarts and slide for thirty days. HTTPS uses a Secure,
  HttpOnly, SameSite=Strict `__Host-agentpack_session` cookie.
- `GET /api/identity`: `{node: {id,name,os,version}, auth, tailscale_user}` for the selected machine.
- `GET /api/nodes`: array of `{id,name,online}` for allowed Tailscale peers.
- `GET /api/topology`: `{self: {id,name,os,relay}, peers: [{id,name,os,state,connection,relay,rtt_ms?,last_seen?,sessions}]}`.
- `GET /api/projects`: array of project rows `{directory,name,folder_id,available,unavailable_reason,last_used,launches,packages,mcp,error}`.
- `GET /api/events`: Server-Sent Events. Each `message` carries one JSON event that the UI uses to refresh cached queries; the UI closes the stream while the page is hidden.
- `GET /api/library/agentpacks?include=revisions`: array of `{agentpack, revisions}`, one call instead of one request per saved agentpack.
- `GET /api/sessions`: array (or `{sessions: [...]}`) of
  `{id, name?, directory, harness, mode, transport?, conversation_state?, agent_session_id?, state: {running,shared_input,responder?,rows,columns,exit_code}}`.
  Omitted `transport` means `terminal`. Chat sessions use `transport: "acp"`; `conversation_state` is `working`, `needs_you`, or `idle` while the worker is running.
- `GET /api/recent`: array (or `{recent: [...]}`) of `{directory,harness,mode,last_used?,available,unavailable_reason?}`.
  Missing or replaced folders remain visible but cannot start until a fresh local launch.
- `POST /api/sessions`: `{directory,harness,mode,transport?,agent_session_id?,resume?}`, returns created session including `id`.
  `transport` is `terminal` (default) or `acp` (chat). Resuming an agent conversation passes `agent_session_id` from `GET /api/agent-sessions`.
- `GET /api/agent-sessions`: `{sessions: [{id,title?,updated_at?}]}` for the selected folder, harness, and mode (cached; requires a live worker probe).
- Chat worker routes (also available on the gateway as `/api/sessions/{id}/conversation/...` with the same auth and `?node=` routing as other session APIs):
  - `GET /conversation?since=` — NDJSON log; `Accept: text/event-stream` or `?stream=1` for SSE live updates. The gateway falls back to saved SQLite history for unavailable workers. The same `since` filter and truncation header apply; stopped SSE replay is finite.
  - `POST /conversation/prompt` — `{text, image?}` with `{sha,mime,width?,height?}` after `POST /conversation/blobs`; the server derives MIME from stored bytes and ignores the client MIME.
  - `POST /conversation/permission` — `{request_id, option_id}`.
  - `POST /conversation/elicitation` — `{elicitation_id, value}`.
  - `POST /conversation/cancel`, `POST /conversation/config` — `{key, value}`.
  - `GET /conversation/config-options` — agent `configOptions` snapshot.
  - `GET /blobs/{sha}` — session-scoped PNG/JPEG/GIF/WebP bytes, 20 MiB each and 100 MiB total or 128 images per session (whichever limit is reached first).

Only the recorded user and stable peer node identity (or the same local user/host) may mutate an existing ACP session. Read-only peers retain conversation, config-option, and blob reads.

- `PATCH /api/sessions/{id}`: `{name}` renames the session display label and returns the
  updated session. Names are trimmed and limited to 80 characters; an empty name clears
  the label. The label survives gateway restarts without changing the session ID or folder.
- `POST /api/sessions/{id}/stop`: `{}`. Stop is explicit; detach is separate.
- `POST /api/sessions/{id}/restart`: `{operation_id}`. Each user intent gets a UUID;
  transport failures and 5xx retry the same UUID, while success or 4xx ends that intent.
- `GET /api/limits`: `{limits: [...]}` with one snapshot per harness (`codex`, `claude`, `agy`, `grok`, `omp`, `opencode`, `cursor`) of `{harness,state,source,observed_at,plan?,error?,windows: [{id,label,used_percent?,remaining_percent?,reset_at?,reset_text?}]}`. Reads query local replicated snapshots across nodes, with `node_id`, `reachable` and `last_sync`. `?node=<stable-id>` filters replicated reads; refreshes still route to that machine. The Limits view (`/usage/limits`) uses one local query, shows a usable report first for each provider, and keeps other machine reports expandable without combining account allowances. Unknown percentages stay unknown. Results are cached for 60 seconds; a failing harness reports its own `error` and does not hide the others.
- `POST /api/limits/refresh`: `{}`, returns the same shape. Refresh is throttled to one collection per 10 seconds. While limits are on screen the UI sends this once to each online peer (at most every five minutes per tab); gateways also re-read an agent's limits after its turns, at most every five minutes per agent.
- `GET /api/usage?from&to`: Unix seconds, at most 100 days apart. `{hourly, sessions, samples}`: `usage_hourly` rows that start in the range, sessions (with lifetime counters; `cost_micros` -1 when the agent reports none) active in it, and `limit_samples` for the burn line. Local replicated reads only. The Usage view buckets the hours into the viewer's local days.
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
  discard removes the shared draft; `DELETE /api/library/drafts/{id}` discards it.
- `GET /api/library/replication/peers` returns enrolled peers, their mesh approval
  and contact results. `POST /api/library/replication/peers` with `{peer}` enrolls
  a stable peer ID; `DELETE /api/library/replication/peers/{id}` removes enrollment;
  `POST /api/library/replication/sync` with `{peer}` pulls bounded shared-state pages.
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
`DELETE /api/recent` with `{directory}` to the machine that owns the row.
A terminal `ended` notice with `state.restarting=true` triggers bounded reconnect,
waits up to 90 seconds, resets the emulator, and enables shared browser input after replay. A replacement that immediately exits still replays its output.
Antigravity desktop launcher presence cannot be launched or controlled remotely;
detached desktop windows are not tracked.

Settings → Diagnostics loads lazily. `GET /api/logs/issues?status=open|resolved|ignored`
lists issues (`status=` includes all), and `GET /api/logs/issues/{fingerprint}`
returns machine counts, the sample event and recent trace IDs. Resolve sends
`POST /api/logs/issues/{fingerprint}/resolve` with `{version,note}`; reopen/ignore
use the corresponding path suffix. `GET /api/logs/traces/{trace}` reads the full
retained trace. These routes use the same gateway login/origin checks as Settings.
The inline resolution form requires the fix version and reports success through
the Hud. See [Diagnostics](../docs/src/guides/diagnostics.md) for recording and
regression rules.

The Library shows each module once; select a version in its inspector when several pins are saved. Sessions is the initial active view. Gateway menu → Appearance settings selects System, Light, or Dark for this browser across views and reloads; the terminal background, default text, cursor, and fallback ANSI palette follow the choice. Explicit RGB colors emitted by agents remain intact.

Terminal attachment shows an accessible loading indicator until replay finishes. A socket that fails to complete replay within 30 seconds is closed and retried with the existing backoff. Session polling preserves the dimmed last frame throughout reconnect. The WebSocket handshake starts before emulator initialization and layout.

Initial session discovery and empty Library fetches show loading states instead of premature empty results. Gateway/browser WebSockets use 15-second pings and a 45-second pong deadline so silently lost connections close and enter the normal reconnect flow.

Loading states name the current operation. Sessions and limits query the local replicated store once, including offline machines after reload. Offline chats retain history with an Offline, read-only banner and no composer; live request-answer controls stay disabled. Limits preserves prior reports during refresh. Library and Machines expose discovery/inventory progress; initial session errors are distinct from an empty list. Startup, recent folders, package reads, pending installs, and settings have loading/error states. Settings failures expose a retry action. Buttons for asynchronous actions show busy state and prevent duplicate clicks. Cursor reads its CLI login store (including the macOS keychain) and calls the read-only DashboardService usage RPC; its included, Auto, API, and on-demand percentages stay separate.
