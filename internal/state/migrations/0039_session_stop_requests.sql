-- A retry can stop only the worker that received the first request.
CREATE TABLE session_stop_requests (
    session_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    worker_fingerprint BLOB NOT NULL,
    PRIMARY KEY (session_id, request_id)
);
