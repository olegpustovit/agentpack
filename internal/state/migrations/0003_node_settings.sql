CREATE TABLE node_settings (
    node_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(node_id, key)
);
