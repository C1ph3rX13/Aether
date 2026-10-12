-- Preserve nullable Pi result provenance without inferring historical evidence.
ALTER TABLE execution_runs ADD COLUMN result_evidence_source TEXT CHECK (
    result_evidence_source IS NULL OR result_evidence_source IN ('session', 'stdout', 'reconciled')
);

-- The provenance is part of the atomic terminal finish payload.
DROP TRIGGER execution_run_finish_fields_atomic;

CREATE TRIGGER execution_run_finish_fields_atomic
BEFORE UPDATE OF
    finished_at, result_hash, outcome_kind, error_code,
    capture_failure_reason, capture_diagnostic, result_evidence_source,
    result_json, finish_receipt_json
ON execution_runs
WHEN OLD.status <> 'active' OR NEW.status = OLD.status
BEGIN
    SELECT RAISE(ABORT, 'execution run finish fields require an active-to-terminal transition');
END;
