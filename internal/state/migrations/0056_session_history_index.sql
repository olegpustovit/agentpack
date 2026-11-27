CREATE INDEX sessions_history ON sessions(last_used DESC, id DESC)
WHERE hidden=0 AND transport='acp';
