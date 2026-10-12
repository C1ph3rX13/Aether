ALTER TABLE execution_runs ADD COLUMN result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json));
ALTER TABLE execution_runs ADD COLUMN finish_receipt_json TEXT CHECK (finish_receipt_json IS NULL OR json_valid(finish_receipt_json));

CREATE TABLE reason_checkpoints (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL CHECK (generation > 0),
    input_revision INTEGER NOT NULL CHECK (input_revision >= 0),
    run_id TEXT NOT NULL UNIQUE REFERENCES execution_runs(id) ON DELETE CASCADE,
    checked_at TEXT NOT NULL,
    PRIMARY KEY (project_id, generation)
);

CREATE TRIGGER execution_run_terminal_once
BEFORE UPDATE OF status ON execution_runs
WHEN OLD.status <> 'active'
  OR NEW.status NOT IN ('committed', 'declined', 'stale', 'failed', 'cancelled', 'interrupted')
BEGIN
    SELECT RAISE(ABORT, 'execution run terminal state is immutable');
END;

CREATE TRIGGER execution_run_finish_fields_atomic
BEFORE UPDATE OF
    finished_at, result_hash, outcome_kind, error_code,
    result_json, finish_receipt_json
ON execution_runs
WHEN OLD.status <> 'active' OR NEW.status = OLD.status
BEGIN
    SELECT RAISE(ABORT, 'execution run finish fields require an active-to-terminal transition');
END;

CREATE TRIGGER execution_run_finish_receipt_required
AFTER UPDATE OF status ON execution_runs
WHEN NEW.finished_at IS NULL OR NEW.finish_receipt_json IS NULL
BEGIN
    SELECT RAISE(ABORT, 'execution run terminal transition requires a receipt');
END;

CREATE TRIGGER execution_run_result_required
AFTER UPDATE OF status ON execution_runs
WHEN NEW.status IN ('committed', 'declined', 'stale')
 AND (NEW.result_hash IS NULL OR NEW.outcome_kind IS NULL OR NEW.result_json IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'execution run result terminal state requires a normalized result');
END;
