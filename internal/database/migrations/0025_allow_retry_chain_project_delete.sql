-- Project deletion cascades through every Execution Run in one Retry Chain.
-- SQLite applies the self-referential ON DELETE SET NULL after the parent Run
-- is no longer visible, so permit only that database-owned lineage cleanup.
DROP TRIGGER execution_run_spec_immutable;

CREATE TRIGGER execution_run_spec_immutable
BEFORE UPDATE OF
    id, project_id, target_intent_id, work_key, claim_scope, attempt,
    fencing_token, dispatcher_id, dispatcher_fencing_token, task_type,
    execution_spec_schema, execution_spec_json, started_at
ON execution_runs
BEGIN
    SELECT RAISE(ABORT, 'execution run spec is immutable');
END;

CREATE TRIGGER execution_run_previous_run_immutable
BEFORE UPDATE OF previous_run_id ON execution_runs
WHEN NOT (
    OLD.previous_run_id IS NOT NULL
    AND NEW.previous_run_id IS NULL
    AND NOT EXISTS (
        SELECT 1 FROM execution_runs AS parent
        WHERE parent.id = OLD.previous_run_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'execution run spec is immutable');
END;
