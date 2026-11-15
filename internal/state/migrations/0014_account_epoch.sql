CREATE TABLE mesh_settings (
    key TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
SELECT crsql_as_crr('mesh_settings');
ALTER TABLE auth_sessions ADD COLUMN session_epoch INTEGER NOT NULL DEFAULT 0;
UPDATE auth_sessions SET session_epoch=coalesce((SELECT sessions_valid_after FROM users WHERE email=auth_sessions.email),0);
