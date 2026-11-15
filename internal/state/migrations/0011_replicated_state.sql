CREATE TABLE mesh_nodes (
    node_id TEXT NOT NULL PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    site_id BLOB,
    schema_version INTEGER NOT NULL DEFAULT 0,
    app_version TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT 0
);
SELECT crsql_as_crr('users');
SELECT crsql_as_crr('node_settings');
SELECT crsql_as_crr('sessions');
SELECT crsql_as_crr('conversation_events');
SELECT crsql_as_crr('limits');
SELECT crsql_as_crr('mesh_nodes');
SELECT crsql_as_crr('issues');
SELECT crsql_as_crr('issue_counts');
