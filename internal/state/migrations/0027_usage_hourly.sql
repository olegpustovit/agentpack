-- hour is Unix seconds at the start of the UTC hour. Each row has one writer, its own node,
-- so last-writer-wins never loses an increment.
CREATE TABLE usage_hourly (
    hour INTEGER NOT NULL,
    node_id TEXT NOT NULL,
    harness TEXT NOT NULL,
    folder_id TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    turns INTEGER NOT NULL DEFAULT 0,
    sessions INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_micros INTEGER NOT NULL DEFAULT 0,
    agent_ms INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (hour, node_id, harness, folder_id, model)
);
SELECT crsql_as_crr('usage_hourly');
