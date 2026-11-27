# ACP chat sessions

Some harnesses speak the [Agent Client Protocol](https://agentclientprotocol.com/) over stdio. Agentpack runs them in a **worker** (same ownership model as terminal PTY sessions), exposes a **conversation log** on the gateway, and renders chat in the gateway web UI.

## Session transport

`GET /api/sessions` includes optional fields:

| Field | Meaning |
|---|---|
| `transport` | `terminal` (default) or `acp` |
| `conversation_state` | `starting` until a new chat's agent is up, then `working`, `needs_you`, or `idle` while the worker is running. A terminal Claude session reports the same three states through hooks agentpack adds at launch (`ap hook-exec session-state`). A terminal Codex session reports `working` and `idle` from the turn states its own interface prints (OSC 7501), and `idle` again through a `notify` agentpack adds at launch, which still runs a `notify` program you configured. Other terminals leave it empty |
| `waiting` | Background work an `idle` chat will wake for: `{kind, until?, peer?}` with kind `shell`, `monitor`, `agent`, `task`, `wake` (a `ScheduleWakeup`) or `reply` (an `ask` to the chat `peer`). Agentpack advertises the AIR `asyncTasks` capability, so the Claude and Codex adapters report background shells, monitors and self-started turns. It never holds commands or arguments, and `until` bounds every timed item. The UI shows such a chat as Waiting, and push sends "finished" only once the list empties |
| `last_event_at` | Time of the chat's latest conversation entry; the session list's ages and unread markers use it |
| `agent_session_id` | Agent-native session id for resume |
| `closed_at` | Time the session was closed, omitted when open |

Start chat with `POST /api/sessions` and `{ "transport": "acp", ... }`. The gateway answers at once with a gateway-chosen session ID and `conversation_state: "starting"`; pack sync and the agent launch continue in the background, and the worker keeps `starting` until its endpoint serves. A message typed meanwhile is held by the browser and sent once the chat is running. A failed start ends the row and leaves the error in the transcript; a gateway restart ends any chat it was still starting. In the sidebar, "+" on a folder starts a chat with that folder's last agent (⌥-click or right-click picks another); ⌘N opens the full New session sheet. Resume with `agent_session_id` from `GET /api/agent-sessions?directory=&harness=&mode=`. Switching Chat ↔ Terminal in the UI stops the current worker and starts the other transport; resume depends on the agent. A message sent to a stopped chat calls `POST /api/sessions/{id}/resume` with `{ "text": … }`: the gateway claims the ended row, restarts its worker under the same ID (the agent reloads its conversation and the transcript continues), and sends the text once it runs. Every 15 s, and at startup, the gateway ends rows whose worker died and settles chats left working, with a notice in the transcript. A chat whose agent died mid-turn (the machine slept, rebooted or lost its gateway) resumes by itself with a prompt to carry on, once per ten minutes, so an agent that keeps dying is left for you. After a start or a wake it first waits, at most 75 s, until the gateway has pulled state from the mesh (from Main, when another machine is Main), so the agent does not start on a replaced sign-in; the session row reports `resume_pending` meanwhile, and a message you send resumes it at once. A second gateway restart during the wait drops the automatic resume. A chat waiting on you, or one that never started, is only settled. A chat that never started (its agent wasn't signed in, say) starts fresh on resume. When a turn ends on the agent's own connection-error line (Claude's `API Error: Connection error.`, Codex's `stream disconnected …`) or the prompt fails with a network error, the worker sends a "continue" turn after 30 s, 2 min and 8 min, with a notice each time; anything the person sends first cancels it, and a reply that only talks about networking never counts.

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
queued updates and active prompts drain during shutdown. A storage failure keeps
the chat running from memory, retries with backoff, leaves one notice in the chat
and reports one diagnostic without copying chat into it; an archived chat stops
the worker.
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

## Closing and reopening chats

Choose **Close** from a session's menu to take it out of the sidebar. Close
stops the agent if it is running and asks for confirmation if it would interrupt
a busy agent. Use the project or machine header menu to close several sessions
at once. The toolbar activity viewer offers one **Undo** for the whole close.

After upgrading, ended chats you previously archived appear in History once their gateway restores them in the background.

Work's **History** section shows the three most recently closed chats.
Select **Show all** to browse chat history by date, search by name or project,
or filter by project, machine or agent. A project's **History** tab in the
New session sheet also lists its saved chats.

To return a chat to the sidebar, choose **Reopen** in History or open the chat
and choose **Reopen** in the **Closed · in History** banner. Resuming the chat or
sending it a message also reopens it. Older chats moved to cold storage on Main
are **Read-only**. See [Main gateway and archive](../concepts/state.md#main-gateway-and-archive).

## Starting in any folder

In **New session**, choose a machine and type an absolute path or select **Open folder…** to begin at `~/` on that machine. The last part of the path filters folder names as you type. Use ↑ and ↓ to move through matches and Tab or Enter to open one. Backspace after a trailing slash moves up one folder. Select **Use this folder** to review the session settings, then press **Start** or Ctrl+Enter (⌘+Enter on Mac). Escape returns to the project list.

**Found on machines** shows Git repositories and folders with an `agentpack.toml` that are outside the recent list. Each gateway scans up to three levels under its home and the usual development roots within it (`projects`, `src`, `code`, `dev`, `work`, `repos`, `git`, `Developer`, and `go/src`). It skips hidden folders, symlinks and agentpack worktrees, and stops descending inside a Git repository. The Agent filter shows only folders where that agent has run.

Signed-in browser accounts can browse folders the gateway's OS user can read and start sessions in existing folders without a prior launch. A folder joins the machine's recent projects once a session runs there. Folder browsing requires an updated gateway on the selected machine.

## Conversation API

Worker routes (gateway proxies as `/api/sessions/{id}/conversation/...` with the same auth and `?node=` peer routing as other session APIs):

- `GET /conversation?since=` — NDJSON replay; `Accept: text/event-stream` or `?stream=1` for live updates. Snapshots are capped at 4 MiB and retain only complete records; `X-Agentpack-Truncated: true` marks omitted data. The gateway returns saved history when the worker is unavailable, preserving the same sequence filter and response formats. A stopped session’s SSE replay ends after its saved entries.
- `POST /conversation/prompt`, `/cancel`, `/permission`, `/elicitation`, `/config`
- `POST /conversation/revert` — body `{turn}` (1-based; a turn starts at each user prompt). Restores the turn's edited files from the worker's own log: every file must still match the turn's final text, all are checked before any is written, created files are deleted, paths must stay inside the session directory. Refused with 409 while a turn runs.
- `GET /conversation/config-options`
- `GET /blobs/{sha}` — session-scoped PNG/JPEG/GIF/WebP images, 20 MiB each and 100 MiB total or 128 images per session (whichever limit is reached first)

All writes to an existing ACP session—including prompts, cancellation, config changes, image uploads, permission/elicitation answers, rename, stop, and restart—require the session's recorded owner. Browser writes match the authenticated account email. Peers match by immutable Tailscale stable node ID and user; local transports match by user and host. Other actors can read the conversation and images but cannot modify the session; a missing owner identity is read-only.

Uploaded image MIME is detected from the stored blob bytes before the ACP agent receives it; client-supplied MIME is ignored.
A new chat's first message can carry up to 32 images, 20 MiB in total, sent inside the start request (`first_images`).
Images an agent sends in its reply, or a tool returns (an MCP screenshot, say), are moved from the event into the same blob store, so the log keeps only `sha256:` references and the chat loads them from `/blobs/{sha}`; embedded text resources show as text and links as chips (opened only for `http(s)` URLs). An image that cannot be stored (over the caps, not PNG/JPEG/GIF/WebP, invalid) is shown as a short note instead and never fails the turn. Subagent updates the worker logs raw keep their content as sent. Terminal content blocks are not rendered; shell output comes from the tool's `output`/`rawOutput`.
ACP terminal output is capped at 4 MiB; nested terminal process groups are terminated on release or worker shutdown. Complete stdout frames over 4 MiB are dropped; an unterminated oversized frame closes the stream after 10 seconds.
Session stop/restart are distinct from turn cancel: stop terminates the ACP adapter process; restart replaces it and reloads the native session. Both retain the existing session owner checks.

A session is stale when the environment it launched with (`internal/network/environment.go`: the mode-filtered package files, `.agents`, hooks, MCP servers and the MCP connections that reroute them) differs from the current one. Each worker records it at start. Package and MCP changes, Library restores and MCP connection changes reload this node's stale sessions: a chat once it is idle with nothing queued, no goal wanting a turn and no background work it will wake for, and a terminal once its agent's hooks report it idle, relaunched with `claude --resume`/`codex resume` on its own session. The pending reload is kept in the session directory (`restart_pending`) and shows the session as restarting. Before a prompt to an idle chat the worker compares the launch fingerprint and, only when that moved, the environment, so an edit nothing announced (a skill changed in an editor) reloads first. A reload resumes the same conversation and adds one notice such as `Reloaded: +skill canvas-design, MCP github connected`; a failed one leaves the chat open with a notice, and its next message retries. After an agent signs in, here or on a peer, the gateway restarts this node's running chats of that agent whose last word was a sign-in request.

The wire log stores raw ACP `session/update` kinds plus agentpack kinds such as `permission_request` and `turn_end`. Shared fixtures live in `testdata/acp/full.jsonl`; reducers in Go (`internal/acp`) and the gateway UI (`ui/src/features/conversation/reducer.ts`) must stay aligned. Optional local check: `./scripts/acp-conformance/run`.
The script does not launch agents. It reports missing `testdata/acp/<harness>.jsonl` transcripts; recording them requires owner-authorized live prompts and terminal captures and may incur provider usage.

## Switching agents and permissions

**New session** offers model and reasoning choices from the agent's last reported options. Type `/` for commands advertised in that project or `@` for tracked Git paths. The worker applies the selected choices before sending the first message to the agent. On a machine without a saved option list, open the chat and choose from the agent's live options before sending. Use **⌘Enter** or **Ctrl+Enter** to start a session or submit a question card with a Submit button.

The agent button in the composer continues a chat with another agent (`POST /api/sessions/{id}/harness`). The old agent stops; the chat keeps its ID and transcript, and the new agent starts a fresh session whose first prompt carries that transcript (messages and tool titles, no thoughts or subagent output). With agentpack tools on, only the newest 32 KiB is inlined and the prompt names the `read_conversation` tool and session ID for the rest, which stays in `state.db`; without tools the newest 96 KiB is inlined.

**Enter** queues a message while the agent works. You can edit or remove it while queued. **Steer** (⌘Enter or Ctrl+Enter) and **Send now** on a queued message send into the current turn with Claude, Codex and Antigravity CLI 1.5.0 or later. Enter on an empty composer sends the oldest queued message now, like its **Send now**. Commands the agent has already dispatched can still finish. The composer says when steering will interrupt an agent. Claude can also take ordinary queued messages at a model-cycle boundary inside a long turn. A message the agent has accepted but has yet to take in shows as Delivered.

While a permission request or question is open, whatever the agent, **Enter** queues and a message is never steered into the waiting turn. **Steer**, ⌘Enter and **Send now** dismiss the request, cancel the turn and send the message as the next prompt. Messages from other agents and goal pause or clear wait in the queue; Claude still receives agent messages during the turn and holds them itself. A question an agent raised in a turn it started itself (woken by its own background work) is dismissed the same way, by cancelling that turn. A request that stays open after its turn ended, such as an MCP sign-in link, has no turn to cancel: messages queue until **Done** or **Decline**.

Codex keeps an active goal running when you queue or steer a message. Use the goal's Pause control or `/goal pause` to pause it, and `/goal resume` to resume. A regular message can pause a Claude goal that is blocking the turn from ending. A native goal whose check blocks stop three times with nearly the same reason and no tool calls is marked blocked with a "Goal unmet" notice. A refused sign-in marks an active goal blocked with "sign-in required"; resume it after signing in. A met goal is cleared when you send your next message.

In **Settings → Agents**, the gateway owner can enable **Bypass permissions by default** for chats across enrolled machines, or turn on **Auto-approve helper chats** for chats started as helpers. The defaults apply when a chat starts or resumes. You can still set a permission mode for an individual chat in its composer. When neither default applies, a chat uses the last permission mode chosen for its agent.

Agentpack uses native bypass modes where available, including Claude's Bypass Permissions and Codex's Full access. In Cursor and Antigravity, Agentpack offers a Bypass Permissions choice and answers tool permission requests itself. Built-in subagents share their chat's approval policy. Questions, plans that need approval, and MCP sign-ins still require a response.

## Opening the web UI

On a TTY, bare `agentpack` and the `ap` symlink (installed beside the binary) print the running gateway's URL and open it in the default browser; inside an agent session they only print it. Chats, permission answers, queued prompts, goals and image attachments all live in the gateway web UI. When no gateway is running, the command says to start one with `ap gateway start`.

## Signing agents in

An agent that isn't signed in can be signed in from the web UI, from any device, without a terminal on its machine. A chat that fails because its agent needs a sign-in shows a sign-in card instead of the error, and the machine page's Agents table offers **Sign in…** next to each agent that isn't signed in. The gateway runs the agent's own login command on that machine in a hidden terminal (`internal/gateway/agent_login.go`), shows the link and one-time code it prints, and types back what you paste. Nothing opens a browser on the machine, and the credentials stay where the agent normally keeps them. Each gateway also shares those sign-in files with every mesh peer through the replicated `agent_credentials` table, so an agent signed in on one machine is signed in on all of them. A file that changed on a machine since it last installed or published one is shared with a higher revision; otherwise the latest revision is installed. A changed file whose token is older than the shared one (Claude, Codex, Cursor) is stale: the shared sign-in is installed over it instead. A sign-in made in the web UI is always shared. A replaced file that had changed is kept as `.bak`, and a file with no sign-in in it is never shared. A machine seeing a shared sign-in for the first time installs it and keeps its own file as `.bak`. File times on different machines are never compared. The shared files are Claude's `~/.claude/.credentials.json`, the Codex `auth.json` its staged homes use, Grok's shared `auth.json`, OpenCode's `auth.json` and Antigravity's `~/.gemini/antigravity-cli/antigravity-oauth-token` and Cursor's file-store sign-in. On macOS, Claude and Antigravity read the copied file only while the keychain holds no sign-in of their own, and a Mac's keychain sign-in is not shared. OMP sign-ins are not files another machine can use, so they are not shared. Claude and Codex refresh tokens are single-use, so one gateway renews the current sign-in: Main when a Main exists, otherwise the gateway that published it. It renews Claude an hour before its access token expires and Codex seven days after its last refresh, ahead of the agents' own refresh, so two machines do not spend the same token. It first catches up after a start or wake: one successful state pull (from Main, when another machine is Main), or 3 minutes without one. While Main is off no gateway renews, and no other takes over; the agents then refresh themselves, and the age check above keeps their older files from replacing a newer sign-in. **Sign out…** in the web UI stops sharing that sign-in but leaves other machines' copies in place; nothing is shared again, not even a peer's refreshed token or CLI sign-in, until someone uses **Sign in…** in the web UI.

**Settings → Vault** shows every agent's sign-in on the mesh in one place: whether it is shared, which gateway renews it and when it expires, with **Sign in…**, **Sign out…** and a per-machine view. It reads `GET /api/agents/shared` (owner only), built from the replicated rows; it returns metadata only, never a sign-in file or its hash. `renewer_node` is Main when a Main exists, otherwise the publisher, and only for agents agentpack renews. A failed renewal is reported by the gateway that attempted it; other gateways show the expiry once it passes.

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
