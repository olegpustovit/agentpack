CREATE TABLE cache_locations (
    cache_key TEXT NOT NULL,
    node_id TEXT NOT NULL,
    content_hash TEXT NOT NULL DEFAULT '',
    size INTEGER NOT NULL DEFAULT 0,
    verified_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(cache_key,node_id)
);
CREATE TABLE blob_locations (
    sha TEXT NOT NULL,
    node_id TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    size INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(sha,node_id)
);
SELECT crsql_as_crr('cache_locations');
SELECT crsql_as_crr('blob_locations');
