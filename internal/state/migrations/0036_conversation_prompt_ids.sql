CREATE INDEX conversation_prompt_ids ON conversation_events (session_id, json_extract(body, '$.payload.queueId'))
WHERE json_valid(body) AND json_extract(body, '$.kind') IN ('queued_prompt', 'prompt', 'dequeued_prompt');
