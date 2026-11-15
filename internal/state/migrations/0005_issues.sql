CREATE TABLE issues (
    fingerprint TEXT NOT NULL PRIMARY KEY,
    kind TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    first_seen INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'open',
    regressed INTEGER NOT NULL DEFAULT 0,
    resolved_at INTEGER NOT NULL DEFAULT 0,
    resolved_by TEXT NOT NULL DEFAULT '',
    resolved_version TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    sample TEXT NOT NULL DEFAULT ''
);
CREATE TABLE issue_counts (
    fingerprint TEXT NOT NULL,
    node_id TEXT NOT NULL,
    count INTEGER NOT NULL DEFAULT 0,
    first_seen INTEGER NOT NULL DEFAULT 0,
    last_seen INTEGER NOT NULL DEFAULT 0,
    last_version TEXT NOT NULL DEFAULT '',
    last_trace BLOB,
    PRIMARY KEY(fingerprint, node_id)
);
-- Local publication cursors make count deltas idempotent across process restarts.
CREATE TABLE issue_log_cursors (
    day TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    node_id TEXT NOT NULL,
    version TEXT NOT NULL,
    count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(day, fingerprint, node_id, version)
);
