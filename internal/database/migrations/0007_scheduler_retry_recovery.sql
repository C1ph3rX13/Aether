ALTER TABLE execution_work_keys ADD COLUMN retry_state TEXT NOT NULL DEFAULT 'eligible';
ALTER TABLE execution_work_keys ADD COLUMN failure_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE execution_work_keys ADD COLUMN max_attempts INTEGER NOT NULL DEFAULT 3;
ALTER TABLE execution_work_keys ADD COLUMN initial_backoff_ms INTEGER NOT NULL DEFAULT 30000;
ALTER TABLE execution_work_keys ADD COLUMN max_backoff_ms INTEGER NOT NULL DEFAULT 300000;
ALTER TABLE execution_work_keys ADD COLUMN backoff_multiplier REAL NOT NULL DEFAULT 2.0;
ALTER TABLE execution_work_keys ADD COLUMN decline_cooldown_ms INTEGER NOT NULL DEFAULT 5000;
ALTER TABLE execution_work_keys ADD COLUMN next_eligible_at TEXT;
ALTER TABLE execution_work_keys ADD COLUMN updated_at TEXT;

ALTER TABLE execution_runs ADD COLUMN previous_run_id TEXT REFERENCES execution_runs(id) ON DELETE SET NULL;
ALTER TABLE execution_runs ADD COLUMN next_eligible_at TEXT;
ALTER TABLE execution_runs ADD COLUMN dispatcher_fencing_token INTEGER NOT NULL DEFAULT 0 CHECK (dispatcher_fencing_token >= 0);

ALTER TABLE execution_claims ADD COLUMN lease_expires_unix_nano INTEGER;
UPDATE execution_claims
SET lease_expires_unix_nano = CAST((julianday(lease_expires_at) - 2440587.5) * 86400000000000 AS INTEGER);

CREATE TRIGGER execution_claim_lease_text_sync
AFTER UPDATE OF lease_expires_at ON execution_claims
WHEN NEW.lease_expires_unix_nano = OLD.lease_expires_unix_nano
BEGIN
    UPDATE execution_claims
    SET lease_expires_unix_nano = CAST((julianday(NEW.lease_expires_at) - 2440587.5) * 86400000000000 AS INTEGER)
    WHERE claim_scope = NEW.claim_scope;
END;

DROP TRIGGER execution_run_spec_immutable;
CREATE TRIGGER execution_run_spec_immutable
BEFORE UPDATE OF
    project_id, target_intent_id, work_key, claim_scope, attempt, previous_run_id,
    fencing_token, dispatcher_id, dispatcher_fencing_token, task_type, input_generation, input_revision,
    input_hash, worker_name, config_revision, image_id, image_repo_digest,
    run_profile, result_schema, execution_spec_json, started_at
ON execution_runs
BEGIN
    SELECT RAISE(ABORT, 'execution run spec is immutable');
END;

CREATE TABLE execution_worker_declines (
    work_key TEXT NOT NULL REFERENCES execution_work_keys(work_key) ON DELETE CASCADE,
    worker_name TEXT NOT NULL,
    retry_after TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (work_key, worker_name)
);

CREATE INDEX execution_worker_declines_retry_idx
    ON execution_worker_declines(work_key, retry_after);

CREATE TABLE execution_recovery_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL REFERENCES execution_runs(id) ON DELETE CASCADE,
    dispatcher_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('observed', 'disposed', 'interrupted', 'dispose_failed')),
    reason TEXT NOT NULL,
    observed_at TEXT NOT NULL
);

CREATE INDEX execution_recovery_events_run_idx ON execution_recovery_events(run_id, id);

CREATE TABLE dispatcher_lease (
    lease_name TEXT PRIMARY KEY CHECK (lease_name = 'global'),
    instance_id TEXT NOT NULL,
    fencing_token INTEGER NOT NULL CHECK (fencing_token > 0),
    acquired_at TEXT NOT NULL,
    renewed_at TEXT NOT NULL,
    lease_expires_at TEXT NOT NULL,
    lease_expires_unix_nano INTEGER NOT NULL
);

UPDATE execution_work_keys
SET failure_count = (
        SELECT COUNT(*) FROM execution_runs r
        WHERE r.work_key = execution_work_keys.work_key
          AND r.status IN ('failed', 'interrupted')
    ),
    retry_state = CASE (
        SELECT r.status FROM execution_runs r
        WHERE r.work_key = execution_work_keys.work_key
        ORDER BY r.attempt DESC LIMIT 1
    )
        WHEN 'committed' THEN 'closed'
        WHEN 'stale' THEN 'closed'
        WHEN 'cancelled' THEN 'closed'
        WHEN 'failed' THEN CASE WHEN (
            SELECT COUNT(*) FROM execution_runs r
            WHERE r.work_key = execution_work_keys.work_key
              AND r.status IN ('failed', 'interrupted')
        ) >= max_attempts THEN 'exhausted' ELSE 'backoff' END
        WHEN 'interrupted' THEN CASE WHEN (
            SELECT COUNT(*) FROM execution_runs r
            WHERE r.work_key = execution_work_keys.work_key
              AND r.status IN ('failed', 'interrupted')
        ) >= max_attempts THEN 'exhausted' ELSE 'backoff' END
        ELSE 'eligible'
    END,
    next_eligible_at = CASE WHEN (
        SELECT r.status FROM execution_runs r
        WHERE r.work_key = execution_work_keys.work_key
        ORDER BY r.attempt DESC LIMIT 1
    ) IN ('failed', 'interrupted') THEN (
        SELECT r.finished_at FROM execution_runs r
        WHERE r.work_key = execution_work_keys.work_key
        ORDER BY r.attempt DESC LIMIT 1
    ) ELSE NULL END,
    updated_at = COALESCE((
        SELECT COALESCE(r.finished_at, r.started_at) FROM execution_runs r
        WHERE r.work_key = execution_work_keys.work_key
        ORDER BY r.attempt DESC LIMIT 1
    ), created_at);
