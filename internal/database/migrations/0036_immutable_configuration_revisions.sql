CREATE TABLE configuration_revisions (
    id TEXT PRIMARY KEY,
    document_json TEXT NOT NULL CHECK (json_valid(document_json)),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL
);

CREATE TRIGGER configuration_revisions_append_only_update
BEFORE UPDATE ON configuration_revisions
BEGIN
    SELECT RAISE(ABORT, 'Config Revisions are append-only');
END;

CREATE TRIGGER configuration_revisions_append_only_delete
BEFORE DELETE ON configuration_revisions
BEGIN
    SELECT RAISE(ABORT, 'Config Revisions are append-only');
END;

CREATE TRIGGER worker_secret_versions_append_only_update
BEFORE UPDATE ON worker_secret_versions
BEGIN
    SELECT RAISE(ABORT, 'Worker Secret Versions are append-only');
END;

CREATE TRIGGER worker_secret_versions_append_only_delete
BEFORE DELETE ON worker_secret_versions
BEGIN
    SELECT RAISE(ABORT, 'Worker Secret Versions are append-only');
END;

CREATE TRIGGER configuration_state_active_revision_exists
BEFORE UPDATE OF active_revision ON configuration_state
WHEN NEW.active_revision IS NOT NULL
    AND NOT EXISTS (SELECT 1 FROM configuration_revisions WHERE id = NEW.active_revision)
BEGIN
    SELECT RAISE(ABORT, 'active Config Revision does not exist');
END;
