-- This identity and its old aliases are local, never replicated.
CREATE TABLE local_node (
    id INTEGER NOT NULL PRIMARY KEY DEFAULT 1,
    node_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE local_node_aliases (node_id TEXT NOT NULL PRIMARY KEY);
INSERT INTO local_node(id,node_id) VALUES (1,'site:' || lower(hex(crsql_site_id())));
INSERT INTO local_node_aliases(node_id) VALUES ('local');
UPDATE node_settings SET node_id=(SELECT node_id FROM local_node WHERE id=1) WHERE node_id='local';
UPDATE limits SET node_id=(SELECT node_id FROM local_node WHERE id=1) WHERE node_id='local';
UPDATE sessions SET node_id=(SELECT node_id FROM local_node WHERE id=1) WHERE node_id='local';
UPDATE issue_counts SET node_id=(SELECT node_id FROM local_node WHERE id=1) WHERE node_id='local';
-- Publication cursors keep their original day-file identity to avoid counting twice.
