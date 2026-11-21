-- Agent sign-in files (Claude, Codex, Grok, OpenCode, Antigravity), secrets included, so one
-- sign-in serves every node. key is "<harness>/<file name>"; value is one JSON cell
-- {"data","modified"} so a file and its time never mix across nodes.
CREATE TABLE agent_credentials (
    key TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
SELECT crsql_as_crr('agent_credentials');
