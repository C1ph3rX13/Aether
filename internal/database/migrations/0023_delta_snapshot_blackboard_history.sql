-- Retain pre-0023 history as truthful legacy snapshots while adding the
-- indexed metadata and compact delta columns used by all future revisions.
ALTER TABLE blackboard_changes ADD COLUMN payload_schema TEXT NOT NULL DEFAULT 'legacy-snapshot/v1';
ALTER TABLE blackboard_changes ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'migration';
ALTER TABLE blackboard_changes ADD COLUMN source_name TEXT NOT NULL DEFAULT 'Legacy history';
ALTER TABLE blackboard_changes ADD COLUMN source_run_id TEXT;
ALTER TABLE blackboard_changes ADD COLUMN delta TEXT NOT NULL DEFAULT '{}';
ALTER TABLE blackboard_changes ADD COLUMN previous_hash TEXT;
ALTER TABLE blackboard_changes ADD COLUMN change_hash TEXT;

UPDATE blackboard_changes
SET payload_schema = 'legacy-snapshot/v1',
    source_kind = 'migration',
    source_name = 'Legacy history',
    source_run_id = NULL,
    delta = '{}',
    previous_hash = NULL,
    change_hash = NULL;

CREATE INDEX blackboard_changes_project_source_revision_idx
    ON blackboard_changes(project_id, source_kind, revision DESC);
CREATE INDEX blackboard_changes_project_kind_revision_idx
    ON blackboard_changes(project_id, kind, revision DESC);
CREATE INDEX blackboard_changes_project_created_revision_idx
    ON blackboard_changes(project_id, created_at, revision DESC);

CREATE TABLE project_history_snapshots (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK (revision >= 0),
    generation INTEGER NOT NULL CHECK (generation > 0),
    snapshot_kind TEXT NOT NULL CHECK (snapshot_kind IN ('cutover', 'creation', 'generation', 'periodic')),
    schema TEXT NOT NULL,
    state_json TEXT NOT NULL CHECK (json_valid(state_json)),
    state_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (project_id, revision)
);

CREATE INDEX project_history_snapshots_project_revision_idx
    ON project_history_snapshots(project_id, revision DESC);

-- Retention is intentionally not exposed by the Project command surface yet.
-- A future retention job must create an active operation row before pruning;
-- without one, delta rows are append-only.
CREATE TABLE blackboard_history_retention_operations (
    operation_id TEXT PRIMARY KEY,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    created_at TEXT NOT NULL
);

CREATE INDEX blackboard_history_retention_operations_project_idx
    ON blackboard_history_retention_operations(project_id, active);

-- New history is append-only. A future retention operation must be introduced
-- as a dedicated migration and authorization path; ordinary UPDATE/DELETE
-- statements cannot rewrite or prune a Project's causal record.
CREATE TRIGGER blackboard_changes_delta_immutable
BEFORE UPDATE ON blackboard_changes
FOR EACH ROW WHEN OLD.payload_schema = 'delta/v1'
    AND NOT EXISTS (
        SELECT 1 FROM blackboard_history_retention_operations AS operation
        WHERE operation.active = 1
          AND (operation.project_id IS NULL OR operation.project_id = OLD.project_id)
    )
BEGIN
    SELECT RAISE(ABORT, 'delta history is immutable');
END;

CREATE TRIGGER blackboard_changes_delta_delete_immutable
BEFORE DELETE ON blackboard_changes
FOR EACH ROW WHEN OLD.payload_schema = 'delta/v1'
    AND EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id)
    AND COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
    AND NOT EXISTS (
        SELECT 1 FROM blackboard_history_retention_operations AS operation
        WHERE operation.active = 1
          AND (operation.project_id IS NULL OR operation.project_id = OLD.project_id)
    )
BEGIN
    SELECT RAISE(ABORT, 'delta history is immutable');
END;

CREATE TRIGGER project_history_snapshots_immutable
BEFORE UPDATE ON project_history_snapshots
FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'project history snapshots are immutable');
END;

CREATE TRIGGER project_history_snapshots_delete_immutable
BEFORE DELETE ON project_history_snapshots
FOR EACH ROW WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
    AND EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id)
    AND NOT EXISTS (
        SELECT 1 FROM blackboard_history_retention_operations AS operation
        WHERE operation.active = 1
          AND (operation.project_id IS NULL OR operation.project_id = OLD.project_id)
    )
BEGIN
    SELECT RAISE(ABORT, 'project history snapshots are immutable');
END;

-- Cutover snapshots are initialized by the Project domain in bounded batches
-- after migration. Keeping that backfill outside this DDL prevents one
-- migration transaction from scanning every Project's Blackboard while
-- holding SQLite's write lock.
