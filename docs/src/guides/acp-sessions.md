# ACP chat sessions

Some harnesses speak the [Agent Client Protocol](https://agentclientprotocol.com/) over stdio. Agentpack runs them in a **worker** (same ownership model as terminal PTY sessions), exposes a **conversation log** on the gateway, and renders chat in the browser or in the **`ap`** Bubble Tea hub.

## Session transport

`GET /api/sessions` includes optional fields:

| Field | Meaning |
|---|---|
| `transport` | `terminal` (default) or `acp` |
| `conversation_state` | `starting` until a new chat's agent is up, then `working`, `needs_you`, or `idle` while the worker is running |
| `last_event_at` | Time of the chat's latest conversation entry; the session list's ages and unread markers use it |
| `agent_session_id` | Agent-native session id for resume |

Start chat with `POST /api/sessions` and `{ "transport": "acp", ... }`. The gateway answers at once with a gateway-chosen session ID and `conversation_state: "starting"`; pack sync and the agent launch continue in the background, and the worker keeps `starting` until its endpoint serves. A message typed meanwhile is held by the browser and sent once the chat is running. A failed start ends the row and leaves the error in the transcript; a gateway restart ends any chat it was still starting. In the sidebar, "+" on a folder starts a chat with that folder's last agent (⌥-click or right-click picks another); ⌘N opens the full New session sheet. Resume with `agent_session_id` from `GET /api/agent-sessions?directory=&harness=&mode=`. Switching Chat ↔ Terminal in the UI stops the current worker and starts the other transport; resume depends on the agent. A message sent to a stopped chat calls `POST /api/sessions/{id}/resume` with `{ "text": … }`: the gateway claims the ended row, restarts its worker under the same ID (the agent reloads its conversation and the transcript continues), and sends the text once it runs. Every 15 s, and at startup, the gateway ends rows whose worker died and settles chats left working, with a notice in the transcript. A chat whose agent died mid-turn (the machine slept, rebooted or lost its gateway) resumes by itself with a prompt to carry on, once per ten minutes, so an agent that keeps dying is left for you. A chat waiting on you, or one that never started, is only settled. A chat that never started (its agent wasn't signed in, say) starts fresh on resume. When a turn ends on the agent's own connection-error line (Claude's `API Error: Connection error.`, Codex's `stream disconnected …`) or the prompt fails with a network error, the worker sends a "continue" turn after 30 s, 2 min and 8 min, with a notice each time; anything the person sends first cancels it, and a reply that only talks about networking never counts.

Session metadata, names and recent folders live in `$AGENTPACK_HOME/state.db`.
Explicitly enrolled mesh gateways replicate these rows and conversation events.
The browser reads one local session list across nodes, including offline chats.
Offline history remains readable, while the composer and request-answer controls
are disabled. Renaming is a shared metadata edit and works offline. See
[Gateway State and Mesh Sync](../concepts/state.md) for trust and version rules.
Worker exit records the end time and final terminal state without deleting that
metadata. Conversation entries also persist in state.db. Memory eviction does not
remove saved history; adjacent text chunks may be stored as one merged entry.
Restart loads the saved replay tail and continues its sequence numbers. Writes
batch every 50 ms, flushing sooner under byte pressure and once more after
queued updates and active prompts drain during shutdown. Storage failures stop
the worker and report a diagnostic without copying chat into diagnostics.
A private `network/sessions/<id>/worker.json` retains only the running
process's ID, PID, loopback address and secret. Forgetting a recent folder hides
its launch rows; it does not remove files from the folder or erase session data.

Native resume ownership also lives in state.db. Hidden ownership rows retain the
original actor even when an old worker directory has disappeared. At gateway
startup, stopped legacy workers' metadata, names and conversation files are
imported transactionally; their imported files are removed only after commit.
Images and other support files remain. Live workers retain their files until a
later startup. Unreadable sources stay in place and produce a diagnostic without
preventing gateway startup; unreadable ownership records keep affected resumes
read-only until the source can be imported.

## Conversation API

Worker routes (gateway proxies as `/api/sessions/{id}/conversation/...` with the same auth and `?node=` peer routing as other session APIs):

- `GET /conversation?since=` — NDJSON replay; `Accept: text/event-stream` or `?stream=1` for live updates. Snapshots are capped at 4 MiB and retain only complete records; `X-Agentpack-Truncated: true` marks omitted data. The gateway returns saved history when the worker is unavailable, preserving the same sequence filter and response formats. A stopped session’s SSE replay ends after its saved entries.
- `POST /conversation/prompt`, `/cancel`, `/permission`, `/elicitation`, `/config`
- `POST /conversation/revert` — body `{turn}` (1-based; a turn starts at each user prompt). Restores the turn's edited files from the worker's own log: every file must still match the turn's final text, all are checked before any is written, created files are deleted, paths must stay inside the session directory. Refused with 409 while a turn runs.
- `GET /conversation/config-options`
- `GET /blobs/{sha}` — session-scoped PNG/JPEG/GIF/WebP images, 20 MiB each and 100 MiB total or 128 images per session (whichever limit is reached first)

All writes to an existing ACP session—including prompts, cancellation, config changes, image uploads, permission/elicitation answers, rename, stop, and restart—require the session's recorded owner. Browser writes match the authenticated account email. Peers match by immutable Tailscale stable node ID and user; local transports match by user and host. Other actors can read the conversation and images but cannot modify the session; a missing owner identity is read-only.

Uploaded image MIME is detected from the stored blob bytes before the ACP agent receives it; client-supplied MIME is ignored.
ACP terminal output is capped at 4 MiB; nested terminal process groups are terminated on release or worker shutdown. Complete stdout frames over 4 MiB are dropped; an unterminated oversized frame closes the stream after 10 seconds.
Session stop/restart are distinct from turn cancel: stop terminates the ACP adapter process; restart replaces it and reloads the native session. Both retain the existing session owner checks.

The wire log stores raw ACP `session/update` kinds plus agentpack kinds such as `permission_request` and `turn_end`. Shared fixtures live in `testdata/acp/full.jsonl`; reducers in Go (`internal/acp`) and the gateway UI (`ui/src/features/conversation/reducer.ts`) must stay aligned. Optional local check: `./scripts/acp-conformance/run`.
The script does not launch agents. It reports missing `testdata/acp/<harness>.jsonl` transcripts; recording them requires owner-authorized live prompts and terminal captures and may incur provider usage.

## Switching agents and permissions

The agent button in the composer continues a chat with another agent (`POST /api/sessions/{id}/harness`). The old agent stops; the chat keeps its ID and transcript, and the new agent starts a fresh session whose first prompt carries that transcript (messages and tool titles, no thoughts or subagent output). With agentpack tools on, only the newest 32 KiB is inlined and the prompt names the `read_conversation` tool and session ID for the rest, which stays in `state.db`; without tools the newest 96 KiB is inlined. Enter on an empty composer sends the first queued message now.

The permission mode last chosen for an agent is stored network-wide in `mesh_settings` (`permission_mode.<harness>`) and applied to every chat of that agent when it starts or resumes. Antigravity's ACP server offers no permission modes, so agentpack offers Ask and Bypass Permissions for it and answers its permission requests itself in Bypass.

## Opening the web UI

On a TTY, bare `agentpack` and the `ap` symlink (installed beside the binary) print the running gateway's URL and open it in the default browser; inside an agent session they only print it. Chats, permission answers, queued prompts, goals and image attachments all live in the gateway web UI. When no gateway is running, the command says to start one with `ap gateway start`.

## Signing agents in

An agent that isn't signed in can be signed in from the web UI, from any device, without a terminal on its machine. A chat that fails because its agent needs a sign-in shows a sign-in card instead of the error, and the machine page's Agents table offers **Sign in…** next to each agent that isn't signed in. The gateway runs the agent's own login command on that machine in a hidden terminal (`internal/network/agent_login.go`), shows the link and one-time code it prints, and types back what you paste. Nothing opens a browser on the machine, and the credentials stay where the agent normally keeps them. Each gateway also shares those sign-in files with every mesh peer through the replicated `agent_credentials` table, so an agent signed in on one machine is signed in on all of them. The newest file wins. The shared files are Claude's `~/.claude/.credentials.json`, the Codex `auth.json` its staged homes use, Grok's shared `auth.json`, OpenCode's `auth.json` and Antigravity's `~/.gemini/antigravity-cli/antigravity-oauth-token`. On macOS, Claude and Antigravity read the copied file only while the keychain holds no sign-in of their own, and a Mac's keychain sign-in is not shared. Cursor and OMP sign-ins are not files another machine can use, so they are not shared. **Sign out…** in the web UI stops sharing that sign-in but leaves other machines' copies in place.

| Agent | Command | You do |
| --- | --- | --- |
| Codex | `codex login --device-auth`, in an isolated home; the result becomes the shared credential | open the link, enter the code |
| Claude | `claude auth login --claudeai` (or `--console`) with `BROWSER=true` | open the link, paste the code the page shows |
| Antigravity | agy's own sign-in screen (it has no login command) | open the link, paste the address the page lands on |
| Cursor | `cursor-agent login` with `NO_OPEN_BROWSER=1` | open the link |
| Grok | `grok login --device-auth` in an isolated home; the result becomes the shared login | open the link, confirm the code |
| OpenCode | `opencode auth login` for one provider | open the link; paste the code or address when asked |
| OMP | `omp login <provider>` | open the link, paste the address the page lands on |

A pasted `http://localhost…` address is a callback the browser could not reach; the gateway replays it on the machine. Routes, owner-only and proxied with `?node=` like other machine routes: `GET /api/agents` (each agent's account, cached for a minute), `POST /api/agents/{harness}/login` `{method}`, `GET`/`POST`/`DELETE /api/agents/{harness}/login/{id}`. Links, codes and pasted text are never logged. After a sign-in the chat starts with the message left in its composer. **Sign out…** on the machine page (after a confirmation, `POST /api/agents/{harness}/logout`) runs `claude auth logout` or `cursor-agent logout`, removes the shared Codex credential with a logout marker (as `agentpack codex logout` does), or removes the shared Grok login. OMP, OpenCode and Antigravity have no logout that runs without a terminal, so they offer none.

## Harness coverage

Each harness implements `Harness.ACP` under `internal/harness/<name>/acp.go`. Claude and Codex pin npm ACP adapters under `$AGENTPACK_HOME/adapters/` (Node required for chat). Claude ACP forwards staged plugin directories and the staged `agentpack-settings.json` as session metadata, without redirecting `CLAUDE_CONFIG_DIR`; `CLAUDE_CODE_EXECUTABLE` is honored, with `CLAUDE_CODE_PATH` as its fallback. Adapter specs and verified trees are cached; intact warm caches need no registry lookup, while changed trees are re-fetched against their pinned integrity. Grok may require prior login; do not launch Grok solely to discover auth state (its sign-in state comes from its auth file).

See `ui/README.md` for the full HTTP contract and `CHANGELOG.md` for release notes.
