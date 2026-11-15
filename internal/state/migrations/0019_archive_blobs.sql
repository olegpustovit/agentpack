CREATE TABLE archive_blobs (
    session_id TEXT NOT NULL,
    sha TEXT NOT NULL,
    PRIMARY KEY (session_id, sha)
);
SELECT crsql_as_crr('archive_blobs');

CREATE TABLE archive_cleanup (
    session_id TEXT NOT NULL PRIMARY KEY,
    archived_seq INTEGER NOT NULL
);
