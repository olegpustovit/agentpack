CREATE TABLE limits (
    node_id TEXT NOT NULL,
    harness TEXT NOT NULL,
    snapshot TEXT NOT NULL DEFAULT '',
    collected_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(node_id, harness)
);
