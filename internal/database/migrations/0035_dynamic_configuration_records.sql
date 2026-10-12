CREATE TABLE worker_secret_versions (
    id TEXT PRIMARY KEY,
    worker_name TEXT NOT NULL,
    agent_type TEXT NOT NULL,
    variable_name TEXT NOT NULL,
    secret_value TEXT NOT NULL,
    created_at TEXT NOT NULL
);
