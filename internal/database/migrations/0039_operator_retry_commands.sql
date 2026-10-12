CREATE TABLE execution_retry_commands (
    request_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    expected_revision INTEGER NOT NULL CHECK (expected_revision >= 1),
    operator_name TEXT NOT NULL CHECK (length(operator_name) > 0),
    created_at TEXT NOT NULL
);

CREATE TABLE execution_retry_resets (
    request_id TEXT NOT NULL REFERENCES execution_retry_commands(request_id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES execution_runs(id) ON DELETE CASCADE,
    previous_failure_count INTEGER NOT NULL CHECK (previous_failure_count > 0),
    PRIMARY KEY (request_id, run_id)
);

CREATE INDEX execution_retry_resets_run_idx ON execution_retry_resets(run_id, request_id);
