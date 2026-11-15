CREATE TABLE sessions (
    id TEXT NOT NULL PRIMARY KEY,
    last_used INTEGER NOT NULL DEFAULT 0,
    folder_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    directory TEXT NOT NULL DEFAULT '',
    definition_root TEXT NOT NULL DEFAULT '',
    harness TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL DEFAULT '',
    transport TEXT NOT NULL DEFAULT '',
    agent_session_id TEXT NOT NULL DEFAULT '',
    agent_capabilities TEXT NOT NULL DEFAULT '',
    conversation_state TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL DEFAULT 0,
    started_by TEXT NOT NULL DEFAULT '',
    node_id TEXT NOT NULL DEFAULT '',
    owner_email TEXT NOT NULL DEFAULT '',
    ended_at INTEGER NOT NULL DEFAULT 0,
    hidden INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX sessions_node ON sessions(node_id, ended_at);
CREATE INDEX sessions_recent ON sessions(node_id, hidden, last_used DESC);
CREATE INDEX sessions_agent ON sessions(folder_id, harness, agent_session_id);

CREATE TABLE conversation_events (
    session_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(session_id, seq)
);
