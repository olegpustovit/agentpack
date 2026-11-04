# ACP chat sessions and the `ap` hub

Some harnesses speak the [Agent Client Protocol](https://agentclientprotocol.com/) over stdio. Agentpack runs them in a **worker** (same ownership model as terminal PTY sessions), exposes a **conversation log** on the gateway, and renders chat in the browser or in the **`ap`** Bubble Tea hub.

## Session transport

`GET /api/sessions` includes optional fields:

| Field | Meaning |
|---|---|
| `transport` | `terminal` (default) or `acp` |
| `conversation_state` | `working`, `needs_you`, or `idle` while the worker is running |
| `agent_session_id` | Agent-native session id for resume |

Start chat with `POST /api/sessions` and `{ "transport": "acp", ... }`. Resume with `agent_session_id` from `GET /api/agent-sessions?directory=&harness=&mode=`. Switching Chat ↔ Terminal in the UI stops the current worker and starts the other transport; resume depends on the agent.

## Conversation API

Worker routes (gateway proxies as `/api/sessions/{id}/conversation/...` with the same auth and `?node=` peer routing as other session APIs):

- `GET /conversation?since=` — NDJSON replay; `Accept: text/event-stream` or `?stream=1` for live updates. Snapshots are capped at 4 MiB and retain only complete records; `X-Agentpack-Truncated: true` marks omitted data.
- `POST /conversation/prompt`, `/cancel`, `/permission`, `/elicitation`, `/config`
- `GET /conversation/config-options`
- `GET /blobs/{sha}` — session-scoped PNG/JPEG/GIF/WebP images, 20 MiB each and 100 MiB total or 128 images per session (whichever limit is reached first)

All writes to an existing ACP session—including prompts, cancellation, config changes, image uploads, permission/elicitation answers, rename, stop, and restart—require the session's recorded user and node identity. Peers match by immutable Tailscale stable node ID; local transports match by user and host. Other actors can read the conversation and images but cannot modify the session; a missing owner identity is read-only.

Uploaded image MIME is detected from the stored blob bytes before the ACP agent receives it; client-supplied MIME is ignored.
ACP terminal output is capped at 4 MiB; nested terminal process groups are terminated on release or worker shutdown. Complete stdout frames over 4 MiB are dropped; an unterminated oversized frame closes the stream after 10 seconds.
Session stop/restart are distinct from turn cancel: stop terminates the ACP adapter process; restart replaces it and reloads the native session. Both retain the existing session owner checks.

The wire log stores raw ACP `session/update` kinds plus agentpack kinds such as `permission_request` and `turn_end`. Shared fixtures live in `testdata/acp/full.jsonl`; reducers in Go (`internal/acp`, `internal/hub`) and the gateway UI (`ui/src/features/conversation/reducer.ts`) must stay aligned. Optional local check: `./scripts/acp-conformance/run`.
The script does not launch agents. It reports missing `testdata/acp/<harness>.jsonl` transcripts; recording them requires owner-authorized live prompts and terminal captures and may incur provider usage.

## `ap` hub

On a TTY, bare `agentpack` and the `ap` symlink (installed beside the binary) open the hub TUI instead of printing help. Without a gateway URL, the hub lists local worker records only. The preview supports permission answers, elicitation choices, text prompts, image attachments from a local path, and the `/help`, `/image`, `/reload`, and `/sessions` slash commands. Image/file blocks render as text placeholders; click one or hover it and press `o` to open the stored blob with the OS default file handler. Downloaded images are cached under the OS user cache, capped at 100 MiB.

## Harness coverage

Each harness implements `Harness.ACP` under `internal/harness/<name>/acp.go`. Claude and Codex pin npm ACP adapters under `$AGENTPACK_HOME/adapters/` (Node required for chat). Claude ACP forwards staged plugin directories and the staged `agentpack-settings.json` as session metadata, without redirecting `CLAUDE_CONFIG_DIR`; `CLAUDE_CODE_EXECUTABLE` is honored, with `CLAUDE_CODE_PATH` as its fallback. Adapter specs and verified trees are cached; intact warm caches need no registry lookup, while changed trees are re-fetched against their pinned integrity. Grok may require prior login; do not launch Grok solely to discover auth state.

See `ui/README.md` for the full HTTP contract and `CHANGELOG.md` for release notes.
