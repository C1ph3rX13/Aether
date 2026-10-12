CREATE TABLE reason_scheduling_baselines (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL CHECK (generation > 0),
    input_revision INTEGER NOT NULL CHECK (input_revision >= 0),
    created_at TEXT NOT NULL,
    PRIMARY KEY (project_id, generation)
);

INSERT INTO reason_scheduling_baselines (project_id, generation, input_revision, created_at)
SELECT p.id, p.generation, p.blackboard_revision, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM projects p
WHERE p.status = 'active'
  AND EXISTS (
      SELECT 1 FROM intents i
      WHERE i.project_id = p.id
        AND i.to_fact_id IS NULL
        AND i.cancelled_at IS NULL
  );

CREATE INDEX execution_runs_work_attempt_idx
    ON execution_runs(work_key, attempt);
