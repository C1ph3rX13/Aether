-- Project is the ownership and range-scan prefix for Blackboard entities.
-- Rebuild the complete Intent foreign-key dependency graph so SQLite never
-- retargets a live child table to a legacy parent during table renames.
PRAGMA defer_foreign_keys = ON;

CREATE TEMP TABLE aether_project_first_sequence (
    value INTEGER NOT NULL
);
INSERT INTO aether_project_first_sequence (value)
SELECT COALESCE((
    SELECT seq FROM sqlite_sequence WHERE name = 'execution_recovery_events'
), 0);

DROP TRIGGER facts_description_immutable;
DROP TRIGGER facts_delete_only_with_project;
DROP TRIGGER intents_content_immutable;
DROP TRIGGER intents_delete_only_with_project;
DROP TRIGGER intents_kind_immutable;
DROP TRIGGER intent_sources_immutable;
DROP TRIGGER intent_sources_insert_immutable;
DROP TRIGGER intent_sources_delete_immutable;
DROP TRIGGER intent_sources_lock_one_way;
DROP TRIGGER hints_content_immutable;
DROP TRIGGER hints_delete_only_with_project;
DROP TRIGGER execution_work_identity_immutable;
DROP TRIGGER execution_claim_scope_identity_immutable;
DROP TRIGGER execution_run_spec_immutable;
DROP TRIGGER execution_run_terminal_once;
DROP TRIGGER execution_run_finish_fields_atomic;
DROP TRIGGER execution_run_finish_receipt_required;
DROP TRIGGER execution_run_result_required;
DROP TRIGGER execution_claim_identity_immutable;
DROP TRIGGER execution_claim_lease_text_sync;

DROP INDEX facts_project_order_idx;
DROP INDEX facts_project_active_idx;
DROP INDEX intents_project_created_idx;
DROP INDEX intents_project_open_kind_idx;
DROP INDEX hints_project_created_idx;
DROP INDEX hints_project_active_idx;
DROP INDEX intent_sources_project_order_idx;
DROP INDEX execution_reason_scope_project_idx;
DROP INDEX execution_intent_scope_target_idx;
DROP INDEX execution_runs_project_started_idx;
DROP INDEX execution_runs_status_idx;
DROP INDEX execution_runs_work_attempt_idx;
DROP INDEX execution_runs_project_activity_idx;
DROP INDEX execution_runs_project_status_idx;
DROP INDEX execution_runs_project_task_idx;
DROP INDEX execution_claims_project_idx;
DROP INDEX execution_claims_reason_project_idx;
DROP INDEX execution_claims_target_intent_idx;
DROP INDEX execution_worker_declines_retry_idx;
DROP INDEX execution_recovery_events_run_idx;
DROP INDEX execution_runtime_handles_active_idx;

ALTER TABLE facts RENAME TO facts_legacy_project_first;
ALTER TABLE intents RENAME TO intents_legacy_project_first;
ALTER TABLE hints RENAME TO hints_legacy_project_first;
ALTER TABLE intent_sources RENAME TO intent_sources_legacy_project_first;
ALTER TABLE project_completions RENAME TO project_completions_legacy_project_first;
ALTER TABLE execution_work_keys RENAME TO execution_work_keys_legacy_project_first;
ALTER TABLE execution_claim_scopes RENAME TO execution_claim_scopes_legacy_project_first;
ALTER TABLE execution_runs RENAME TO execution_runs_legacy_project_first;
ALTER TABLE execution_claims RENAME TO execution_claims_legacy_project_first;
ALTER TABLE execution_worker_declines RENAME TO execution_worker_declines_legacy_project_first;
ALTER TABLE execution_recovery_events RENAME TO execution_recovery_events_legacy_project_first;
ALTER TABLE execution_runtime_handles RENAME TO execution_runtime_handles_legacy_project_first;
ALTER TABLE reason_checkpoints RENAME TO reason_checkpoints_legacy_project_first;

CREATE TABLE facts (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    description TEXT NOT NULL,
    supersedes_fact_id TEXT,
    retracted_at TEXT,
    retracted_by TEXT,
    retraction_reason TEXT,
    replacement_fact_id TEXT,
    PRIMARY KEY (project_id, id)
);

CREATE TABLE intents (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    to_fact_id TEXT,
    description TEXT NOT NULL,
    creator TEXT NOT NULL,
    created_at TEXT NOT NULL,
    concluded_at TEXT,
    cancelled_at TEXT,
    cancelled_by TEXT,
    cancellation_reason TEXT,
    sources_locked INTEGER NOT NULL DEFAULT 1 CHECK (sources_locked IN (0, 1)),
    kind TEXT NOT NULL DEFAULT 'explore' CHECK (kind IN ('bootstrap', 'explore', 'completion')),
    PRIMARY KEY (project_id, id)
);

CREATE TABLE hints (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    content TEXT NOT NULL,
    creator TEXT NOT NULL,
    created_at TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
    withdrawn_at TEXT,
    withdrawn_by TEXT,
    withdrawal_reason TEXT,
    PRIMARY KEY (project_id, id)
);

CREATE TABLE intent_sources (
    project_id TEXT NOT NULL,
    intent_id TEXT NOT NULL,
    fact_id TEXT NOT NULL,
    PRIMARY KEY (project_id, intent_id, fact_id),
    FOREIGN KEY (project_id, intent_id) REFERENCES intents(project_id, id) ON DELETE CASCADE,
    FOREIGN KEY (project_id, fact_id) REFERENCES facts(project_id, id) ON DELETE CASCADE
);

CREATE TABLE project_completions (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL CHECK (generation > 0),
    intent_id TEXT NOT NULL,
    description TEXT NOT NULL,
    actor TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    PRIMARY KEY (project_id, generation),
    UNIQUE (project_id, intent_id),
    FOREIGN KEY (project_id, intent_id) REFERENCES intents(project_id, id) ON DELETE RESTRICT
);

CREATE TABLE execution_work_keys (
    work_key TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_type TEXT NOT NULL CHECK (task_type IN ('reason', 'explore', 'bootstrap')),
    target_intent_id TEXT,
    last_attempt INTEGER NOT NULL DEFAULT 0 CHECK (last_attempt >= 0),
    created_at TEXT NOT NULL,
    retry_state TEXT NOT NULL DEFAULT 'eligible',
    failure_count INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    initial_backoff_ms INTEGER NOT NULL DEFAULT 30000,
    max_backoff_ms INTEGER NOT NULL DEFAULT 300000,
    backoff_multiplier REAL NOT NULL DEFAULT 2.0,
    decline_cooldown_ms INTEGER NOT NULL DEFAULT 5000,
    next_eligible_at TEXT,
    updated_at TEXT,
    CHECK (
        (task_type = 'reason' AND target_intent_id IS NULL) OR
        (task_type IN ('explore', 'bootstrap') AND target_intent_id IS NOT NULL)
    ),
    FOREIGN KEY (project_id, target_intent_id) REFERENCES intents(project_id, id) ON DELETE CASCADE
);

CREATE TABLE execution_claim_scopes (
    claim_scope TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL CHECK (scope_type IN ('reason', 'intent')),
    target_intent_id TEXT,
    last_fencing_token INTEGER NOT NULL DEFAULT 0 CHECK (last_fencing_token >= 0),
    created_at TEXT NOT NULL,
    CHECK (
        (scope_type = 'reason' AND target_intent_id IS NULL) OR
        (scope_type = 'intent' AND target_intent_id IS NOT NULL)
    ),
    FOREIGN KEY (project_id, target_intent_id) REFERENCES intents(project_id, id) ON DELETE CASCADE
);

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
    input_generation INTEGER NOT NULL CHECK (input_generation > 0),
    input_revision INTEGER NOT NULL CHECK (input_revision >= 0),
    input_hash TEXT NOT NULL,
    worker_name TEXT NOT NULL,
    config_revision TEXT NOT NULL,
    image_reference TEXT NOT NULL,
    result_schema TEXT NOT NULL,
    execution_spec_json TEXT NOT NULL,
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

CREATE TABLE execution_worker_declines (
    work_key TEXT NOT NULL REFERENCES execution_work_keys(work_key) ON DELETE CASCADE,
    worker_name TEXT NOT NULL,
    retry_after TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (work_key, worker_name)
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

INSERT INTO facts (
    project_id, id, description, supersedes_fact_id, retracted_at, retracted_by,
    retraction_reason, replacement_fact_id
)
SELECT project_id, id, description, supersedes_fact_id, retracted_at, retracted_by,
       retraction_reason, replacement_fact_id
FROM facts_legacy_project_first;

INSERT INTO intents (
    project_id, id, to_fact_id, description, creator, created_at, concluded_at,
    cancelled_at, cancelled_by, cancellation_reason, sources_locked, kind
)
SELECT project_id, id, to_fact_id, description, creator, created_at, concluded_at,
       cancelled_at, cancelled_by, cancellation_reason, sources_locked, kind
FROM intents_legacy_project_first;

INSERT INTO hints (
    project_id, id, content, creator, created_at, generation, withdrawn_at,
    withdrawn_by, withdrawal_reason
)
SELECT project_id, id, content, creator, created_at, generation, withdrawn_at,
       withdrawn_by, withdrawal_reason
FROM hints_legacy_project_first;

INSERT INTO intent_sources (project_id, intent_id, fact_id)
SELECT project_id, intent_id, fact_id
FROM intent_sources_legacy_project_first;

INSERT INTO project_completions (
    project_id, generation, intent_id, description, actor, completed_at
)
SELECT project_id, generation, intent_id, description, actor, completed_at
FROM project_completions_legacy_project_first;

INSERT INTO execution_work_keys (
    work_key, project_id, task_type, target_intent_id, last_attempt, created_at,
    retry_state, failure_count, max_attempts, initial_backoff_ms, max_backoff_ms,
    backoff_multiplier, decline_cooldown_ms, next_eligible_at, updated_at
)
SELECT work_key, project_id, task_type, target_intent_id, last_attempt, created_at,
       retry_state, failure_count, max_attempts, initial_backoff_ms, max_backoff_ms,
       backoff_multiplier, decline_cooldown_ms, next_eligible_at, updated_at
FROM execution_work_keys_legacy_project_first;

INSERT INTO execution_claim_scopes (
    claim_scope, project_id, scope_type, target_intent_id, last_fencing_token, created_at
)
SELECT claim_scope, project_id, scope_type, target_intent_id, last_fencing_token, created_at
FROM execution_claim_scopes_legacy_project_first;

INSERT INTO execution_runs (
    id, project_id, target_intent_id, work_key, claim_scope, attempt, status,
    fencing_token, dispatcher_id, task_type, input_generation, input_revision,
    input_hash, worker_name, config_revision, image_reference, result_schema,
    execution_spec_json, started_at, finished_at, result_hash, outcome_kind,
    error_code, result_json, finish_receipt_json, previous_run_id,
    next_eligible_at, dispatcher_fencing_token
)
SELECT id, project_id, target_intent_id, work_key, claim_scope, attempt, status,
       fencing_token, dispatcher_id, task_type, input_generation, input_revision,
       input_hash, worker_name, config_revision, image_reference, result_schema,
       execution_spec_json, started_at, finished_at, result_hash, outcome_kind,
       error_code, result_json, finish_receipt_json, previous_run_id,
       next_eligible_at, dispatcher_fencing_token
FROM execution_runs_legacy_project_first;

INSERT INTO execution_claims (
    claim_scope, work_key, run_id, project_id, target_intent_id, task_type,
    fencing_token, dispatcher_id, claimed_at, lease_expires_at, lease_expires_unix_nano
)
SELECT claim_scope, work_key, run_id, project_id, target_intent_id, task_type,
       fencing_token, dispatcher_id, claimed_at, lease_expires_at, lease_expires_unix_nano
FROM execution_claims_legacy_project_first;

INSERT INTO execution_worker_declines (work_key, worker_name, retry_after, updated_at)
SELECT work_key, worker_name, retry_after, updated_at
FROM execution_worker_declines_legacy_project_first;

INSERT INTO execution_recovery_events (id, run_id, dispatcher_id, status, reason, observed_at)
SELECT id, run_id, dispatcher_id, status, reason, observed_at
FROM execution_recovery_events_legacy_project_first;

INSERT INTO execution_runtime_handles (
    run_id, project_id, phase, container_id, exec_id, started_at, status, stopped_at
)
SELECT run_id, project_id, phase, container_id, exec_id, started_at, status, stopped_at
FROM execution_runtime_handles_legacy_project_first;

INSERT INTO reason_checkpoints (project_id, generation, input_revision, run_id, checked_at)
SELECT project_id, generation, input_revision, run_id, checked_at
FROM reason_checkpoints_legacy_project_first;

DROP TABLE execution_claims_legacy_project_first;
DROP TABLE execution_worker_declines_legacy_project_first;
DROP TABLE execution_recovery_events_legacy_project_first;
DROP TABLE execution_runtime_handles_legacy_project_first;
DROP TABLE reason_checkpoints_legacy_project_first;
DROP TABLE execution_runs_legacy_project_first;
DROP TABLE execution_work_keys_legacy_project_first;
DROP TABLE execution_claim_scopes_legacy_project_first;
DROP TABLE project_completions_legacy_project_first;
DROP TABLE intent_sources_legacy_project_first;
DROP TABLE facts_legacy_project_first;
DROP TABLE hints_legacy_project_first;
DROP TABLE intents_legacy_project_first;

DELETE FROM sqlite_sequence WHERE name = 'execution_recovery_events';
INSERT INTO sqlite_sequence (name, seq)
SELECT 'execution_recovery_events', value
FROM aether_project_first_sequence;
DROP TABLE aether_project_first_sequence;

CREATE INDEX facts_project_active_idx
    ON facts(project_id, id) WHERE retracted_at IS NULL;
CREATE INDEX intents_project_created_idx
    ON intents(project_id, created_at, id);
CREATE INDEX intents_project_open_kind_idx
    ON intents(project_id, kind, created_at, id)
    WHERE to_fact_id IS NULL AND cancelled_at IS NULL;
CREATE INDEX hints_project_created_idx
    ON hints(project_id, created_at, id);
CREATE INDEX hints_project_active_idx
    ON hints(project_id, created_at, id) WHERE withdrawn_at IS NULL;
CREATE INDEX projects_created_id_idx
    ON projects(created_at, id);
CREATE INDEX projects_status_id_idx
    ON projects(status, id);
CREATE INDEX execution_work_keys_project_retry_idx
    ON execution_work_keys(project_id, retry_state, next_eligible_at);
CREATE INDEX execution_work_keys_project_task_target_idx
    ON execution_work_keys(project_id, task_type, target_intent_id, created_at, work_key);
CREATE UNIQUE INDEX execution_reason_scope_project_idx
    ON execution_claim_scopes(project_id) WHERE scope_type = 'reason';
CREATE UNIQUE INDEX execution_intent_scope_target_idx
    ON execution_claim_scopes(project_id, target_intent_id) WHERE scope_type = 'intent';
CREATE INDEX execution_runs_project_started_idx
    ON execution_runs(project_id, started_at);
CREATE INDEX execution_runs_status_idx
    ON execution_runs(status);
CREATE INDEX execution_runs_work_attempt_idx
    ON execution_runs(work_key, attempt);
CREATE INDEX execution_runs_project_activity_idx
    ON execution_runs(project_id, started_at DESC, id DESC);
CREATE INDEX execution_runs_project_status_idx
    ON execution_runs(project_id, status, started_at DESC);
CREATE INDEX execution_runs_project_task_idx
    ON execution_runs(project_id, task_type, started_at DESC);
CREATE INDEX execution_claims_project_idx
    ON execution_claims(project_id);
CREATE UNIQUE INDEX execution_claims_reason_project_idx
    ON execution_claims(project_id) WHERE task_type = 'reason';
CREATE UNIQUE INDEX execution_claims_target_intent_idx
    ON execution_claims(project_id, target_intent_id) WHERE target_intent_id IS NOT NULL;
CREATE INDEX execution_worker_declines_retry_idx
    ON execution_worker_declines(work_key, retry_after);
CREATE INDEX execution_recovery_events_run_idx
    ON execution_recovery_events(run_id, id);
CREATE INDEX execution_runtime_handles_active_idx
    ON execution_runtime_handles(run_id, status);
CREATE INDEX execution_runtime_handles_project_active_idx
    ON execution_runtime_handles(project_id, started_at, run_id, phase, exec_id)
    WHERE status = 'active';

CREATE TRIGGER facts_description_immutable
BEFORE UPDATE OF description ON facts
FOR EACH ROW WHEN NEW.description IS NOT OLD.description
BEGIN
    SELECT RAISE(ABORT, 'fact description is immutable');
END;

CREATE TRIGGER facts_delete_only_with_project
BEFORE DELETE ON facts
WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
 AND EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id)
BEGIN
    SELECT RAISE(ABORT, 'facts can only be deleted with their project');
END;

CREATE TRIGGER intents_content_immutable
BEFORE UPDATE OF description, creator ON intents
FOR EACH ROW WHEN NEW.description IS NOT OLD.description OR NEW.creator IS NOT OLD.creator
BEGIN
    SELECT RAISE(ABORT, 'intent content is immutable');
END;

CREATE TRIGGER intents_delete_only_with_project
BEFORE DELETE ON intents
WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
 AND EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id)
BEGIN
    SELECT RAISE(ABORT, 'intents can only be deleted with their project');
END;

CREATE TRIGGER intents_kind_immutable
BEFORE UPDATE OF kind ON intents
FOR EACH ROW WHEN NEW.kind IS NOT OLD.kind
BEGIN
    SELECT RAISE(ABORT, 'intent kind is immutable');
END;

CREATE TRIGGER intent_sources_immutable
BEFORE UPDATE ON intent_sources
BEGIN
    SELECT RAISE(ABORT, 'intent sources are immutable');
END;

CREATE TRIGGER intent_sources_insert_immutable
BEFORE INSERT ON intent_sources
WHEN COALESCE((SELECT sources_locked FROM intents WHERE project_id = NEW.project_id AND id = NEW.intent_id), 1) = 1
BEGIN
    SELECT RAISE(ABORT, 'intent sources are immutable');
END;

CREATE TRIGGER intent_sources_delete_immutable
BEFORE DELETE ON intent_sources
WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
  AND EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id)
BEGIN
    SELECT RAISE(ABORT, 'intent sources are immutable');
END;

CREATE TRIGGER intent_sources_lock_one_way
BEFORE UPDATE OF sources_locked ON intents
FOR EACH ROW WHEN OLD.sources_locked = 1 AND NEW.sources_locked <> 1
BEGIN
    SELECT RAISE(ABORT, 'intent sources lock is immutable');
END;

CREATE TRIGGER hints_content_immutable
BEFORE UPDATE OF content, creator ON hints
FOR EACH ROW WHEN NEW.content IS NOT OLD.content OR NEW.creator IS NOT OLD.creator
BEGIN
    SELECT RAISE(ABORT, 'hint content is immutable');
END;

CREATE TRIGGER hints_delete_only_with_project
BEFORE DELETE ON hints
WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
 AND EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id)
BEGIN
    SELECT RAISE(ABORT, 'hints can only be deleted with their project');
END;

CREATE TRIGGER execution_work_identity_immutable
BEFORE UPDATE OF project_id, task_type, target_intent_id, created_at ON execution_work_keys
BEGIN
    SELECT RAISE(ABORT, 'execution work identity is immutable');
END;

CREATE TRIGGER execution_claim_scope_identity_immutable
BEFORE UPDATE OF project_id, scope_type, target_intent_id, created_at ON execution_claim_scopes
BEGIN
    SELECT RAISE(ABORT, 'execution claim scope identity is immutable');
END;

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
