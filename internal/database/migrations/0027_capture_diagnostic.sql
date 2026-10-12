ALTER TABLE execution_runs ADD COLUMN capture_diagnostic TEXT
    CHECK (capture_diagnostic IS NULL OR capture_diagnostic IN (
        'final_text_empty', 'final_text_raw_json', 'final_text_fenced_json',
        'final_text_has_leading_text', 'final_text_has_trailing_text',
        'marker_missing', 'marker_multiple', 'marker_reversed',
        'json_syntax_error', 'json_truncated'
    ));

DROP TRIGGER execution_run_finish_fields_atomic;

CREATE TRIGGER execution_run_finish_fields_atomic
BEFORE UPDATE OF
    finished_at, result_hash, outcome_kind, error_code,
    capture_failure_reason, capture_diagnostic, result_json, finish_receipt_json
ON execution_runs
WHEN OLD.status <> 'active' OR NEW.status = OLD.status
BEGIN
    SELECT RAISE(ABORT, 'execution run finish fields require an active-to-terminal transition');
END;
