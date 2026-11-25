CREATE INDEX sessions_list_active ON sessions(node_id, id)
WHERE hidden=0 AND ended_at=0 AND (transport<>'shell' OR parent_id='');

CREATE INDEX sessions_list_ended ON sessions(last_used DESC, id DESC, node_id)
WHERE hidden=0 AND ended_at>0 AND transport='acp';
