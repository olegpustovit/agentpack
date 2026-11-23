-- crsql-alter: conversation_events
-- Virtual, so it is derived from body on every node: nothing new replicates and old peers' rows have it too.
ALTER TABLE conversation_events ADD COLUMN kind TEXT GENERATED ALWAYS AS (CASE WHEN json_valid(body) THEN json_extract(body, '$.kind') END) VIRTUAL;
CREATE INDEX conversation_events_kind ON conversation_events(session_id, kind, seq);
