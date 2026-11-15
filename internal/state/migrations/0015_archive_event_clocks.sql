-- Preserve archive conflict positions across Main handovers without replicating bodies.
CREATE TABLE archive_event_clocks (
    pk BLOB NOT NULL PRIMARY KEY,
    clock TEXT NOT NULL DEFAULT ''
);
SELECT crsql_as_crr('archive_event_clocks');
