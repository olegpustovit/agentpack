CREATE TABLE mcp_credentials (
    id TEXT NOT NULL PRIMARY KEY,
    match TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT '',
    label TEXT NOT NULL DEFAULT '',
    account TEXT NOT NULL DEFAULT '',
    client TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT '',
    created_node TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT 0
);
SELECT crsql_as_crr('mcp_credentials');
CREATE TABLE mcp_credential_approvals (
    credential_id TEXT NOT NULL,
    module TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (credential_id, module)
);
SELECT crsql_as_crr('mcp_credential_approvals');
