CREATE INDEX execution_runs_project_activity_idx
    ON execution_runs(project_id, started_at DESC, id DESC);
CREATE INDEX execution_runs_project_status_idx
    ON execution_runs(project_id, status, started_at DESC);
CREATE INDEX execution_runs_project_task_idx
    ON execution_runs(project_id, task_type, started_at DESC);
CREATE TABLE project_cleanup_jobs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    container_id TEXT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'leased', 'completed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT,
    lease_fencing_token INTEGER,
    lease_expires_at TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE project_cleanup_handles (
    job_id TEXT NOT NULL REFERENCES project_cleanup_jobs(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL,
    phase TEXT NOT NULL,
    container_id TEXT NOT NULL,
    exec_id TEXT NOT NULL,
    PRIMARY KEY (job_id, run_id, phase, exec_id)
);

CREATE INDEX project_cleanup_jobs_status_idx
    ON project_cleanup_jobs(status, created_at, id);
