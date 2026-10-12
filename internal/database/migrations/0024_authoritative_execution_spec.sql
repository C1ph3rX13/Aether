-- Refuse to classify evidence that cannot be proven equivalent to the old
-- duplicated columns. The temporary CHECK aborts the whole migration on the
-- first malformed, duplicate-key, incomplete, or inconsistent document.
CREATE TEMP TABLE aether_execution_spec_validation (
    value INTEGER NOT NULL CHECK (value = 1)
);
INSERT INTO aether_execution_spec_validation (value)
SELECT aether_execution_spec_matches(
    'aether.execution-spec/legacy-v1', execution_spec_json, task_type,
    target_intent_id, input_generation, input_revision, input_hash,
    worker_name, config_revision, image_reference, result_schema
)
FROM execution_runs;
DROP TABLE aether_execution_spec_validation;

PRAGMA defer_foreign_keys = ON;

CREATE TEMP TABLE aether_execution_recovery_sequence (
    value INTEGER NOT NULL
);
INSERT INTO aether_execution_recovery_sequence (value)
SELECT COALESCE((
    SELECT seq FROM sqlite_sequence WHERE name = 'execution_recovery_events'
), 0);

DROP TRIGGER execution_run_spec_immutable;
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
DROP INDEX execution_claims_project_idx;
DROP INDEX execution_claims_reason_project_idx;
DROP INDEX execution_claims_target_intent_idx;
DROP INDEX execution_recovery_events_run_idx;
DROP INDEX execution_runtime_handles_active_idx;
DROP INDEX execution_runtime_handles_project_active_idx;

ALTER TABLE execution_runs RENAME TO execution_runs_legacy_authority;
ALTER TABLE execution_claims RENAME TO execution_claims_legacy_authority;
ALTER TABLE execution_recovery_events RENAME TO execution_recovery_events_legacy_authority;
ALTER TABLE execution_runtime_handles RENAME TO execution_runtime_handles_legacy_authority;
ALTER TABLE reason_checkpoints RENAME TO reason_checkpoints_legacy_authority;

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
    phase TEXT NOT NULL CHECK (phase IN ('reason', 'explore_execute', 'explore_conclude', 'bootstrap', 'bootstrap_conclude')),
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
    error_code, result_json, finish_receipt_json, previous_run_id,
    next_eligible_at, dispatcher_fencing_token
)
SELECT id, project_id, target_intent_id, work_key, claim_scope, attempt, status,
       fencing_token, dispatcher_id, task_type, 'aether.execution-spec/legacy-v1',
       execution_spec_json, started_at, finished_at, result_hash, outcome_kind,
       error_code, result_json, finish_receipt_json, previous_run_id,
       next_eligible_at, dispatcher_fencing_token
FROM execution_runs_legacy_authority;

INSERT INTO execution_claims (
    claim_scope, work_key, run_id, project_id, target_intent_id, task_type,
    fencing_token, dispatcher_id, claimed_at, lease_expires_at, lease_expires_unix_nano
)
SELECT claim_scope, work_key, run_id, project_id, target_intent_id, task_type,
       fencing_token, dispatcher_id, claimed_at, lease_expires_at, lease_expires_unix_nano
FROM execution_claims_legacy_authority;

INSERT INTO execution_recovery_events (id, run_id, dispatcher_id, status, reason, observed_at)
SELECT id, run_id, dispatcher_id, status, reason, observed_at
FROM execution_recovery_events_legacy_authority;

INSERT INTO execution_runtime_handles (
    run_id, project_id, phase, container_id, exec_id, started_at, status, stopped_at
)
SELECT run_id, project_id, phase, container_id, exec_id, started_at, status, stopped_at
FROM execution_runtime_handles_legacy_authority;

INSERT INTO reason_checkpoints (project_id, generation, input_revision, run_id, checked_at)
SELECT project_id, generation, input_revision, run_id, checked_at
FROM reason_checkpoints_legacy_authority;

DROP TABLE execution_claims_legacy_authority;
DROP TABLE execution_recovery_events_legacy_authority;
DROP TABLE execution_runtime_handles_legacy_authority;
DROP TABLE reason_checkpoints_legacy_authority;
DROP TABLE execution_runs_legacy_authority;

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
    id, project_id, target_intent_id, work_key, claim_scope, attempt, previous_run_id,
    fencing_token, dispatcher_id, dispatcher_fencing_token, task_type,
    execution_spec_schema, execution_spec_json, started_at
ON execution_runs
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
