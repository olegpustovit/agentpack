CREATE TABLE oauth_clients (
    secret_hash TEXT NOT NULL PRIMARY KEY,
    metadata TEXT NOT NULL DEFAULT ''
);
CREATE TABLE oauth_tokens (
    secret_hash TEXT NOT NULL PRIMARY KEY,
    kind TEXT NOT NULL DEFAULT '',
    expires_at INTEGER NOT NULL DEFAULT 0,
    client_id TEXT NOT NULL DEFAULT '',
    redirect_uri TEXT NOT NULL DEFAULT '',
    challenge TEXT NOT NULL DEFAULT '',
    resource TEXT NOT NULL DEFAULT '',
    family TEXT NOT NULL DEFAULT '',
    grant_expires_at INTEGER NOT NULL DEFAULT 0,
    query TEXT NOT NULL DEFAULT '',
    session TEXT NOT NULL DEFAULT ''
);
CREATE INDEX oauth_tokens_expiry ON oauth_tokens(expires_at);
CREATE INDEX oauth_tokens_family ON oauth_tokens(family);
