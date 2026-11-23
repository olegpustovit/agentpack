-- Local record of the sign-in file this machine last installed or published, per agent_credentials
-- key: a file whose hash still matches is unchanged here, so the row's higher rev wins without
-- comparing clocks across machines.
CREATE TABLE agent_credential_installs (
    key TEXT NOT NULL PRIMARY KEY,
    hash BLOB NOT NULL,
    rev INTEGER NOT NULL
);
