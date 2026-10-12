CREATE TEMP TABLE bootstrap_intent_backfill_projects (
    project_id TEXT PRIMARY KEY
);

INSERT INTO bootstrap_intent_backfill_projects (project_id)
SELECT p.id
FROM projects p
WHERE p.status IN ('active', 'paused')
  AND p.bootstrap_enabled = 1
  AND p.generation = 1
  AND NOT EXISTS (SELECT 1 FROM intents i WHERE i.project_id = p.id)
  AND EXISTS (
      SELECT 1 FROM facts f
      WHERE f.project_id = p.id AND f.id = 'origin' AND f.retracted_at IS NULL
  )
  AND EXISTS (
      SELECT 1 FROM facts f
      WHERE f.project_id = p.id AND f.id = 'goal' AND f.retracted_at IS NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM facts f
      WHERE f.project_id = p.id
        AND f.retracted_at IS NULL
        AND f.id NOT IN ('origin', 'goal')
  );

INSERT INTO scoped_counters (project_id, kind, value)
SELECT project_id, 'intent', 1
FROM bootstrap_intent_backfill_projects
WHERE 1
ON CONFLICT(project_id, kind) DO UPDATE SET value = MAX(value, 1);

INSERT INTO intents (
    id, project_id, description, creator, worker, to_fact_id,
    concluded_at, last_heartbeat_at, created_at, sources_locked
)
SELECT 'i001', p.id, 'bootstrap', 'dispatcher.bootstrap', NULL, NULL,
       NULL, NULL, p.created_at, 0
FROM projects p
JOIN bootstrap_intent_backfill_projects backfill ON backfill.project_id = p.id;

INSERT INTO intent_sources (intent_id, project_id, fact_id)
SELECT 'i001', project_id, 'origin'
FROM bootstrap_intent_backfill_projects;

UPDATE intents
SET sources_locked = 1
WHERE id = 'i001'
  AND project_id IN (SELECT project_id FROM bootstrap_intent_backfill_projects);

UPDATE projects
SET blackboard_revision = blackboard_revision + 1
WHERE id IN (SELECT project_id FROM bootstrap_intent_backfill_projects);

INSERT INTO blackboard_changes (
    project_id, revision, generation, kind, entity_type, entity_id, data, created_at
)
SELECT p.id, p.blackboard_revision, p.generation, 'intent_created', 'intent', 'i001',
       '{"bootstrap_intent_id":"i001","migration":"v9"}',
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM projects p
JOIN bootstrap_intent_backfill_projects backfill ON backfill.project_id = p.id;

INSERT OR REPLACE INTO reason_scheduling_baselines (
    project_id, generation, input_revision, created_at
)
SELECT p.id, p.generation, p.blackboard_revision,
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM projects p
JOIN bootstrap_intent_backfill_projects backfill ON backfill.project_id = p.id;

DROP TABLE bootstrap_intent_backfill_projects;
