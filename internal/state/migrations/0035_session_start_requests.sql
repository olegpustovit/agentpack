-- Only the node that received a start request can replay its claim.
CREATE TABLE session_start_requests (
    session_id TEXT NOT NULL PRIMARY KEY,
    fingerprint BLOB NOT NULL
);
