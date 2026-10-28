Reference: https://artifacts.microapps.space/a/a88mxr1gd37z, version 9 (retrieved 2026-10-28).

# PLAN: implement the Agentpack desktop workspace in the gateway

Audience: an implementing agent (Codex) working in the `agentpack` repository.
Design reference: this artifact. `index.html` is the runnable prototype. `src/` holds its readable source (`shell.html`, `style.css`, `app.js`, `notes-sections.html`). `DESIGN.md` and `.impeccable/design.json` hold the tokens and components, and `DIRECTION.md` holds the layout and flows. All prototype data is synthetic. Treat the prototype as the visual and behavioral spec, not as code to paste wholesale.

Read `AGENTS.md` first and follow it. In particular:
- pre-release, so no backwards compatibility;
- shared code lives in its subsystem, with no tiny single-purpose packages;
- prefer the standard library and existing dependencies;
- the gateway UI ships no CDN, no external fonts and no runtime Node server.

## Where things live today

| Concern | Current location |
|---|---|
| Frontend source | `internal/network/web/app.js`, bundled by `web/build.mjs` (esbuild) into `web/public/app.js` |
| Markup, styles | `internal/network/web/public/index.html`, `public/style.css` (embedded by `internal/network/assets.go`) |
| Browser tests | `internal/network/web/tests/gateway.spec.js` (Playwright, `npm test`) |
| HTTP API | `internal/network/gateway.go` (`Handler()`), contract in `internal/network/web/README.md` |
| Peer routing | `internal/network/discovery.go`: `peerRoute()` allowlist, `?node=<tailnet id>` proxies to that machine |
| Machines | `GET /api/nodes` → `[{id,name,online}]` (`TailnetNode`) |
| Sessions | `GET /api/sessions` → `SessionInfo{id,directory,harness,mode,state{running,owner,epoch,rows,columns,exit_code,…}}` |
| Trusted folders | `GET /api/recent` → `{directory,harness,mode,last_used,available,unavailable_reason}` |
| Packages in a folder | `GET /api/packages?directory=` → `[{module,commit?}]` |
| Install | `POST /api/packages {directory,spec}`: installs, then restarts affected agents, synchronously |
| Terminal | `GET /api/sessions/{id}/terminal` WebSocket: binary PTY bytes and text notices; control `{action:take/release/resize,epoch}` |
| Lifecycle | `POST /api/sessions/{id}/stop`, `/restart {operation_id}` |

Everything above can be reached on another machine by adding `?node=<id>`.

## Phase 1: window, tokens and sessions (frontend only, no API changes)

Goal: the gateway looks and behaves like the prototype's Sessions view, on real data.

1. **Tokens.** Replace `public/style.css` with the prototype's token block and component rules from `src/style.css`. Keep the token names: `--fs*`, `--bar-h`, `--status-h`, `--ctl-h`, `--row-h`, `--row-h-2`, `--s1…s7`, `--r-*`, the surface and label colors, the light and dark sets, and the `@media(max-width:760px)` pane switching. Drop the rules for the prototype-only Simulate popover.
2. **Window.** Build the window from `src/shell.html`:
   - one 40px toolbar (view switcher, title and subtitle, view tools, notes button);
   - the left sessions sidebar (`--sidebar-w`, resizable 220–420px, collapsible);
   - the terminal column;
   - one 24px status bar;
   - no page scroll; each pane scrolls on its own.
3. **Sessions sidebar.**
   - Group by machine → project → session, with a Project-first toggle.
   - Data sources:
     - machines: `GET /api/nodes` plus the local node;
     - sessions per machine: `GET /api/sessions?node=`;
     - projects: `directory` basename, with the full path in the tooltip.
   - Show each machine's own results as soon as they arrive. Never block on the slowest peer.
   - Offline machines keep their last-known rows, dimmed with the hollow-dot style.
   - Arrow keys move the selection. Selection follows focus (accent fill when the list is focused).
4. **Terminal.**
   - Render at the host grid (`state.columns × state.rows`).
   - Fit is a CSS `transform: scale()` of the whole xterm screen, exactly as in `scaleTerminal()` in `src/app.js`. Also keep 1:1 and ± zoom, and a 70% legibility floor on narrow screens that switches to scrolling.
   - Browser resizing never sends `{action:"resize"}`. Today `app.js` calls `state.fit.fit()` and then `sendControl("resize", …)`. Remove that auto-resize path, and drop `addon-fit` if nothing else uses it. The resize action belongs to the local terminal.
   - The status bar shows live/connecting/offline/stopped, the grid size and shared-input availability.
5. **Control and lifecycle.**
   - Local and browser input share the PTY. Replay must finish rendering before input is enabled. Older session workers remain view-only until a fresh launch.
   - The ⋯ menu holds Rename…, Restart… (existing `operation_id` retry semantics), Stop…, Install package…, and Save agentpack…. Disable the last two until phase 3, with a tooltip.
6. **New session sheet.** `POST /api/sessions?node= {directory,harness,mode}`. The Project list is that node's `available` recent folders, and offline machines are disabled.
7. **Tests.**
   - Extend `web/tests/gateway.spec.js`, mirroring the prototype checks in `src/verify.mjs` and `src/split.mjs`. Cover:
     - the grid stays fixed across window resizes and Fit/1:1/zoom;
     - the sidebar is left, resizes and collapses;
     - arrow-key selection;
     - shared input, replay query suppression, and repeated selection without reattachment;
     - phone pane switching at 390px.
   - Run in Chromium and WebKit (`npx playwright install webkit`).

Done when: the UI matches the prototype screenshots at 1440, 1024 and 390, in light and dark, and the existing gateway tests still pass.

## Phase 2: install flow on the existing endpoints

Goal: the two install sheets work against real machines. Library data comes later, so this phase installs from a module spec.

1. **Destination states.** For each online node, take its `available` recent folders. For each folder, `GET /api/packages?node=&directory=` returns its `{module,commit}` pins.
   - Installed: same module and same commit.
   - Update `old → new`: same module, different commit.
   - Not installed: otherwise.
   - Fetch concurrently and paint each row as it resolves.
2. **"Install into …" sheet** (session ⋯ menu, machine + button). Show the project picker and a package list. Until phase 3, the list is the union of modules seen across all reachable folders, de-duplicated by module and showing the newest commit seen.
3. **Package → destinations sheet** (Library "Install…" and the Update buttons). Group every machine → project row with its state, with "Select all online" and the counted primary label (`Install in N projects`, `Install N · Draft M`).
4. **Apply.** Send one `POST /api/packages?node= {directory, spec:"<module>@<commit>"}` per selected row, with at most 2 in flight per node. Each row shows a spinner, then ✓ Installed or the error text.
5. **Restart choice.** The endpoint always restarts affected agents. Add an optional `restart` boolean (default `true`) to `POST /api/packages` in `gateway.go` and pass it through `GatewayOptions.Install`. Wire the "Restart N affected agents" checkbox to it. Add a Go test for both values.
6. **Offline machines.** Until phase 4, rows on offline machines are disabled, with "Offline · install when it returns". Do not fake drafts.

Done when: Playwright covers the following with the API stubbed through `page.route` and `page.routeWebSocket`, as `tests/gateway.spec.js` already does:
- multi-select across nodes;
- disabled already-installed rows;
- the Update path;
- per-row failure display;
- the `restart:false` request body.

The Go test covers the `restart` flag.

## Phase 3: Library and named agentpacks (new backend)

Goal: the Library view and Machines matrix on real, persisted data.

1. **Storage.**
   - Location: `$AGENTPACK_HOME/registry/`. Do not create a second home.
   - Code: implement it as a shared subsystem package (e.g. `internal/registry`). It has real structure (objects, index, revisions), so it isn't a tiny package.
   - Immutable objects addressed by SHA-256:
     - **Package record:** module, exact commit, `content_hash`, kind, display name, provenance node and project.
     - **Agentpack:** a stable ID, a mutable display name, and a list of revisions. Each revision holds parent IDs, the normalized `agentpack.toml` and `pack.lock`, the source node and project, a timestamp, a label and the mode.
   - A small rebuildable JSON index lists agentpacks and packages.
   - Writes are atomic (temp file + rename).
2. **Redaction before saving.**
   - Strip MCP `env` and `headers` values, tokens, and anything credential-shaped into machine-local bindings.
   - Mark an incomplete snapshot "Needs local configuration".
   - Raw values must never enter objects, hashes or logs.
   - Test this explicitly.
3. **Capture.** On successful `lock`/`add`/`remove`/`update`/`sync`, append a revision when the normalized content changed, if capture is enabled for that project. Also capture on an explicit Save from the UI.
4. **API** (add routes to `gateway.go`, and add the read and save routes to `peerRoute()` where per-node):
   - `GET /api/library/packages`, `GET /api/library/agentpacks`, `GET /api/library/agentpacks/{id}`;
   - `POST /api/library/agentpacks {directory,name?,agentpack_id?,label?,auto_capture}`, which saves a revision;
   - `PATCH /api/library/agentpacks/{id} {name}`;
   - `GET /api/library/agentpacks/{id}/revisions/{rev}/restore-preview?directory=` → `{add:[],update:[],remove:[],mode}`;
   - `POST /api/library/agentpacks/{id}/revisions/{rev}/restore {directory,restart}`. This backs up the manifest and lock, applies atomically through the existing install pipeline, and creates a new revision whose parent is the restored one.
5. **UI.**
   - Library list and inspector per `src/app.js` `renderLibrary()`/`renderDetail()`:
     - a full-width "Installed in" grid built from phase-2 data;
     - history rows with the diff against the previous revision;
     - an `agentpack.toml` disclosure;
     - rename and name suggestion.
   - Machines view: a narrow machine list plus the project × package matrix. ↑ marks an older pin and opens the update sheet.
   - Enable "Save agentpack…" in the session menu.
   - The restore sheet is single-select and shows the preview diff.

Done when:
- Go tests cover the object round-trip, redaction, revision parentage, restore preview and diff, and atomic restore with rollback on failure;
- Playwright covers save → appears in Library → rename → restore preview.

## Phase 4: replication and offline drafts

1. **Replication.**
   - Enrollment is an explicit opt-in per peer, reusing Tailscale identity and the existing sharing allowlist (`ReadSharingConfig`).
   - On contact, peers exchange inventories (object digests) and fetch missing objects by digest. Each object is verified before an atomic publish. A peer acknowledges only after a durable write, and records replica receipts.
   - Deletions are tombstones, and garbage collection waits for every acknowledgment.
   - Never replicate credentials, environment values, MCP headers, terminal output or arbitrary project files.
2. **Drafts.**
   - A draft is persisted in the local registry as `{item, revision?, node, directory, reason}`.
   - When the node returns, re-check reachability, folder identity, pins and the diff, then show "Review…". Never auto-apply.
   - If package bytes exist only on offline holders, keep the draft and name the machine that's needed. Never substitute a different version.
3. **UI.** Draft rows are enabled in both install sheets. The Library list shows "Pending installs" with Review/Discard. "Verified copies" and the Copy column come from replica receipts.

Done when there are Go tests for:
- inventory exchange;
- digest mismatch rejection;
- tombstone propagation;
- concurrent revisions producing two heads ("Two versions to review");
- draft revalidation.

## Phase 5: PWA and polish

1. **PWA.**
   - Serve `manifest.webmanifest` and `icon.svg` (copy them from this artifact) under `/static/`.
   - Link them from `index.html` along with `theme-color`, and keep the `display-mode: window-controls-overlay` CSS.
   - A service worker is not required for install. Do not cache API responses.
   - Keep the same-origin CSP from `web/README.md`, and allow `manifest-src 'self'`.
2. **Host colors (optional, proposal in notes).** Probe the host's default foreground and background with a bounded OSC 10/11 query, intercept the replies, and fall back with the label "Fallback colors". A server-side screen snapshot plus a replay watermark replaces raw-history replay for faithful reconnects.
3. **Connection timing.**
   - Track click → socket open → attached → first frame → replay complete.
   - Cancel stale attaches when the selection changes.
   - Keep the last frame dimmed while reconnecting, and disable input until replay completes on a worker that supports shared input.

Done when:
- Chromium reports the app installable when served by the gateway (`Page.getInstallabilityErrors` is empty in a non-incognito profile);
- timing marks are visible in debug logs;
- reconnect keeps the last frame.

## Rules that apply to every phase

- Every number in the UI comes from the tokens. Do not add padding to fix density; change layout instead.
- Status is always a dot or ring plus a word, never color alone.
- Install and restore always show every destination with its current state before any action, and nothing changes until the user confirms.
- No silent fallbacks: show unreachable peers, missing bytes, refused control and failed installs per row.
- Build the frontend with `npm ci && npm run build` in `internal/network/web`, and commit the generated `public/` assets.
- Run `go test ./internal/network/...` and `npm test` (Chromium and WebKit) before each PR. Keep one phase per PR.

