-- Local diagnostics for rejected incoming state changes.
CREATE TABLE sync_quarantine (
    peer BLOB NOT NULL,
    version INTEGER NOT NULL,
    reason TEXT NOT NULL,
    PRIMARY KEY (peer, version, reason)
);
