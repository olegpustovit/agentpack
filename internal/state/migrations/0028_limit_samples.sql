-- The last reading of each limit window per hour; hour as in usage_hourly, reset_at in Unix seconds.
CREATE TABLE limit_samples (
    hour INTEGER NOT NULL,
    node_id TEXT NOT NULL,
    harness TEXT NOT NULL,
    window_id TEXT NOT NULL,
    used_percent REAL,
    reset_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (hour, node_id, harness, window_id)
);
SELECT crsql_as_crr('limit_samples');
