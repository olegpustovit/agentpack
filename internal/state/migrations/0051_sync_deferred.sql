-- Local: peer change versions held back while the archive guard protects a live session.
CREATE TABLE sync_deferred (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    peer BLOB NOT NULL,
    version INTEGER NOT NULL,
    sessions TEXT NOT NULL,
    changes TEXT NOT NULL,
    UNIQUE (peer, version)
);
