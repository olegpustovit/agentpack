CREATE TABLE agent_snapshots (
    node_id TEXT NOT NULL,
    harness TEXT NOT NULL,
    definition_root TEXT NOT NULL,
    mode TEXT NOT NULL,
    agent_version TEXT NOT NULL,
    snapshot TEXT NOT NULL DEFAULT '{}',
    updated_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, harness, definition_root, mode, agent_version)
);
SELECT crsql_as_crr('agent_snapshots');
