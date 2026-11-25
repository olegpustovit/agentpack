CREATE TABLE agent_defaults (
    key TEXT NOT NULL PRIMARY KEY,
    value INTEGER NOT NULL DEFAULT 1
);
SELECT crsql_as_crr('agent_defaults');
