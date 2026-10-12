ALTER TABLE projects ADD COLUMN generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0);
ALTER TABLE projects ADD COLUMN blackboard_revision INTEGER NOT NULL DEFAULT 0 CHECK (blackboard_revision >= 0);
ALTER TABLE hints ADD COLUMN generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0);
UPDATE projects SET status = 'paused' WHERE status = 'stopped';

CREATE TABLE project_completions (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL CHECK (generation > 0),
    intent_id TEXT NOT NULL,
    description TEXT NOT NULL,
    actor TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    PRIMARY KEY (project_id, generation),
    UNIQUE (project_id, intent_id),
    FOREIGN KEY (intent_id, project_id) REFERENCES intents(id, project_id) ON DELETE RESTRICT
);

INSERT INTO project_completions (project_id, generation, intent_id, description, actor, completed_at)
SELECT p.id,
       p.generation,
       i.id,
       i.description,
       i.creator,
       COALESCE(i.concluded_at, i.created_at)
FROM projects p
JOIN intents i ON i.project_id = p.id AND i.to_fact_id = 'goal'
WHERE p.status = 'completed'
  AND i.id = (
      SELECT i2.id
      FROM intents i2
      WHERE i2.project_id = p.id AND i2.to_fact_id = 'goal'
      ORDER BY COALESCE(i2.concluded_at, i2.created_at) DESC, i2.id DESC
      LIMIT 1
  );
