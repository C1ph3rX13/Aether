-- Existing Change rows are notifications, not reconstructable History. Replace
-- them with one truthful current-state baseline without changing business
-- Revision. Revision-zero Projects remain without a replayable frame until
-- their first future business command.
DELETE FROM blackboard_changes;

INSERT INTO blackboard_changes (
    project_id, revision, generation, kind, entity_type, entity_id, data, created_at
)
SELECT
    p.id,
    p.blackboard_revision,
    p.generation,
    'project_baseline',
    'project',
    p.id,
    json_object(
        'schema_version', 1,
        'source', json_object('kind', 'migration', 'name', '0013_project_history_baseline'),
        'before', NULL,
        'after', json_object(
            'project', json_object(
                'id', p.id,
                'title', p.title,
                'status', p.status,
                'bootstrap_enabled', json(CASE WHEN p.bootstrap_enabled = 1 THEN 'true' ELSE 'false' END),
                'created_at', p.created_at,
                'generation', p.generation,
                'revision', p.blackboard_revision,
                'reason', CASE
                    WHEN p.reason_worker IS NULL THEN NULL
                    ELSE json_object(
                        'worker', p.reason_worker,
                        'trigger', p.reason_trigger,
                        'started_at', p.reason_started_at,
                        'last_heartbeat_at', p.reason_last_heartbeat_at
                    )
                END
            ),
            'facts', json(COALESCE((
                SELECT json_group_array(json_object(
                    'id', f.id,
                    'description', f.description,
                    'status', CASE WHEN f.retracted_at IS NULL THEN 'active' ELSE 'retracted' END,
                    'supersedes', f.supersedes_fact_id,
                    'retracted_at', f.retracted_at,
                    'retracted_by', f.retracted_by,
                    'retraction_reason', f.retraction_reason,
                    'replacement_fact_id', f.replacement_fact_id
                ))
                FROM facts f WHERE f.project_id = p.id
            ), '[]')),
            'intents', json(COALESCE((
                SELECT json_group_array(json_object(
                    'id', i.id,
                    'description', i.description,
                    'creator', i.creator,
                    'from', json(COALESCE((
                        SELECT json_group_array(src.fact_id)
                        FROM intent_sources src
                        WHERE src.project_id = i.project_id AND src.intent_id = i.id
                    ), '[]')),
                    'to', i.to_fact_id,
                    'worker', i.worker,
                    'last_heartbeat_at', i.last_heartbeat_at,
                    'created_at', i.created_at,
                    'concluded_at', i.concluded_at,
                    'cancelled_at', i.cancelled_at,
                    'cancelled_by', i.cancelled_by,
                    'cancellation_reason', i.cancellation_reason
                ))
                FROM intents i WHERE i.project_id = p.id
            ), '[]')),
            'hints', json(COALESCE((
                SELECT json_group_array(json_object(
                    'id', h.id,
                    'content', h.content,
                    'creator', h.creator,
                    'created_at', h.created_at,
                    'generation', h.generation,
                    'withdrawn_at', h.withdrawn_at,
                    'withdrawn_by', h.withdrawn_by,
                    'withdrawal_reason', h.withdrawal_reason
                ))
                FROM hints h WHERE h.project_id = p.id
            ), '[]')),
            'completion_records', json(COALESCE((
                SELECT json_group_array(json_object(
                    'generation', c.generation,
                    'intent_id', c.intent_id,
                    'description', c.description,
                    'actor', c.actor,
                    'completed_at', c.completed_at
                ))
                FROM project_completions c WHERE c.project_id = p.id
            ), '[]'))
        )
    ),
    strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM projects p
WHERE p.blackboard_revision > 0;
