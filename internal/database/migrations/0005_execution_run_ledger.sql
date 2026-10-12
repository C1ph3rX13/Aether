CREATE TABLE execution_work_keys (
    work_key TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_type TEXT NOT NULL CHECK (task_type IN ('reason', 'explore', 'bootstrap')),
    target_intent_id TEXT,
    last_attempt INTEGER NOT NULL DEFAULT 0 CHECK (last_attempt >= 0),
    created_at TEXT NOT NULL,
    CHECK (
        (task_type = 'reason' AND target_intent_id IS NULL) OR
        (task_type IN ('explore', 'bootstrap') AND target_intent_id IS NOT NULL)
    ),
    FOREIGN KEY (target_intent_id, project_id) REFERENCES intents(id, project_id) ON DELETE CASCADE
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
    FOREIGN KEY (target_intent_id, project_id) REFERENCES intents(id, project_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX execution_reason_scope_project_idx
    ON execution_claim_scopes(project_id) WHERE scope_type = 'reason';
CREATE UNIQUE INDEX execution_intent_scope_target_idx
    ON execution_claim_scopes(project_id, target_intent_id) WHERE scope_type = 'intent';

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
    image_id TEXT NOT NULL,
    image_repo_digest TEXT NOT NULL,
    run_profile TEXT NOT NULL,
    result_schema TEXT NOT NULL,
    execution_spec_json TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    result_hash TEXT,
    outcome_kind TEXT,
    error_code TEXT,
    UNIQUE (work_key, attempt),
    UNIQUE (claim_scope, fencing_token),
    UNIQUE (id, work_key, claim_scope, fencing_token),
    FOREIGN KEY (target_intent_id, project_id) REFERENCES intents(id, project_id) ON DELETE CASCADE
);

CREATE INDEX execution_runs_project_started_idx ON execution_runs(project_id, started_at);
CREATE INDEX execution_runs_status_idx ON execution_runs(status);

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
    CHECK (
        (task_type = 'reason' AND target_intent_id IS NULL) OR
        (task_type IN ('explore', 'bootstrap') AND target_intent_id IS NOT NULL)
    ),
    FOREIGN KEY (run_id, work_key, claim_scope, fencing_token) REFERENCES execution_runs(id, work_key, claim_scope, fencing_token) ON DELETE CASCADE,
    FOREIGN KEY (target_intent_id, project_id) REFERENCES intents(id, project_id) ON DELETE CASCADE
);

CREATE INDEX execution_claims_project_idx ON execution_claims(project_id);
CREATE UNIQUE INDEX execution_claims_reason_project_idx
    ON execution_claims(project_id) WHERE task_type = 'reason';
CREATE UNIQUE INDEX execution_claims_target_intent_idx
    ON execution_claims(project_id, target_intent_id) WHERE target_intent_id IS NOT NULL;

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
    project_id, target_intent_id, work_key, claim_scope, attempt, fencing_token,
    dispatcher_id, task_type, input_generation, input_revision, input_hash,
    worker_name, config_revision, image_id, image_repo_digest, run_profile,
    result_schema, execution_spec_json, started_at
ON execution_runs
BEGIN
    SELECT RAISE(ABORT, 'execution run spec is immutable');
END;

CREATE TRIGGER execution_claim_identity_immutable
BEFORE UPDATE OF
    claim_scope, work_key, run_id, project_id, target_intent_id, task_type,
    fencing_token, dispatcher_id, claimed_at
ON execution_claims
BEGIN
    SELECT RAISE(ABORT, 'execution claim identity is immutable');
END;
