CREATE TABLE configuration_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    deployment_id TEXT NOT NULL,
    draft_version INTEGER NOT NULL CHECK (draft_version > 0),
    draft_json TEXT NOT NULL CHECK (json_valid(draft_json)),
    active_revision TEXT,
    updated_at TEXT NOT NULL
);
