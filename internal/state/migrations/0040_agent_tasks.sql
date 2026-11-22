-- Messages between chats' agents and A2A agents (ask/reply). Local: the asking node keeps the 'out'
-- row, the asked node the 'in' row; one node running both chats keeps both. from_node 'a2a' marks a
-- task an external A2A client sent; to_node 'a2a' one this chat sent to the A2A agent at remote_url,
-- whose task ID is to_session. remote_auth is the bearer token for that agent; it never leaves this node.
CREATE TABLE agent_tasks (
    id TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('in', 'out')),
    context_id TEXT NOT NULL DEFAULT '',
    from_session TEXT NOT NULL,
    from_node TEXT NOT NULL DEFAULT '',
    to_session TEXT NOT NULL,
    to_node TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    request TEXT NOT NULL DEFAULT '',
    result TEXT NOT NULL DEFAULT '',
    hops INTEGER NOT NULL DEFAULT 0,
    since_seq INTEGER NOT NULL DEFAULT 0,
    operation TEXT NOT NULL DEFAULT '',
    delivered INTEGER NOT NULL DEFAULT 1,
    remote_url TEXT NOT NULL DEFAULT '',
    remote_auth TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (id, role)
);
CREATE INDEX agent_tasks_open ON agent_tasks (role, state, delivered);
