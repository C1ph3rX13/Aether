CREATE TABLE execution_runtime_handles (
    run_id TEXT NOT NULL REFERENCES execution_runs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    phase TEXT NOT NULL CHECK (phase IN ('reason', 'explore_execute', 'explore_conclude', 'bootstrap', 'bootstrap_conclude')),
    container_id TEXT NOT NULL,
    exec_id TEXT NOT NULL,
    started_at TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'stopped')),
    stopped_at TEXT,
    PRIMARY KEY (run_id, phase, exec_id)
);

CREATE INDEX execution_runtime_handles_active_idx
    ON execution_runtime_handles(run_id, status);
