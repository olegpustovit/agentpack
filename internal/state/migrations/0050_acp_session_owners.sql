-- Native ACP session ownership leaves the sessions table, where it lived as hidden pseudo-rows.
-- It replicates as those rows did; each node reads only its own bindings.
CREATE TABLE acp_session_owners (
    node_id TEXT NOT NULL DEFAULT '',
    key TEXT NOT NULL DEFAULT '',
    folder_id TEXT NOT NULL DEFAULT '',
    harness TEXT NOT NULL DEFAULT '',
    started_by TEXT NOT NULL DEFAULT '',
    owner_email TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(node_id, key)
);
SELECT crsql_as_crr('acp_session_owners');
-- An id is ownership-<site hex>-<key>; import markers (empty owner) are dropped.
INSERT OR IGNORE INTO acp_session_owners(node_id,key,folder_id,harness,started_by,owner_email)
    SELECT node_id, substr(id, 11 + instr(substr(id, 11), '-')), folder_id, harness, started_by, owner_email
    FROM sessions WHERE id LIKE 'ownership-%' AND started_by<>'';
DELETE FROM sessions WHERE id LIKE 'ownership-%';
