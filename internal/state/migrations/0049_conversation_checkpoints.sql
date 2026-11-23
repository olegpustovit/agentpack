-- Local, never replicated: the owning worker's fold of a chat's log at seq, written in the same
-- transaction as the events up to seq, so a restarted worker resumes from it instead of re-reading the tail.
CREATE TABLE conversation_checkpoints (
    session_id TEXT NOT NULL PRIMARY KEY,
    seq INTEGER NOT NULL,
    body TEXT NOT NULL
);
