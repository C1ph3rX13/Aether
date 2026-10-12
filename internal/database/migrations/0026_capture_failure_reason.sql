ALTER TABLE execution_runs ADD COLUMN capture_failure_reason TEXT
    CHECK (capture_failure_reason IS NULL OR capture_failure_reason IN (
        'timed_out', 'no_candidate', 'multiple_candidates',
        'invalid_candidate', 'candidate_too_large', 'boundary_error'
    ));

DROP TRIGGER execution_run_finish_fields_atomic;

CREATE TRIGGER execution_run_finish_fields_atomic
BEFORE UPDATE OF
    finished_at, result_hash, outcome_kind, error_code,
    capture_failure_reason, result_json, finish_receipt_json
ON execution_runs
WHEN OLD.status <> 'active' OR NEW.status = OLD.status
BEGIN
    SELECT RAISE(ABORT, 'execution run finish fields require an active-to-terminal transition');
END;
