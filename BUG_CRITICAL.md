# BUG_CRITICAL: steering kills background subagents; queued and agent messages wait for the whole turn

Reported by the owner on 2026-11-23 during a Claude chat running a native `/goal` with background workers. It cost three rounds of in-progress work from four background Claude workers.

## What happens

1. **Agent messages and queued prompts wait for the whole goal.** An `ask`/`reply` answer from another chat, or a message the owner types, shows as `Queued: …` until the recipient's turn ends. Under Claude Code's native `/goal` the Stop hook keeps one ACP prompt turn open for the entire goal, so "turn end" means "goal done": minutes to hours.
2. **"Steer now" kills every background subagent.** The only way to get the queued message in is Steer now. Steer sends `session/cancel` and then the prompt. Claude Code treats the cancel as an interrupt (`[Request interrupted by user]`) and stops every running background subagent (Agent tool with `run_in_background`); their task notifications read "stopped by the user or by the app". Their uncommitted work is left behind and they do not resume. Observed three times on 2026-11-23 (08:2x UTC), four workers each time.
3. **Background subagents do not show in the session tree.** The chat showed "5 subagents running" while the sidebar tree listed none. Commit `4f1e3d2` ("Show Claude subagents in the session tree") translates AIR's draft `subagent_spawned`/`subagent_state_update`; check whether the running gateway has it (it landed after v0.14.4) and whether Agent-tool background tasks emit those updates at all.

   **Update 08:45 UTC:** helper *chats* are missing too. `list_sessions` reports six children with `parent_id` = the coordinating chat (four Claude, two Codex), but the sidebar tree only nests the Codex ones; the Claude helper chats started with `start_session` do not appear under their parent.
4. **The six-helper cap counts finished helpers.** `start_session` refuses with "this chat already runs 6 helpers; stop one first" while some of those helpers are idle with their task completed. A helper whose task answered should not hold a slot.

## Why

- `internal/network/conversation_queue.go`: `nextPrompt` / `maybeDrain` only send after `turn_end`. Queued prompts already rank ahead of agentpack's own gateway-engine goal turns, but a native goal is one long turn with no boundary agentpack observes.
- `afterQueued` implements steer as cancel-then-send (`"Interrupted to steer"` notice, `steer_cancel`).
- `claude-agent-acp@0.100.0` accepts a `session/prompt` during an active turn: it queues it for Claude Code, which takes it in at its next boundary and reports `TurnEvents.inserted` (see `dist/turn-events.d.ts`, `startTurn`). Agentpack never uses this, so it has to cancel to get a message in.

## Required behavior

1. **Agent messages are delivered immediately and never interrupt.** On adapters that accept a mid-turn prompt (Claude), send at once through the adapter's own queue. Elsewhere, send at the next turn boundary.
2. **Queued user prompts go after the current agent turn** (each model cycle or Stop-hook continuation counts as a boundary), never after the whole goal. They stay editable while still in agentpack's queue.
3. **Steer must not destroy background work.** On Claude, steer = hand the prompt to the adapter mid-turn (Claude Code inserts it at the next tool boundary) with no `session/cancel`. Only an explicit Stop cancels. If an adapter can only steer by cancelling, say so in the UI before doing it.
4. **The Queued chip clears once the agent takes the prompt in** (`inserted`), and shows "Delivered" between send and insertion.
5. **Background subagents appear in the session tree** while they run and after they finish.
6. **Every helper chat nests under its parent** in the tree, whatever its agent (Claude, Codex, …), using `parent_id`.
7. **The helper cap counts only helpers still working.** Idle helpers whose task completed, and stopped ones, do not hold a slot.
8. **Stopped children leave the tree.** (Owner, 10:35 UTC) A helper chat or background subagent that stopped or finished must disappear from the sidebar tree; the owner does not want intermediate children listed once they are done. Their parent keeps them reachable from its transcript (the helper card / subagent card), not from the tree.

## Tests

- A fake ACP agent holds one turn open across several cycles: an agent message reaches it before `turn_end`; a queued prompt reaches it at the next cycle boundary; a fallback adapter gets both at `turn_end`.
- Steer on a Claude chat sends no `session/cancel` (assert on the fake adapter's received methods).
- A live check with a Claude chat that runs a background Agent: steer a message in; the background agent keeps running and its result still arrives.
- Session tree fixture: Agent-tool background task updates produce child rows.

Tracked in the core repair plan as P1.33 (`.claude/plan-core.md`, artifact `ehmsft739nec`).

## Status (2026-11-23): fixed on core/integrate

| # | Fix | Where |
|---|---|---|
| 1 | Agent messages go to Claude at once through the adapter's own queue; other adapters get them at the next turn boundary | WP-B `95b17d1` |
| 2 | Queued prompts go one per model cycle (a parent `usage_update` with `cost` marks the end), editable until sent | WP-B `95b17d1` |
| 3 | Steer on Claude and Codex uses `_session/steering` (never `session/cancel`); other agents warn "Interrupt the agent and send this now" | WP-B `fb651a3` |
| 4 | "Delivered" chip between send and insertion; `prompt` entry clears it (insertion inferred: ACP v1 has no `inserted`) | WP-B `fb651a3` |
| 5 | Background Agent tasks reach the tree through the AIR subagent translation | WP-B `464a15c`, then item 8 |
| 6 | Helpers nest under their parent across folders, worktrees and machines | WP-B `46f4247` |
| 7 | The helper cap counts only helpers that are not idle | WP-B `d806c65` |
| 8 | Finished helpers and subagents leave the tree; reachable from the parent's cards | WP-T `54dac99` |

Not verified live: a real Claude background Agent surviving a steer (needs a live session after deploy).
