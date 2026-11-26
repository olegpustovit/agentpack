# Gateway state and mesh sync

Every gateway keeps its own `$AGENTPACK_HOME/state.db`. Accounts, node settings,
sessions, conversation events, usage limits, machine metadata, mesh settings and diagnostic
issues are replicated with cr-sqlite. Archive event clocks, attachment references and pending late edits also replicate.
Main acknowledges late edits only after its archive commits them, including when
they arrive through an intermediate machine during a handover.
Reads use this local copy, so a sleeping
machine's sessions and saved conversation history remain available after a
browser reload. The last successful sync time describes freshness.

Browser login tokens, OAuth clients and tokens, synchronization cursors and local
node identity stay local. Password hashes are shared; login cookies are not.
Harness credentials, package trees, terminal output and machine process secrets
are not replicated by state sync. Public Library objects, name history, heads, deletion records and pending drafts
replicate through the same state pages. Raw Library credential bindings and peer
approvals stay local. Cache and blob bytes use separate verified transfers.

## Usage counters

The Usage page reads three replicated sources. Each session row carries its model,
turns, token counts and reported cost; `usage_hourly` sums the same counters per
hour, machine, agent, project folder and model; `limit_samples` keeps each limit
window's last reading per hour for a week. A machine writes only its own rows, so
merging never loses an increment. Counters move in the same transaction that stores
a new `turn_end` or cost update, and a replayed event counts nothing. Claude, Codex,
OMP and Antigravity report tokens per turn; Grok reports tokens and cost in its own
metadata; Claude and OMP report cost as a running total, stored as the difference
from the previous total since the worker last started (a smaller total, or a
fresh agent process, starts over). Failed turns count nothing. `usage_hourly`
has no retention (about a hundred rows per machine per day); each machine deletes
its own week-old `limit_samples`, which leaves small replicated tombstones, and a
removed peer's samples stay until its rows are cleared. Cursor
reports neither, so its turns count without tokens, and an agent that reports no
cost shows "—", never $0. Sessions from before the counters existed are not
backfilled. A gateway re-reads an agent's limits after its turns, at most every five
minutes per agent, and the Limits pane asks each online peer to refresh while open.
For each provider the pane shows the newest reading with a current window; one login
can be shared by several machines. An Antigravity window nobody has used yet has no
reset date and reads `starts on first use`.

## Trust and enrollment

A mesh member receives account password hashes and chat history. Enroll only
machines you fully trust. An owner enrolls each machine in Settings → Replication,
and the other gateway must independently enroll this one. The authenticated
machine-local administrative transport can also manage enrollment.

Existing Library-only peers must explicitly enable mesh sync. Automatic Library
discovery never grants access to shared accounts or conversations. Removing a
peer stops new sync; it does not erase copies already held by either machine.

## Join an existing mesh

On a new machine, before starting its gateway, run:

```sh
agentpack gateway join SOURCE_NODE_ID
agentpack gateway start
```

The join command asks you to type `trust`, records your local approval, and closes
the owner-claim window before the gateway starts. On the existing gateway, its
owner must independently approve this machine in Settings → Replication. Until
accounts arrive, the new gateway shows `Joining your mesh` and offers no owner
claim form. After sync, sign in with an existing account. Joining an already
claimed gateway through this command is refused; use its owner's replication
settings instead.

Local gateway actions use the mesh owner account by default. Bind this machine's
OS user to another enabled account with `agentpack gateway whoami EMAIL`.
Bindings are local and survive restarts. Disabling the bound account refuses
local account-owned actions until you bind an enabled account; it never silently
switches to owner access. Existing OS-owned native histories retain their local
ownership checks.

Owner reset records a replicated account epoch before deleting accounts. Browser
sessions keep a local copy of the epoch, and OAuth grants also check it. Recreating
the same email after reset cannot revive another node's old logins, including
when its clock is ahead.

## Offline reads and live actions

Sessions, limits and usage use one local read across machines. An offline chat displays
`Offline, read-only`, keeps its history visible, and hides the composer. Permission
answers, prompts, forks, restarts and terminal input need the owning machine.
Renaming changes shared metadata and can be done while the owner is offline.
Forwarded writes carry the signed-in account only across explicitly approved mesh
connections, and that account can modify only its own ACP conversations.

The Machines inventory is read locally. Replication settings can explicitly
discover allowed Tailscale peers to enroll a new machine. Sync liveness, rather
than a replicated heartbeat timestamp, determines whether a machine is reachable.

## Protocol and version skew

Peers pull bounded change pages with atomic cursor updates. A state notification
wakes the pull worker; a 30-second timer covers missed events. Each peer retries
independently, so one unavailable machine cannot block the others. Changes retain
their original author and can pass through intermediate machines.

Peers must use the same state schema. An older endpoint or a schema mismatch
shows `Needs update`; sync retries after the machines are updated. Migrations are
additive, and alterations to replicated tables use the cr-sqlite alteration
transaction. Foreground harness launch setup never opens state.db.

## Main gateway and archive

Choose a machine that is usually available with `ap gateway main set`. Machines
shows its `Main` badge. `ap gateway main` reports its name and whether the archive
is ready. No machine is automatically selected.

Every hour, Main moves complete conversations inactive for more than seven days
from replicated state into its private `$AGENTPACK_HOME/archive.db`. It commits
and verifies the archive copy before deleting shared events and marking the chat
archived. Incomplete tails, active turns, pending approvals and more recent chats
stay shared. Main fetches and verifies attachments before archiving. Other nodes
remove their archived attachment files only after verifying Main still has the
bytes; a paused handover or missing copy leaves local files intact.

Archived chats remain listed, render `Archived, read-only`, and cannot accept
writes, resume or fork. Any gateway reads their history through Main. If Main or
its archive is unavailable, the UI shows `Archived on <main>, unavailable right
now`. There is no restore route.

To switch Main:

1. On the new machine, run `ap gateway main set` and type `switch` after reviewing
   the old machine and both archive paths. Archiving and archived reads pause.
2. Cleanly stop the old gateway, then copy its `archive.db` to the displayed path
   on the new machine. Copy the archived sessions’ `network/sessions/<id>/blobs/`
   directories too, or restart the old and new gateways so missing attachments
   can be fetched. Do not copy a live SQLite database file.
3. On the new machine, run `ap gateway main archive-ready`. It validates the
   archive and every referenced attachment before resuming. `--empty` explicitly creates a fresh archive when the
   file is missing; it never overwrites an existing file or recovers older chats.

**Back up `archive.db` and its attachment directories separately.** The archive
is not replicated, and state.db alone cannot recover archived history. Keep the
old copies until the new archive and attachments are verified. The archive uses plain SQLite, private file permissions, WAL and FULL
synchronous commits.

## Failure traces

When Main is configured, other gateways forward retained failed events and the
other spans in those traces. Healthy traces stay on their original machines.
Forwarding requires explicit mesh approval, keeps original machine identities,
retries safely, and retains the same five-day diagnostic window. `ap logs trace`
and the MCP `get_trace` tool merge Main's retained copy; `--all-nodes` (or
`all_nodes` for MCP) also queries reachable approved peers. Raw diagnostic day
files are not replicated by cr-sqlite.

Missing packages can be restored from approved peers, with the locked content
hash checked before installation; any failure falls through to GitHub. A cached
launch never asks the gateway for package bytes. Missing chat attachments use
verified peer copies under the existing storage quota. The disposable metadata
index is plain SQLite at `cache/index.db`; it can be deleted and is rebuilt
without changing package trees or project locks.

The Library imports verified legacy files before removing them. Its effective
heads are derived from immutable revisions, so concurrent edits keep their merge
and conflict behavior. Pending drafts retain the target machine when viewed from
another gateway. Local capture settings and restore journals remain files so
launch setup and interrupted-restore recovery never open state.db.

Final release verification is still pending.
