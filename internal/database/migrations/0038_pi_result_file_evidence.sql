-- Extend terminal evidence for Pi Reason result files.
-- SQLite cannot alter CHECK constraints in place, so rebuild the execution ledger.
-- Rebuild the execution ledger tables because SQLite cannot alter a CHECK
-- constraint in place. All existing execution evidence remains unchanged.
PRAGMA defer_foreign_keys = ON;

CREATE TEMP TABLE aether_execution_recovery_sequence (
    value INTEGER NOT NULL
);
INSERT INTO aether_execution_recovery_sequence (value)
SELECT COALESCE((
    SELECT seq FROM sqlite_sequence WHERE name = 'execution_recovery_events'
), 0);

DROP TRIGGER execution_run_spec_immutable;
DROP TRIGGER execution_run_previous_run_immutable;
DROP TRIGGER execution_run_terminal_once;
DROP TRIGGER execution_run_finish_fields_atomic;
DROP TRIGGER execution_run_finish_receipt_required;
DROP TRIGGER execution_run_result_required;
DROP TRIGGER execution_claim_identity_immutable;
DROP TRIGGER execution_claim_lease_text_sync;

DROP INDEX execution_runs_project_started_idx;
DROP INDEX execution_runs_status_idx;
DROP INDEX execution_runs_work_attempt_idx;
DROP INDEX execution_runs_project_activity_idx;
DROP INDEX execution_runs_project_status_idx;
DROP INDEX execution_runs_project_task_idx;
DROP INDEX execution_runs_schema_project_activity_idx;
DROP INDEX execution_claims_project_idx;
DROP INDEX execution_claims_reason_project_idx;
DROP INDEX execution_claims_target_intent_idx;
DROP INDEX execution_recovery_events_run_idx;
DROP INDEX execution_runtime_handles_active_idx;
DROP INDEX execution_runtime_handles_project_active_idx;

ALTER TABLE execution_runs RENAME TO execution_runs_before_pi_result_file;
ALTER TABLE execution_claims RENAME TO execution_claims_before_pi_result_file;
ALTER TABLE execution_recovery_events RENAME TO execution_recovery_events_before_pi_result_file;
ALTER TABLE execution_runtime_handles RENAME TO execution_runtime_handles_before_pi_result_file;
ALTER TABLE reason_checkpoints RENAME TO reason_checkpoints_before_pi_result_file;

CREATE TABLE execution_runs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    target_intent_id TEXT,
    work_key TEXT NOT NULL REFERENCES execution_work_keys(work_key) ON DELETE CASCADE,
    claim_scope TEXT NOT NULL REFERENCES execution_claim_scopes(claim_scope) ON DELETE CASCADE,
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    status TEXT NOT NULL CHECK (status IN ('active', 'committed', 'declined', 'stale', 'failed', 'cancelled', 'interrupted')),
    fencing_token INTEGER NOT NULL CHECK (fencing_token > 0),
    dispatcher_id TEXT NOT NULL,
    task_type TEXT NOT NULL CHECK (task_type IN ('reason', 'explore', 'bootstrap')),
    execution_spec_schema TEXT NOT NULL CHECK (execution_spec_schema IN (
        'aether.execution-spec/legacy-v1', 'aether.execution-spec/v2'
    )),
    execution_spec_json TEXT NOT NULL
        CHECK (json_valid(execution_spec_json))
        CHECK (aether_json_no_duplicate_keys(execution_spec_json) = 1)
        CHECK (aether_execution_spec_valid(
            execution_spec_schema, execution_spec_json, task_type, target_intent_id
        ) = 1),
    input_generation INTEGER GENERATED ALWAYS AS (
        json_extract(execution_spec_json, '$.input.generation')
    ) STORED NOT NULL,
    input_revision INTEGER GENERATED ALWAYS AS (
        json_extract(execution_spec_json, '$.input.revision')
    ) STORED NOT NULL,
    input_hash TEXT GENERATED ALWAYS AS (
        CASE execution_spec_schema
            WHEN 'aether.execution-spec/v2'
                THEN json_extract(execution_spec_json, '$.input.identity.sha256')
            ELSE COALESCE(
                json_extract(execution_spec_json, '$.input.identity.sha256'),
                json_extract(execution_spec_json, '$.input.hash')
            )
        END
    ) STORED NOT NULL,
    input_evidence_schema TEXT GENERATED ALWAYS AS (
        CASE execution_spec_schema
            WHEN 'aether.execution-spec/v2'
                THEN json_extract(execution_spec_json, '$.input.identity.schema')
            ELSE COALESCE(
                json_extract(execution_spec_json, '$.input.identity.schema'),
                'aether.input-hash/legacy-rendered-v0'
            )
        END
    ) STORED NOT NULL,
    prompt_hash TEXT GENERATED ALWAYS AS (
        CASE execution_spec_schema
            WHEN 'aether.execution-spec/v2'
                THEN json_extract(execution_spec_json, '$.prompt.sha256')
            ELSE COALESCE(
                json_extract(execution_spec_json, '$.prompt_template_hash'),
                json_extract(execution_spec_json, '$.prompt_hash')
            )
        END
    ) STORED NOT NULL,
    prompt_evidence_schema TEXT GENERATED ALWAYS AS (
        CASE
            WHEN execution_spec_schema = 'aether.execution-spec/v2'
                THEN json_extract(execution_spec_json, '$.prompt.schema')
            WHEN json_type(execution_spec_json, '$.prompt_template_hash') = 'text'
                THEN 'aether.prompt-template-hash/v1'
            ELSE 'aether.rendered-prompt-hash/legacy-v1'
        END
    ) STORED NOT NULL,
    worker_name TEXT GENERATED ALWAYS AS (
        json_extract(execution_spec_json, '$.worker.name')
    ) STORED NOT NULL,
    config_revision TEXT GENERATED ALWAYS AS (
        json_extract(execution_spec_json, '$.config_revision')
    ) STORED NOT NULL,
    image_reference TEXT GENERATED ALWAYS AS (
        json_extract(execution_spec_json, '$.image.requested_ref')
    ) STORED NOT NULL,
    result_schema TEXT GENERATED ALWAYS AS (
        json_extract(execution_spec_json, '$.result_schema')
    ) STORED NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    result_hash TEXT,
    outcome_kind TEXT,
    error_code TEXT,
    capture_failure_reason TEXT CHECK (capture_failure_reason IS NULL OR capture_failure_reason IN (
        'timed_out', 'no_candidate', 'multiple_candidates',
        'invalid_candidate', 'candidate_too_large', 'boundary_error'
    )),
    capture_diagnostic TEXT CHECK (capture_diagnostic IS NULL OR capture_diagnostic IN (
        'final_text_empty', 'final_text_raw_json', 'final_text_fenced_json',
        'final_text_has_leading_text', 'final_text_has_trailing_text',
        'marker_missing', 'marker_multiple', 'marker_reversed',
        'json_syntax_error', 'json_truncated', 'json_repaired',
        'result_file_missing', 'result_file_invalid', 'result_file_too_large'
    )),
    result_evidence_source TEXT CHECK (result_evidence_source IS NULL OR result_evidence_source IN (
        'session', 'stdout', 'reconciled', 'file'
    )),
    recovery_used INTEGER NOT NULL DEFAULT 0 CHECK (recovery_used IN (0, 1)),
    recovery_phase TEXT CHECK (
        recovery_phase IS NULL OR recovery_phase IN (
            'explore_recovery', 'explore_conclude_recovery',
            'bootstrap_recovery', 'bootstrap_conclude_recovery'
        )
    ),
    recovery_started_at TEXT,
    recovery_mode TEXT CHECK (
        recovery_mode IS NULL OR recovery_mode IN (
            'explore_primary', 'explore_conclude',
            'bootstrap_primary', 'bootstrap_conclude'
        )
    ),
    result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
    finish_receipt_json TEXT CHECK (finish_receipt_json IS NULL OR json_valid(finish_receipt_json)),
    previous_run_id TEXT REFERENCES execution_runs(id) ON DELETE SET NULL,
    next_eligible_at TEXT,
    dispatcher_fencing_token INTEGER NOT NULL DEFAULT 0 CHECK (dispatcher_fencing_token >= 0),
    UNIQUE (work_key, attempt),
    UNIQUE (claim_scope, fencing_token),
    UNIQUE (id, work_key, claim_scope, fencing_token),
    FOREIGN KEY (project_id, target_intent_id) REFERENCES intents(project_id, id) ON DELETE CASCADE
);

CREATE TABLE execution_claims (
    claim_scope TEXT PRIMARY KEY REFERENCES execution_claim_scopes(claim_scope) ON DELETE CASCADE,
    work_key TEXT NOT NULL UNIQUE REFERENCES execution_work_keys(work_key) ON DELETE CASCADE,
    run_id TEXT NOT NULL UNIQUE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    target_intent_id TEXT,
    task_type TEXT NOT NULL CHECK (task_type IN ('reason', 'explore', 'bootstrap')),
    fencing_token INTEGER NOT NULL CHECK (fencing_token > 0),
    dispatcher_id TEXT NOT NULL,
    claimed_at TEXT NOT NULL,
    lease_expires_at TEXT NOT NULL,
    lease_expires_unix_nano INTEGER,
    CHECK (
        (task_type = 'reason' AND target_intent_id IS NULL) OR
        (task_type IN ('explore', 'bootstrap') AND target_intent_id IS NOT NULL)
    ),
    FOREIGN KEY (run_id, work_key, claim_scope, fencing_token)
        REFERENCES execution_runs(id, work_key, claim_scope, fencing_token) ON DELETE CASCADE,
    FOREIGN KEY (project_id, target_intent_id) REFERENCES intents(project_id, id) ON DELETE CASCADE
);

CREATE TABLE execution_recovery_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL REFERENCES execution_runs(id) ON DELETE CASCADE,
    dispatcher_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('observed', 'disposed', 'interrupted', 'dispose_failed')),
    reason TEXT NOT NULL,
    observed_at TEXT NOT NULL
);

CREATE TABLE execution_runtime_handles (
    run_id TEXT NOT NULL REFERENCES execution_runs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    phase TEXT NOT NULL CHECK (phase IN (
        'reason', 'reason_finalization', 'explore_execute', 'explore_conclude',
        'explore_recovery', 'explore_conclude_recovery',
        'bootstrap', 'bootstrap_conclude',
        'bootstrap_recovery', 'bootstrap_conclude_recovery'
    )),
    container_id TEXT NOT NULL,
    exec_id TEXT NOT NULL,
    started_at TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'stopped')),
    stopped_at TEXT,
    PRIMARY KEY (run_id, phase, exec_id)
);

CREATE TABLE reason_checkpoints (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL CHECK (generation > 0),
    input_revision INTEGER NOT NULL CHECK (input_revision >= 0),
    run_id TEXT NOT NULL UNIQUE REFERENCES execution_runs(id) ON DELETE CASCADE,
    checked_at TEXT NOT NULL,
    PRIMARY KEY (project_id, generation)
);

INSERT INTO execution_runs (
    id, project_id, target_intent_id, work_key, claim_scope, attempt, status,
    fencing_token, dispatcher_id, task_type, execution_spec_schema,
    execution_spec_json, started_at, finished_at, result_hash, outcome_kind,
    error_code, capture_failure_reason, capture_diagnostic, result_evidence_source, recovery_used,
    recovery_phase, recovery_started_at, recovery_mode, result_json,
    finish_receipt_json, previous_run_id, next_eligible_at, dispatcher_fencing_token
)
SELECT id, project_id, target_intent_id, work_key, claim_scope, attempt, status,
       fencing_token, dispatcher_id, task_type, execution_spec_schema,
       execution_spec_json, started_at, finished_at, result_hash, outcome_kind,
       error_code, capture_failure_reason, capture_diagnostic, result_evidence_source, recovery_used,
       recovery_phase, recovery_started_at, recovery_mode, result_json,
       finish_receipt_json, previous_run_id, next_eligible_at, dispatcher_fencing_token
FROM execution_runs_before_pi_result_file;

INSERT INTO execution_claims (
    claim_scope, work_key, run_id, project_id, target_intent_id, task_type,
    fencing_token, dispatcher_id, claimed_at, lease_expires_at, lease_expires_unix_nano
)
SELECT claim_scope, work_key, run_id, project_id, target_intent_id, task_type,
       fencing_token, dispatcher_id, claimed_at, lease_expires_at, lease_expires_unix_nano
FROM execution_claims_before_pi_result_file;

INSERT INTO execution_recovery_events (id, run_id, dispatcher_id, status, reason, observed_at)
SELECT id, run_id, dispatcher_id, status, reason, observed_at
FROM execution_recovery_events_before_pi_result_file;

INSERT INTO execution_runtime_handles (
    run_id, project_id, phase, container_id, exec_id, started_at, status, stopped_at
)
SELECT run_id, project_id, phase, container_id, exec_id, started_at, status, stopped_at
FROM execution_runtime_handles_before_pi_result_file;

INSERT INTO reason_checkpoints (project_id, generation, input_revision, run_id, checked_at)
SELECT project_id, generation, input_revision, run_id, checked_at
FROM reason_checkpoints_before_pi_result_file;

DROP TABLE execution_claims_before_pi_result_file;
DROP TABLE execution_recovery_events_before_pi_result_file;
DROP TABLE execution_runtime_handles_before_pi_result_file;
DROP TABLE reason_checkpoints_before_pi_result_file;
DROP TABLE execution_runs_before_pi_result_file;

DELETE FROM sqlite_sequence WHERE name = 'execution_recovery_events';
INSERT INTO sqlite_sequence (name, seq)
SELECT 'execution_recovery_events', value
FROM aether_execution_recovery_sequence;
DROP TABLE aether_execution_recovery_sequence;

CREATE INDEX execution_runs_project_started_idx
    ON execution_runs(project_id, started_at);
CREATE INDEX execution_runs_status_idx
    ON execution_runs(status);
CREATE INDEX execution_runs_work_attempt_idx
    ON execution_runs(work_key, attempt, worker_name);
CREATE INDEX execution_runs_project_activity_idx
    ON execution_runs(
        project_id, started_at DESC, id DESC,
        worker_name, input_generation, input_revision
    );
CREATE INDEX execution_runs_project_status_idx
    ON execution_runs(project_id, status, started_at DESC);
CREATE INDEX execution_runs_project_task_idx
    ON execution_runs(project_id, task_type, started_at DESC);
CREATE INDEX execution_runs_schema_project_activity_idx
    ON execution_runs(
        execution_spec_schema, project_id, started_at DESC, id DESC,
        input_evidence_schema, input_hash, prompt_evidence_schema, prompt_hash
    );
CREATE INDEX execution_claims_project_idx
    ON execution_claims(project_id);
CREATE UNIQUE INDEX execution_claims_reason_project_idx
    ON execution_claims(project_id) WHERE task_type = 'reason';
CREATE UNIQUE INDEX execution_claims_target_intent_idx
    ON execution_claims(project_id, target_intent_id) WHERE target_intent_id IS NOT NULL;
CREATE INDEX execution_recovery_events_run_idx
    ON execution_recovery_events(run_id, id);
CREATE INDEX execution_runtime_handles_active_idx
    ON execution_runtime_handles(run_id, status);
CREATE INDEX execution_runtime_handles_project_active_idx
    ON execution_runtime_handles(project_id, started_at, run_id, phase, exec_id)
    WHERE status = 'active';

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
    capture_failure_reason, capture_diagnostic, result_evidence_source,
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

CREATE TRIGGER execution_claim_identity_immutable
BEFORE UPDATE OF
    claim_scope, work_key, run_id, project_id, target_intent_id, task_type,
    fencing_token, dispatcher_id, claimed_at
ON execution_claims
BEGIN
    SELECT RAISE(ABORT, 'execution claim identity is immutable');
END;

CREATE TRIGGER execution_claim_lease_text_sync
AFTER UPDATE OF lease_expires_at ON execution_claims
WHEN NEW.lease_expires_unix_nano = OLD.lease_expires_unix_nano
BEGIN
    UPDATE execution_claims
    SET lease_expires_unix_nano = CAST((julianday(NEW.lease_expires_at) - 2440587.5) * 86400000000000 AS INTEGER)
    WHERE claim_scope = NEW.claim_scope;
END;
