# State migrations

Migrations are contiguous, additive SQL files. Replicated tables may only gain
columns with defaults; local authentication and synchronization cursors never
become replicated tables.

When altering an existing replicated table, start the file with
`-- crsql-alter: <table>`. The migration runner's `alterCRR` helper wraps its SQL
in `crsql_begin_alter` and `crsql_commit_alter`, inside the version transaction.
Use one replicated table per alteration file. Creating a new table uses ordinary
SQL followed by `SELECT crsql_as_crr('<table>')` when it should replicate.

Machine-local account bindings and pending joins live in local_node. Browser
session epochs stay local; mesh_settings retains the shared revocation epoch
even after users are deleted. Do not reset that key when reclaiming an owner.

archive_event_clocks replicates conflict positions, not chat bodies. Keep each
clock in one JSON cell so a Main handover cannot mix fields from different
positions. Archive pruning records original clocks before tombstones. Sync writes immutable
archive_edits backups in one transaction before applying a page that could absorb
a newer body. The received page and cursor commit together. Main deletes each backup only after its archive commits it. These
independent backups may survive a rejected page and are safe to relay or retry.
archive_blobs retains required attachment references; archive_cleanup is local. sync_quarantine is also local and records rejected
incoming change versions without copying their values.


mcp_credentials and mcp_credential_approvals replicate MCP logins, secrets
included, on purpose: one login serves every node. Mutable token state stays in
the single `state` JSON cell so a refresh is never mixed with another node's.

Registry public objects, names, heads, tombstones and drafts are replicated.
registry_bindings and registry_peers are local: raw configuration secrets and
machine trust must never enter a change page. Draft bodies store stable target
node IDs; local aliases are applied only at the Store API boundary.

The pinned extension keeps per-author db_version counters. The transport uses a
monotonic database-wide counter instead: applied source-version groups receive
new local transport versions, preserving author, column version, lifetime and
sequence. The local persisted counter advances atomically with data and cursor.
Writer connections are retired between transactions so the next native write
reloads that counter. Never derive the high-water mark from surviving clock rows:
overwriting the last row of an old version must not move a peer's cursor backward.

push_keys and push_subscriptions replicate Web Push VAPID key pairs and browser
endpoints, secrets included, so Main can notify every device. The oldest key pair
(created_at, then public_key) is current; each subscription records the key it was
made with, and its creating node for meshes without a Main.

omp_settings replicates network-wide Oh My Pi settings, one sparse JSON row per changed key.
Each gateway renders the rows into its own `omp/settings.yml` overlay.

agent_credentials replicates agent sign-in files, secrets included, so signing an
agent in on one node signs it in on all of them. The newest file wins; a sign-out
from the web UI leaves a newer empty row that stops sharing without deleting other
nodes' files.
