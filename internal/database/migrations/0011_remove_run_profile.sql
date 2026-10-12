DROP TRIGGER execution_run_spec_immutable;

ALTER TABLE execution_runs DROP COLUMN run_profile;

CREATE TRIGGER execution_run_spec_immutable
BEFORE UPDATE OF
    project_id, target_intent_id, work_key, claim_scope, attempt, previous_run_id,
    fencing_token, dispatcher_id, dispatcher_fencing_token, task_type, input_generation, input_revision,
    input_hash, worker_name, config_revision, image_id, image_repo_digest,
    result_schema, execution_spec_json, started_at
ON execution_runs
BEGIN
    SELECT RAISE(ABORT, 'execution run spec is immutable');
END;
