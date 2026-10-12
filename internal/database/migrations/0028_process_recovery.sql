ALTER TABLE execution_runs ADD COLUMN recovery_used INTEGER NOT NULL DEFAULT 0 CHECK (recovery_used IN (0, 1));
ALTER TABLE execution_runs ADD COLUMN recovery_phase TEXT CHECK (recovery_phase IS NULL OR recovery_phase IN ('explore_recovery', 'explore_conclude_recovery'));
ALTER TABLE execution_runs ADD COLUMN recovery_started_at TEXT;

DROP INDEX execution_runtime_handles_active_idx;
DROP INDEX execution_runtime_handles_project_active_idx;
ALTER TABLE execution_runtime_handles RENAME TO execution_runtime_handles_before_recovery;
CREATE TABLE execution_runtime_handles (
    run_id TEXT NOT NULL REFERENCES execution_runs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    phase TEXT NOT NULL CHECK (phase IN ('reason', 'explore_execute', 'explore_conclude', 'explore_recovery', 'explore_conclude_recovery', 'bootstrap', 'bootstrap_conclude')),
    container_id TEXT NOT NULL,
    exec_id TEXT NOT NULL,
    started_at TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'stopped')),
    stopped_at TEXT,
    PRIMARY KEY (run_id, phase, exec_id)
);
INSERT INTO execution_runtime_handles (run_id, project_id, phase, container_id, exec_id, started_at, status, stopped_at)
SELECT run_id, project_id, phase, container_id, exec_id, started_at, status, stopped_at
FROM execution_runtime_handles_before_recovery;
DROP TABLE execution_runtime_handles_before_recovery;
CREATE INDEX execution_runtime_handles_active_idx
    ON execution_runtime_handles(run_id, status);
CREATE INDEX execution_runtime_handles_project_active_idx
    ON execution_runtime_handles(project_id, started_at, run_id, phase, exec_id);
