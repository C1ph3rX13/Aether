DROP TRIGGER execution_run_spec_immutable;

ALTER TABLE execution_runs RENAME COLUMN image_id TO image_reference;

UPDATE execution_runs
SET image_reference = COALESCE(
    NULLIF(json_extract(execution_spec_json, '$.image.requested_ref'), ''),
    image_reference
);

ALTER TABLE execution_runs DROP COLUMN image_repo_digest;

CREATE TRIGGER execution_run_spec_immutable
BEFORE UPDATE OF
    project_id, target_intent_id, work_key, claim_scope, attempt, previous_run_id,
    fencing_token, dispatcher_id, dispatcher_fencing_token, task_type, input_generation, input_revision,
    input_hash, worker_name, config_revision, image_reference,
    result_schema, execution_spec_json, started_at
ON execution_runs
BEGIN
    SELECT RAISE(ABORT, 'execution run spec is immutable');
END;
