ALTER TABLE execution_runs ADD COLUMN recovery_phase_next TEXT;
UPDATE execution_runs SET recovery_phase_next = recovery_phase;
ALTER TABLE execution_runs DROP COLUMN recovery_phase;
ALTER TABLE execution_runs ADD COLUMN recovery_phase TEXT CHECK (
    recovery_phase IS NULL OR recovery_phase IN (
        'explore_recovery', 'explore_conclude_recovery',
        'bootstrap_recovery', 'bootstrap_conclude_recovery'
    )
);
UPDATE execution_runs SET recovery_phase = recovery_phase_next;
ALTER TABLE execution_runs DROP COLUMN recovery_phase_next;

ALTER TABLE execution_runs ADD COLUMN recovery_mode_next TEXT;
UPDATE execution_runs SET recovery_mode_next = recovery_mode;
ALTER TABLE execution_runs DROP COLUMN recovery_mode;
ALTER TABLE execution_runs ADD COLUMN recovery_mode TEXT CHECK (
    recovery_mode IS NULL OR recovery_mode IN (
        'explore_primary', 'explore_conclude',
        'bootstrap_primary', 'bootstrap_conclude'
    )
);
UPDATE execution_runs SET recovery_mode = recovery_mode_next;
ALTER TABLE execution_runs DROP COLUMN recovery_mode_next;

DROP INDEX execution_runtime_handles_active_idx;
DROP INDEX execution_runtime_handles_project_active_idx;
ALTER TABLE execution_runtime_handles RENAME TO execution_runtime_handles_before_conclude_recovery;
CREATE TABLE execution_runtime_handles (
    run_id TEXT NOT NULL REFERENCES execution_runs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    phase TEXT NOT NULL CHECK (phase IN (
        'reason', 'explore_execute', 'explore_conclude',
        'explore_recovery', 'explore_conclude_recovery',
        'bootstrap', 'bootstrap_conclude',
        'bootstrap_recovery', 'bootstrap_conclude_recovery'
    )),
    container_id TEXT NOT NULL,
    exec_id TEXT NOT NULL,
    started_at TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'stopped')),
    stopped_at TEXT,
    PRIMARY KEY (run_id, phase, exec_id)
);
INSERT INTO execution_runtime_handles (run_id, project_id, phase, container_id, exec_id, started_at, status, stopped_at)
SELECT run_id, project_id, phase, container_id, exec_id, started_at, status, stopped_at
FROM execution_runtime_handles_before_conclude_recovery;
DROP TABLE execution_runtime_handles_before_conclude_recovery;
CREATE INDEX execution_runtime_handles_active_idx
    ON execution_runtime_handles(run_id, status);
CREATE INDEX execution_runtime_handles_project_active_idx
    ON execution_runtime_handles(project_id, started_at, run_id, phase, exec_id)
    WHERE status = 'active';
