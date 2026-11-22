-- A2A was removed: agents message each other through the agentpack MCP tools only.
DROP TABLE a2a_push_configs;
ALTER TABLE agent_tasks DROP COLUMN remote_url;
