CREATE TABLE users (
    email TEXT NOT NULL PRIMARY KEY,
    password_hash TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'member',
    disabled INTEGER NOT NULL DEFAULT 0,
    sessions_valid_after INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE auth_sessions (
    token_hash BLOB NOT NULL PRIMARY KEY,
    email TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX auth_sessions_email ON auth_sessions(email);
CREATE INDEX auth_sessions_expiry ON auth_sessions(expires_at);
