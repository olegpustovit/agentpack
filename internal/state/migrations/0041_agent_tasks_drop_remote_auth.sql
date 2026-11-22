-- call_agent no longer stores an agent's token with its task: it uses the owner's MCP connection for the URL.
ALTER TABLE agent_tasks DROP COLUMN remote_auth;
