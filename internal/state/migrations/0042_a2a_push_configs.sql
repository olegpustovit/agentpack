-- Where an A2A client asked to be told about a task's state changes. Local, like agent_tasks: the
-- config may hold the client's webhook credentials. pushed is the last state delivered.
CREATE TABLE a2a_push_configs (
    task_id TEXT NOT NULL,
    id TEXT NOT NULL,
    config TEXT NOT NULL,
    pushed TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY (task_id, id)
);
