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
archive_edits backups before applying a page that could absorb a newer body. Each
backup is a separate bounded source version; the received page and cursor remain
atomic. Main deletes each backup only after its archive commits it. These
independent backups may survive a rejected page and are safe to relay or retry.
archive_blobs retains required attachment references; archive_cleanup is local.


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
