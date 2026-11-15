CREATE TABLE archive_edits (
    id TEXT NOT NULL PRIMARY KEY,
    edit TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT ''
);
SELECT crsql_as_crr('archive_edits');
