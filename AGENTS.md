# agentpack

`agentpack` is a Go CLI that pins **GitHub-hosted skills**, **plugin directories** (`.claude-plugin`, `.cursor-plugin`, and/or `.codex-plugin`), and single-plugin marketplace repositories for a project.

**Source of truth for what to install** is **`agentpack.toml`** at the repo root (direct dependencies, project-local modes, and MCP settings). **`pack.lock`** (v3; v2 still loads) lists every resolved **package** (direct and transitive from nested `agentpack.toml` files inside dependencies) with its pinned commit, `cache_key`, and a **`content_hash`** of its file tree, plus a record of what each staged **MCP server** runs. Both files live in the **project repo**.

All downloaded trees, the metadata index, and your **`local/`** mirror live under a **user-wide agentpack home** (see below)—not under a repo-local `.agentpack/` directory. Staging for harnesses still uses a **per-project temp** directory (or **`AGENTPACK_STAGING_ROOT`**). Rebuilds publish a verified `modes/<mode>-g-<id>/` generation through `modes/<mode>.current`; running commands retain their original paths, so active launches do not block rebuilds. Every process that selects a generation holds a shared lease on it (`<generation>.lease`) until it exits; after publishing, `sync` removes fully built generations that no process leases and no launch or sync record names, so switching agents never rebuilds.

### Pre-release

**No backwards compatibility.** `agentpack` is pre-release: CLI behavior, lockfile shape, staging layout, env vars, and defaults may change without a migration period or deprecation window. Assume **breaking changes** between versions until a stable release is declared.

### Release requirements

A state migration or a wire-format change can break mesh compatibility. Publish it as a stable release or a development release with `make ship-dev` before deploying it to another machine. The pre-push hook warns about migrations pushed without a new product tag. Use `make ship-patch` for hotfixes.

**Ship to machines locally, not through GitHub.** Updating the owner's machines never waits for GitHub Actions or a published release:

1. Commit, then `make ship-patch` (or `ship-dev`) for the version tag; a GitHub release is optional and never blocks the rollout.
2. `make dev-deploy` updates this machine and the public gateway on oracle-vm.
3. `make local-release HOSTS="olegs-mac-mini olegs-macbook-air kyiv-grid kyiv-win-1"` builds Linux amd64/arm64 and Windows amd64 here with zig, builds macOS natively on the Mac mini (Apple's SDK is needed; Go lives in its `~/.cache/agentpack-build`), then replaces `ap` on each SSH host and restarts its gateway. Windows hosts are updated through PowerShell over OpenSSH.
4. Confirm in System → Topology that every peer shows *Synced* and no `schema` mismatch. For a scripted check, `GET /api/library/replication/peers` shows each contact with a `schema_version` and no error. For a former Library-only peer, use **Add** or **Approve** in Topology on **both** sides. The scripted equivalent is `POST /api/library/replication/peers {"peer": NODE_ID}` with the local gateway secret from `network/node.json`.

Product GitHub releases (tags `v*`), when published, must begin with copyable, version-pinned install commands for every supported operating-system family: the shell installer for macOS/Linux and the PowerShell installer for Windows. Keep these commands in GoReleaser's `release.header`; do not publish a changelog-only release. The URLs must use `{{ .Tag }}` so each release installs itself rather than whatever `latest` points to. Both installer scripts and `checksums.txt` must be attached to the same release, and release validation must fail if any command or asset declaration is removed.

Static build-input releases tagged `crsqlite-<commit>` are the sole exception: these are prereleases, never latest, contain the five static library archives and `checksums.txt`, and have no product installers. `third_party/crsqlite.lock` pins the source/toolchain/SQLite versions and archive checksums. Builds use cgo and `make crsqlite` downloads and verifies the host library; only `make crsqlite-build` needs Rust and Zig. macOS release linking uses a native Apple SDK. Normal library fetches never fall back to a source build.

Installer scripts must default to the user-owned `$HOME/.local/bin` directory on every platform. They may create that directory and replace the `agentpack` executable inside it, but must never edit shell startup files, change the current process environment, or persistently modify the user's `PATH`. `AGENTPACK_INSTALL_DIR` is the only supported destination override.

Headless model prompts and exact-response launch checks are optional local checks; account quotas or expired credentials must not block a release. The release workflow is the required validation for the package, installers, snapshot configuration, and published assets, and it is the only GitHub workflow: everything else runs locally (`make ci`, the `.githooks` gates, the `ui/` checks). Docs are not auto-published.

The gateway web UI is the only interactive interface; there is no terminal UI. Modes are edited with `ap mode list|show|create|delete|enable|disable|base`.

### Startup performance is a release requirement

Benchmark cold setup and verified warm launches before and after changing launch, staging, or fingerprint code. Keep reproducible benchmarks and measured results in the repository. On the Linux ARM64 reference host, `BenchmarkMaterializationHome` must stay below 1 ms, `BenchmarkCodexSetup/cold` below 20 ms, and `BenchmarkCodexSetup/warm` below 1 ms (medians of five repeated runs; 32 MiB history fixture). Investigate regressions before release; these are setup budgets, excluding harness startup and downloads.

Fingerprint only configuration and authored resources. Never recursively scan global chat histories, sessions, project runtime state, sockets, or logs merely because the launch workspace is the user's home. Preserve cache integrity checks, symlink validation, nonregular configuration rejection, and concurrent-launch validation when optimizing. See `docs/research/startup-performance.md` for the baseline and reproduction commands. Gateway idle cost (< 1% CPU, < 1 MB/s reads with no browser) and streaming latency budgets are in `docs/research/web-performance.md`; profile with the local-only `/api/debug/pprof/` route before changing event, registry or persistence loops.

A launch stages only its own harness, so each harness returns to the generation its last launch built (`selectLaunchGeneration`); switching agents must not rebuild. Overlay harnesses (Cursor, Antigravity) keep the published path; so do launches after Cursor, whose workspace links the rebuild maintains. Registry objects are content-addressed: `registry.memoized` validates each once per process while its replicated conflict position holds, because index reads run on every Library request and a peer's proxied read must finish well inside the gateway's limits.

A launch never waits for housekeeping whose cost depends on what else is on the machine: tidying the temp directory runs beside the agent.

Progress on a terminal is a spinner drawn with carriage returns and spaces only (no escape sequences, no color); anywhere else it stays plain timed lines. Anything written to the same stderr, including `log` output, clears the spinner line first.

Terminal output buffers must be bounded by bytes rather than PTY read count. Keep a regression for thousands of tiny redraw chunks without loss or disconnection, plus a stalled-viewer test that enforces the memory bound.

### Code structure (contributor rule)

**Per-harness code lives in `internal/harness/<name>/`. Shared code lives in its subsystem. No forwarding aliases, no orphaned glue, and no tiny single-purpose packages.**

**Smallest complete change, and refactor as you go.** Solve the whole problem with the least new code. Delete or merge the duplicated, dead or wrongly placed code you find on the path of a change in that same change, behind a test that locks the behavior, rather than building beside it.

### Ecosystem-first implementation rule

Before adding or retaining custom parsing, matching, terminal, archive, retry, serialization, filesystem, or protocol machinery, check the Go standard library and the project's existing dependencies, then mature focused Go packages. Prefer a maintained library when it removes meaningful code and matches the required semantics; add behavior-locking tests before replacing an implementation. Do not add a dependency for a trivial helper, hide a library behind a forwarding wrapper, or trade explicit product behavior for a framework default. JSON-with-comments uses **`tailscale/hujson`**, Git ignore fallback matching uses **`go-git/plumbing/format/gitignore`**, SSE framing uses **`tmaxmax/go-sse`**, Tailscale status and identity use **`tailscale.com/client/local`** (never the `tailscale` command, which the macOS app builds do not put on `PATH`), collection helpers use **`slices`**, and the public command/help/error/completion surface uses **Cobra + Fang** rather than handwritten usage rendering. Agentpack's small global-option extractor remains product-owned because harness arguments must pass through unchanged, including global options placed after the harness command and arguments protected by **`--`**.

All agentpack-owned terminal rendering is monochrome and terminal-agnostic: use the terminal's native foreground and transparent background instead of detecting or prescribing black/white. Do not add semantic ANSI colors, fixed foreground colors, or palettes that depend on a particular terminal theme; distinguish state with text, glyphs, borders, and emphasis.

- **One folder per harness, uniformly.** Each coding agent is a folder **`internal/harness/<name>/`** implementing `harness.Harness` — even small harnesses (`grok/`, `agy/`) use the same layout. **Everything used by only that one harness lives there**: seed and attribution logic, MCP writer, hook renderer, credential bridging (`codex/auth.go`, `codex/mcp_auth.go`), fake home (`cursor/fake_home.go`), workspace overlays, and launch command construction. To understand or add a harness, read or create one folder. Shared harness contracts, targets, durable-state helpers, and launch helpers stay at `internal/harness/`; registry construction stays in `internal/harness/registry/` to avoid Go import cycles.
- **Shared infrastructure stays in its subsystem and never holds per-harness logic:** `internal/staging/` owns cross-harness staging passes; `internal/hooks/` owns the hook engine; `internal/artifacts/` owns artifact rendering; generic file-tree helpers live in `internal/harness/files.go` or their natural subsystem. A file named after a harness sitting in a shared subsystem is a smell — colocate it or name it for the shared format it adapts.
- **Gateway and worker are separate packages.** `internal/gateway/` holds the gateway: route table, auth and accounts, events hub, HTTP handlers, replication and transfers, its API client and the embedded web UI. `internal/gateway/mesh` owns the tailnet layer (discovery, policy, probes, topology, peer proxy and listener) and `internal/gateway/mcp` the MCP tools, their OAuth server and agent tasks; each declares the small `Host` interface the gateway implements. `internal/network/` holds the worker, PTY and conversation runtime and the worker client; it never imports the gateway. `internal/session/` holds the session row, its SQL and `Actor`, which both use.
- **No single-file folders, no tiny orphans.** Keep a helper in its natural package or caller unless it has enough internal structure to justify a package. The uniform harness folders are the deliberate exception.
- **One canonical home per type; no forwarding aliases.** `Target` is defined only in `internal/harness/target.go` and imported as `harness.Target`. Don't add an alias merely to preserve an obsolete package path; repoint callers.
- **No orphaned glue packages.** Shared launch helpers live in `internal/harness/launch.go`; CLI launch dispatch lives in `internal/cli`. If a package exists only to forward to another, remove it and call the owner directly.
- The harness interface owns *per-harness divergence*; genuinely cross-harness passes that take all roots at once stay shared loops. Adding an eighth harness should mean one new `internal/harness/<name>/`, one registry entry, one `Target` value, and one CLI command.

### User data layout (`AGENTPACK_HOME`)

The full path table lives in [docs/src/reference/internals.md](docs/src/reference/internals.md#user-data-layout-agentpack_home). The rules below still apply.


State sync shares password hashes and chat history with mesh peers. Every
gateway on the tailnet sharing allow list pairs automatically once discovery
reaches it, so starting `ap` is enough to be visible; a peer the owner removed
stays removed. Session and limits UI reads use local replicated rows; live actions
route to their owning node. Keep offline history readable and all live controls
disabled. Altering a replicated table uses the migration `crsql-alter` marker.

MCP connections (`mcp_credentials`, `mcp_credential_approvals`) deliberately
replicate their secrets so one login serves every node; Library bindings stay
local. A bridge reads a secret only through the local gateway for the bound
account. Main (or the creating node) is the only refresher of a rotating
refresh token; other nodes ask it over the mesh. See `docs/src/concepts/mcp.md`.

An env connection approved for `agentpack:agents` is a shared secret: it is never
returned by MCP and reaches only agent processes (never shells), as environment
variables, in sessions started by its owning account.

Web Push keys and subscriptions (`push_keys`, `push_subscriptions`) also
replicate their secrets: Main notifies every device, and without a Main each
node notifies the subscriptions it created. The oldest key pair is current;
each subscription signs with the key it was made with until its browser renews.

`gateway main set` selects the Main gateway and raises the replicated Main epoch; once committed it asks the old Main over the mesh to release its epoch, so that machine stops MCP refreshes and push before the switch replicates (unreachable: a warning, and the old Main stops when the row arrives). Switching machines requires typing
`switch`, cleanly stopping the old gateway and copying its `archive.db` and
archived attachment directories, then running `gateway main archive-ready` on
the new Main. Missing attachments can also be fetched with both gateways running.
Until readiness validates the archive and its attachments, archiving and
archived reads are paused. The hourly mover rechecks both replicated roles for
every session, commits and verifies the archive before replicated pruning, and
never archives an incomplete tail, active turn or pending approval. Main fetches
and verifies attachments first. Other nodes remove archived images only after
verifying Main still has them; a paused handover keeps the local copies.
Archived sessions are permanently read-only,
including native resumes through another folder or a harness alias. No restore
route. Back up `archive.db` and archived `network/sessions/<id>/blobs/` directories.
Archive event clocks and blob references replicate as metadata. A replicated
immutable edit journal preserves late changes through intermediate peers until
Main durably commits and acknowledges them. Keep late body edits and coalescing
deletions safe before applying CRR pruning tombstones, including across a Main
handover; stale gossip must never overwrite a newer archived body.

Non-main gateways forward retained failed traces to Main through mesh-only
diagnostic routes, preserving original machine identity and retry cursors. Healthy
traces stay local. Trace reads merge Main; `logs trace --all-nodes` additionally
queries approved peers. Raw diagnostic day files never enter state replication.

Every new handler, background loop and command must record its failures through
`internal/obs`, preserving request/session tracing and credential redaction. A
successful foreground launcher must not open `state.db` or diagnostic files.
Never record prompts, replies, tool arguments/results, request bodies, raw
environment values or terminal transcripts. See `docs/src/guides/diagnostics.md`.

### Lockfile v2 and **`sync`**

- **`pack.lock`** with **`lockfile_version = 2`** stores **`[[packages]]`** only. Legacy **`[[skills]]`** / **`[[plugins]]`** sections are rejected. In-memory **`skills`** / **`plugins`** are derived views rebuilt from canonical packages after load.
- Lockfile output is canonical and sparse: optional fields such as package **`name`** are omitted when empty. Do not serialize placeholder values such as **`name = ""`**.
- **Rust v2 compatibility is mandatory.** `lockfile_version` and `disabled_plugins` are the canonical underscore keys emitted by Rust `v0.3.12`. The reader also accepts the erroneous `lockfile-version` spelling emitted briefly by the early Go port, but the writer always emits the canonical underscore spelling. Decoder implementation terms such as “strict mode” must never leak into CLI errors; unsupported fields are reported by name with a regeneration command.
- **`sync`** refreshes **`pack.lock`** from **`agentpack.toml`** only when **`[dependencies]`** is **non-empty**. With an **empty** dependency table, **`sync`** treats the existing lock as authoritative (manual edits, tests, or hybrid workflows).
- Run **`agentpack lock`** to force a full resolve from the manifest (requires **`agentpack.toml`**).
- **Lockfile v3 = v2 + `content_hash` per package + `[[mcp_servers]]`.** The reader accepts 2 and 3. The writer emits 3 as soon as a lock holds either addition and 2 otherwise, so a lock that was never re-locked stays byte-compatible with older binaries. `content_hash` carries an algorithm prefix (**`sha256-tree-v2:`**); an unknown prefix is rejected. A **`sha256-tree-v1:`** hash (a tree extracted without execute bits) in a project `pack.lock` is treated exactly like an absent `content_hash`: sync and launchers stage with the one-line warning "pack.lock has v1 content hashes; run `agentpack lock` to record v2", never rewrite the lock for it, and `AGENTPACK_REQUIRE_VERIFIED=1` refuses; `lock`/`add`/`remove`/`update` record v2 under the same pins. Library snapshots and older peers may still carry v1 and are read as they are.
- **Extraction keeps in-package links and execute bits.** A symbolic link in a GitHub archive whose target is a clean relative path inside the extracted tree is created as a link; any other link becomes an empty file. A file with any execute bit in the archive (or a path dependency's source) is cached `0755`, everything else `0644`, and staging keeps the bit, so hook and MCP scripts run by path.
- **`sha256-tree-v2`** hashes the materialized cache entry (after extraction and `NormalizePluginLayout`): every regular file and symlink, sorted bytewise by slash-separated relative path, each written as `kind 0x00 path 0x00 decimal-length 0x00 payload` with kind `f` (regular), `x` (any execute bit), or `l` (symlink; payload is the target, never followed). Directories, other permission bits, owners, and times are ignored. `docs/src/concepts/integrity.md` is the normative description. **Changing extraction or layout normalization changes the digest of existing packages and requires a new prefix**; `TestTreeDigestOfNormalizedPluginIsPinned` guards this.
- **A content hash is only recorded from bytes fetched in the same command**, never read off an existing cache entry. `lock`, `add`, `remove`, and `update` record one for every package and re-download any package that has none; `sync` and launchers record one only for packages they must download. A hash already in the lock for a `cache_key` always wins.
- **Every read of the cache for resolving or staging verifies it first.** `sync`, `lock` and the other explicit commands hash the content. A launch that rebuilds uses `cache.VerifyPackageIncremental`, which reuses a stamp from an earlier full check while the entry's metadata is unchanged; `AGENTPACK_FULL_VERIFY=1` turns that off. A stamp is never written for an entry modified within `racyMetadataWindow` of the check. A mismatch is a `cache.IntegrityError`: the command exits non-zero, nothing is staged, the entry is left as found, and the CLI prints expected/actual digests and the cache path as plain lines. Nothing heals silently. **`agentpack sync --repair`** re-fetches the pinned commit into a temporary directory, verifies it against the lock, replaces the entry, and reports it. Freshly downloaded content is verified before it enters the cache; a download that contradicts the lock is discarded.
- **"No content hash" means the key is absent.** A `content_hash` that is present but malformed, empty, or of an unknown algorithm is `lockfile.ErrContentHash`: every command refuses the lock and none rewrites it, `lock` included. `cache.CheckLockEntries` additionally refuses a content-hashed GitHub entry whose `cache_key` is not the key of its own owner/repo/path/commit (a hand-edited `commit`). All commands load the lock through `loadCheckedLock`.
- **`sync` and launchers never change the pins of an existing entry** (`commit`, `cache_key`, `content_hash`, `url`) and do not rewrite an unchanged lock. They still follow manifest edits (added or removed dependencies, a changed exact commit, changed path-dependency files) and print one `pack.lock: …` line per change. Only `lock` / `add` / `remove` / `update` / `sync --update-lock` re-resolve on purpose, and they report every pin they move. Only `lock` regenerates a lock it cannot parse, with a warning. A lock load error is never swallowed.
- **Staged content is recorded and checked.** Each rebuild writes `.staged.json` inside its selected staging generation with a SHA-256 per file of every harness root's `skills/`, `commands/`, `agents/`, `rules/`, `hooks/` and MCP config file. **`sync --verify-only`** hashes staging against it and fails naming each modified, added, or missing file; a plain **`sync`** reports the changed files it replaces (by type, size, and mtime). Hard links to the project's `./.agents/` are exempt. The launch fast path does not run this check.
- **Locks without content hashes still stage**, with a one-line warning on every `sync` and launch. **`AGENTPACK_REQUIRE_VERIFIED=1`** turns the warning into a refusal.
- **MCP records.** `lock`, `add` and `mcp add` write one `[[mcp_servers]]` record per merged server with `launcher` (`npm` / `pypi` / `docker` / `remote` / `command`) and `status` (`pinned` / `unpinned` / `unpinnable`). npm servers (`npx`, `bunx`, `pnpm dlx`, `yarn dlx`, `bun x`) are resolved to an exact version plus registry integrity, and staging rewrites the package argument to `pkg@<version>` for every harness. An unreachable registry fails the lock unless **`--allow-unpinned-mcp`** records `status = "unpinned"` with `allow_unpinned = true`; `add` keeps the package and leaves that server unrecorded with a warning. `sync` and launchers never contact a registry; a record applies only while its `definition` digest (command, args, URL — never env) matches. Supported `npx` servers can also carry a locked dependency tree. Other runners and unresolved npx trees pin only the top-level package or remain explicitly unpinned; see `docs/src/concepts/mcp.md`.
- Harness launchers (**`agentpack claude`**, **`opencode`**, **`codex`**, **`agent`**) stage and verify **only the selected harness**, suppress routine hook-rendering messages, and run a **fast pre-sync** when **`agentpack.toml`**, **`pack.lock`**, and **`./.agents/`** are unchanged since the last successful launch sync: they check staging and the cache layout and **skip** full lock resolve, re-download, and staging rebuild. Nothing is staged on that path, so the cache content is not hashed there: the launch digest includes a fingerprint of the cache entries' file metadata (path, type, permission bits, size, mtime, and on Unix inode and ctime, as in Git's index), and any visible change forces the full sync, which hashes. Every input identity (launch, materialized, synced, preflight's lock digest) is a digest of one `sync.Inputs`; a harness whose native configuration changes what is staged declares it in its `Harness.LaunchInputs` field. **`AGENTPACK_FULL_VERIFY=1`** hashes on every launch. Floating pins (branch / floating semver) therefore **do not advance** on launch alone — run **`agentpack sync`** or **`agentpack lock`** when you need **`pack.lock`** refreshed from the manifest.
- GitHub **ref → commit** and **tag list** lookups are cached in **`index.db`** and reused across **`add`**, **`lock`**, and **`sync`**. Fresh cached metadata avoids repeat API calls; exact tag-name ref lookups also reuse the cached tag list directly.
- When GitHub REST ref/tag lookups fail, agentpack falls back to the Git protocol through an embedded Go Git client against **`https://github.com/<owner>/<repo>.git`** before using stale cached metadata. This removes dependencies on both the throttled REST API and an external `git` executable for ref and tag resolution.
- **GitHub credentials** come from `GITHUB_TOKEN`, then `GH_TOKEN`, then `gh auth token --hostname github.com` (only when `gh` is on `PATH`; run once per process with a 5 s timeout; failure means anonymous, silently). The token is sent on REST requests and authenticated codeload; the Git-protocol fallbacks try anonymously first and retry with HTTP basic auth (`x-access-token`). When every source answers 401/403/404 the error says the repository may be private and suggests `gh auth login`, or, if a token was used, that the account may lack access. The token is never printed or logged.

### ACP chat sessions

Workers can run **`transport: "acp"`** agents (Claude, Codex, OpenCode, Cursor, Grok, OMP, Antigravity) with the same restart and detach semantics as terminal sessions. Session metadata adds **`conversation_state`** (`working` / **`needs_you`** / **`idle`**), optional **`agent_session_id`**, and **`waiting`**: the background work an idle chat will wake for, from AIR `async_task_*` updates, `ScheduleWakeup` and `ask` (`testdata/acp/background-*.jsonl`). It holds no command text. The worker persists conversation events in state.db and keeps a byte-bounded replay tail; the gateway can read saved history without a live worker and proxies **`/api/sessions/{id}/conversation/*`**, **`GET /api/agent-sessions`**, and image **`/blobs/{sha}`** with the same auth and **`?node=`** routing as other session APIs. Shared reducers and fixtures: **`testdata/acp/full.jsonl`**, **`internal/acp`**, **`ui/src/features/conversation/`**. Bare **`agentpack`** and the installed **`ap`** binary print the gateway URL and open the web UI on a TTY. Claude/Codex adapter specs and verified trees live under **`$AGENTPACK_HOME/adapters/`**; intact warm caches avoid registry lookups. See **`docs/src/guides/acp-sessions.md`** and **`ui/README.md`**.
The New session sheet browses a machine with `GET /api/folders?path=` and lists discovered projects with `GET /api/folders/found`. Both require gateway session auth and use the same `?node=` proxy as projects. Listing is bounded to two seconds and 1,000 directories. Discovery runs on demand, is cached for ten minutes, and omits recents and agentpack worktrees. Browser accounts can start chat or terminal sessions in an existing absolute directory, including folders with no launch history. MCP starts still require a recent folder or a worktree the gateway already recognizes.

Agents sign in from the web UI: the gateway runs each agent's own login command in a hidden terminal on its machine, shows the link and code, and types back what the person pastes (`internal/gateway/agent_login.go`, per-harness `internal/harness/<name>/login.go`; owner-only `/api/agents` routes). A chat whose agent was not signed in records an `auth_required` notice and starts after the sign-in. Sign out exists where the agent has a non-interactive logout. A chat cut off mid-turn by a dead worker resumes by itself once per ten minutes, after waiting at most 75 s for state to sync (`resume_pending` on the session row, never stored); a turn that ends on a network-drop error is retried after 30 s, 2 min and 8 min (`conversation_retry.go`). Never log login links, codes or pasted text. Sign-in files (Claude, Codex, Cursor, Grok, OpenCode, Antigravity) replicate through `agent_credentials`, secrets included; the latest revision wins, never a cross-machine file time (`internal/gateway/agent_credentials.go`); a changed local file whose token age is older than the shared one is replaced instead of published (age only vetoes, never promotes), and a file `ValidCredential` rejects is never shared; a web UI sign-out writes a newer empty row that stops sharing without deleting peers' files, until the next web UI sign-in; on first sight the shared row wins and the local file is kept as `.bak`. A rotating sign-in (Claude, Codex) is renewed only by Main when a Main exists, otherwise by the gateway that published it, ahead of the agent's own refresh, so no two gateways spend one refresh token; there is no timeout failover. No gateway renews it, or an MCP token in the background, before catching up after a start or wake (`caughtUp`: a successful state pull, from Main when another machine is Main, or 3 minutes). A renewal already sent finishes through shutdown and reaches the file before the table. The gateway and background loops never read another program's macOS keychain item: macOS asks for the login password again after every agentpack update. Account probes run each CLI's own status command; OMP's (`omp models`) refreshes its own model cache in `~/.omp`, and nothing else there is written.
ACP limits: all writes to an existing ACP session require the recorded user and stable peer node identity (or the same local user/host); other actors may read logs and blobs but cannot mutate the session. Client MIME is ignored in favor of stored image bytes; storage is capped at 20 MiB per image and 100 MiB total or 128 files per session, with serialized uploads and a 30-second body-read deadline. ACP terminal output is capped at 4 MiB, nested terminal process groups are force-killed on release and worker shutdown, and an oversized unterminated frame closes its stream after 10 seconds.

## Gateway UI

Gateway authentication uses email accounts. The first browser owner claims an
unclaimed gateway; a legacy gateway requires its current password and preserves
that hash. Unclaimed browsers may access only health/status, the claim endpoint
and static UI. Start/status and bare `ap` show `UNCLAIMED: open <url> now` until
claimed. Keep first-time public access restricted until the owner is claimed.
The loopback origin is always served; Public access (node_settings `public`,
owner-only, enabled only after a nonce self-probe over verified TLS, or pinned by
`AGENTPACK_PUBLIC_URL`) adds one HTTPS origin. Origin, cookie name and limiter key
are resolved per request; X-Forwarded-* counts only from a loopback peer while on.
Browser cookies survive restarts and slide for thirty days; HTTPS uses
`__Host-agentpack_session`, Secure, HttpOnly and SameSite=Strict. Disabling an
account, changing its password or signing out everywhere revokes its logins and
OAuth grants. Browser ACP writes require the owning account email; a shared node
name alone never grants write access.

Offline account administration is `gateway user add EMAIL`, `gateway user list`,
`gateway user disable EMAIL`, `gateway passwd EMAIL`, and `gateway reset-owner`.
`gateway join NODE` prepares an unclaimed gateway before it starts, closes its
claim window, and records explicit mesh approval after typing `trust`.
`gateway whoami EMAIL` binds local actions to an enabled account; default mapping
is the owner. Local bindings never replicate or fall back to owner when disabled.
Owner reset must preserve the replicated account epoch; browser and OAuth logins
must not revive when an email is recreated on another node.
Password commands read hidden terminal input twice and accept no password flags.
Owner reset requires typing `reset`, then removes accounts and revokes logins.
The Account group in General shows the email and sign-out actions, without user/password
administration forms. See `docs/src/guides/gateway-deployment.md`.

The gateway's browser app lives in `ui/` (Vite, Bun only; no Node or npm). `make ui-stage` builds it and copies `ui/dist` into `internal/gateway/webdist`, which the Go gateway embeds and serves at `/`.

- **Stack:** React 19, TanStack Router (file routes in `ui/src/routes/`) and TanStack Query, Tailwind CSS v4, shadcn components on `@base-ui/react` (`ui/src/components/ui/`), and xterm.js 6 for the terminal, loaded lazily. Tokens live in `ui/src/index.css`. React Compiler handles memoization: never hand-write `useMemo`, `useCallback` or `memo`.
- **Kit:** macOS composition in `ui/src/components/kit/`, specified in `ui/DESIGN.md`. Components use only kit tokens.
- **Composer:** Enter on an empty composer sends the oldest queued message now. Owner requirement; UX audits must not remove it.
- **CSS traps:** base rules stay inside `@layer base`, because an unlayered rule beats every Tailwind utility. Font-size utilities must name their type (`text-(length:--fs-sm)`); plain `text-(--fs-sm)` is a color. The `data-selected:` variant needs `data-selected="true"`, and Base UI toggles expose `aria-pressed`, not `data-state`. `ui/tests/visual.spec.ts` asserts computed styles, because role-based tests cannot see any of this.
- **Placeholder:** `internal/gateway/webdist/placeholder.html` is the only tracked file there, so `make ui-stage` never dirties the checkout. The gateway serves it when no build was staged, and `agentpack deploy` refuses to build without one.
- **Routes** (`ui/src/routes/`):

| Path | Search params |
|---|---|
| `/` | none (redirects to `/sessions`) |
| `/login` | `return_to` |
| `/sessions` | `q`, `group`, `new`, `view` (`history`), `find` (text), `project` (folder name), `on` (node ID), `by` (agent) |
| `/sessions/$machine/$sessionId` | same, plus `sub` (the subagent open in the main view) |
| `/sessions/project/$machine/$folder` | same, plus `install` (focuses project search); `find` prefills project search |
| `/library` | `q`, `type`, `sort`, `add` |
| `/library/package/$` | same |
| `/library/mcp/$name` | same |
| `/library/discover` | same |
| `/library/agentpack/$id` | same, plus `revision`, `restore` |
| `/system` | none. Desktop redirects to `/usage`; phones show the System list |
| `/usage` | `range` (`day`, `week`, `month`), `back`, `metric`, and the filters `project` (folder name), `machine` (node ID), `agent` |
| `/usage/$kind/$id` | redirects to `/usage` with that filter (`machine`, `agent`; old `project` links open unfiltered) |
| `/usage/limits` | none |
| `/limits` | none (redirects to `/usage/limits`) |
| `/machines` | none (redirects to `/machines/topology`; phones to `/system`) |
| `/machines/topology`, `/machines/$machine` | none |
| `/settings` | none (redirects to `/settings/general`; phones to `/system`) |
| `/settings/$pane` | `general`, `notifications`, `sharing`, `connections`, `agents`, `omp`, `diagnostics`. Diagnostics accepts `issue`, `status`. `connections` is the Vault pane and accepts `callback`, `oauth_login`, `connection`. |
| `/settings/account`, `/settings/appearance`, `/settings/accessibility` | redirect to `/settings/general` |
| `/settings/replication` | redirects to `/machines/topology` |

The toolbar and phone tab bar hold three views, Sessions, Library and System (⌘1–3 or ⌥⌘1–3, since Safari keeps ⌘1–9, with Ctrl off Mac). The System sidebar (`ui/src/features/system/`) lists Usage, Limits and Topology, then General, Vault and Notifications under Settings. Open a machine page from its row or graph node in Topology.

- **Session lifecycle:** Close records `closed_at` and keeps the chat in History. Reopen clears it. Chats moved to cold storage show Read-only.
- **Peers update on their own schedule.** The gateway routinely talks to older peers, so API reads tolerate them: a 404 means `Needs update`, never a raw error, and a response shape that changed is normalized in its query. This is runtime interoperability and is not covered by the pre-release rule. Test new endpoints against a peer without them.
- **Never use `node` as a search param.** The Go handler treats `?node=` as a proxy instruction on `/api/` paths, and this rule stays in force anyway.
- Validate search params with `search()` / `looseSearch()` from `ui/src/lib/search.ts` (a string or absent; `"flag"` keys accept the parser's numbers). Never default them to `""`, which makes the param required on every `<Link>`. Apply defaults where the value is read. Response shapes are checked with the small readers in `ui/src/lib/validate.ts`; zod is not a dependency.
- `bun run lint` fails when React Compiler skips any function (`scripts/check-compiler.ts`); rewrite the code rather than opt out.
- The query cache persists to IndexedDB per UI build (`src/lib/persist.ts`); sign-out and any 401 must call `forgetQueries()`. `public/sw.js` caches only the shell and hashed assets, never `/api/`.
- Live data is pushed: with `/api/events` connected, queries do not poll. A `state` event names the replicated tables it changed (`tables`) and the UI refetches only their readers.
- Linkable overlays open from a search param (`new`, `install`, `restore`, `add`). Confirmations are local state.
- **Alert model:** one surface per situation. Action finished with its result on screen: nothing more. Action finished out of sight: `Hud` (top right), 4 s. Copy: the button shows `Copied` (`useCopied`), never a Hud. Action failed with nothing open: `Hud` with `Retry`, until dismissed. Ongoing pane or terminal state: `Banner`. Irreversible action: `ConfirmAlert`. Form or server rejection: inline under the field. Do not use `notice()`, `*-status` lines or `window.confirm`.
- **Size budgets** (`ui/scripts/check-size.ts`, run by every `bun run build`): main JS 170 KB gzip, initial CSS 30 KB, every lazy JS chunk 100 KB (the terminal included). Never raise a budget. The root route imports only `Button`, `Input`, `Tooltip` and `DropdownMenu`; dialogs, selects, the command palette and context menus load inside routes or through `React.lazy`.
- **Chat terminal:** ⌘J (Ctrl+J off Mac) or Ctrl+Backquote toggles the panel. Ctrl+Shift+Backquote opens a shell in the chat folder or worktree. ⌘⇧J (Ctrl+Shift+J off Mac) jumps to the next session that needs you. Child shells use the same PTY worker and stay out of the session list until opened as a session.
- **Checks:** in `ui/`, `bun run lint && bun run test && bun run build && bun run e2e`. `make artifact` also runs `scripts/ui-smoke.sh` against the built gateway. `bun run capture` writes design-review screenshots; it is not part of the suite.

## Test budgets

A slow test is a defect, and a test that repeats another costs every future run. These limits are requirements, enforced where the table says.

| Suite | Budget | Enforced by |
|---|---|---|
| One browser test | under 2 s; fails at 8 s | `ui/playwright.config.ts` `timeout`, `reportSlowTests` |
| Whole browser suite | under 60 s on a 4-core host; fails at 90 s, or 150 s on CI, which retries once and reports flakes | `globalTimeout`, `retries` |
| One Go test | under 2 s, no real network | review; `go test -json` shows durations |
| `go test ./...`, uncached | under 30 s | review |
| Vitest | under 10 s | review |

Rules:

- **Test each behaviour once, at the cheapest level that can see it.** Pure logic goes in Vitest or Go. A browser test is for what needs a browser: CSS, focus, routing, terminals, sockets. Do not add one that restates a unit test or a sibling spec.
- **Never wait in real time.** Use `page.clock` in the browser and an injectable interval in Go. `test.setTimeout` and `test.slow` are banned, and `bun run lint` rejects them: rewrite the test instead. A test with a 60 s window fast-forwards 60 s.
- **No real network.** Point registries and upstreams at a local fake.
- **Engines are not a multiplier.** Every behaviour runs on Chromium. The `phone` and `webkit` projects hold only what can differ there: touch layout, and CSS, selection and mouse handling. Use a viewport override on Chromium for layout breakpoints. Do not parametrize over viewports or routes unless the behaviour differs between them.
- **Assert what users see.** Role queries cannot see a broken cascade, so kit styling needs computed-style assertions in `ui/tests/visual.spec.ts`.
- `navigator.webdriver` zeroes query retry backoff and skips the WebGL renderer. Keep those two switches and do not add behavior differences under it beyond what makes tests fast.

## Reference

Behaviour reference that is not a contributor rule lives in [docs/src/reference/internals.md](docs/src/reference/internals.md): the `AGENTPACK_HOME` path table, module IDs, the `agentpack.toml` sections, `add` resolution, harness launch research, what each staging step and launcher does, attribution defaults, environment variables, commands, and the manifest and lockfile sketches. Read the matching section before changing that behaviour.
