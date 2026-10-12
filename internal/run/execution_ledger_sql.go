package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
)

type ledgerStore struct{}

func (s *ledgerStore) RecordProjectContainer(
	ctx context.Context,
	tx database.DBTX,
	record ProjectContainerRecordRequest,
	now string,
) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO project_runtime_containers (project_id, container_id, recorded_at)
		SELECT project_id, ?, ?
		FROM execution_runs
		WHERE id = ? AND project_id = ? AND status = 'active' AND fencing_token = ? AND dispatcher_id = ?
		ON CONFLICT(project_id) DO UPDATE SET
			container_id = excluded.container_id,
			recorded_at = excluded.recorded_at`,
		record.Handle.ContainerID, now, record.Handle.RunID, record.Handle.ProjectID,
		record.RunFencingToken, record.DispatcherID,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return newError(ErrorFenced, "execution Run no longer authorizes this Project container")
	}
	return nil
}

func (s *ledgerStore) RecordExecutionHandle(
	ctx context.Context,
	tx database.DBTX,
	record ExecutionHandleRecordRequest,
	now string,
) error {
	result, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO execution_runtime_handles
			(run_id, project_id, phase, container_id, exec_id, started_at, status)
		SELECT id, project_id, ?, ?, ?, ?, 'active'
		FROM execution_runs
		WHERE id = ? AND project_id = ? AND status = 'active' AND fencing_token = ? AND dispatcher_id = ?`,
		record.Handle.Phase, record.Handle.ContainerID, record.Handle.ExecID, now,
		record.Handle.RunID, record.Handle.ProjectID, record.RunFencingToken, record.DispatcherID,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		var exists int
		if queryErr := tx.QueryRowContext(ctx, `
			SELECT 1 FROM execution_runtime_handles
			WHERE run_id = ? AND project_id = ? AND phase = ? AND container_id = ? AND exec_id = ?`,
			record.Handle.RunID, record.Handle.ProjectID, record.Handle.Phase,
			record.Handle.ContainerID, record.Handle.ExecID,
		).Scan(&exists); queryErr == nil && exists == 1 {
			return nil
		}
	}
	if rows != 1 {
		return newError(ErrorFenced, "execution Run no longer authorizes this Exec handle")
	}
	return nil
}

func (s *ledgerStore) ListExecutionHandles(
	ctx context.Context,
	tx database.DBTX,
	runIDs []RunID,
) ([]ExecutionHandle, error) {
	if len(runIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(runIDs))
	args := make([]any, len(runIDs))
	for i, runID := range runIDs {
		placeholders[i], args[i] = "?", runID
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT run_id, project_id, phase, container_id, exec_id, started_at
		FROM execution_runtime_handles
		WHERE status = 'active' AND run_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY started_at, run_id, phase, exec_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var handles []ExecutionHandle
	for rows.Next() {
		var handle ExecutionHandle
		var started string
		if err := rows.Scan(&handle.RunID, &handle.ProjectID, &handle.Phase, &handle.ContainerID, &handle.ExecID, &started); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, fmt.Errorf("parse execution handle start time: %w", err)
		}
		handle.StartedAt = parsed
		handles = append(handles, handle)
	}
	return handles, rows.Err()
}

func (s *ledgerStore) MarkExecutionHandleStopped(ctx context.Context, tx database.DBTX, handle ExecutionHandle, stoppedAt string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE execution_runtime_handles
		SET status = 'stopped', stopped_at = ?
		WHERE run_id = ? AND project_id = ? AND phase = ? AND container_id = ? AND exec_id = ?`,
		stoppedAt, handle.RunID, handle.ProjectID, handle.Phase, handle.ContainerID, handle.ExecID)
	return err
}

func (s *ledgerStore) HasUnresolvedExecutionHandles(ctx context.Context, tx database.DBTX, claimScope ClaimScope) (bool, error) {
	var unresolved bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM execution_runtime_handles h
			JOIN execution_runs r ON r.id = h.run_id
			WHERE r.claim_scope = ? AND h.status = 'active'
		)`, claimScope).Scan(&unresolved)
	return unresolved, err
}

type storedRun struct {
	Run         ExecutionRun
	ResultJSON  sql.NullString
	ReceiptJSON sql.NullString
}

type storedRetryPolicy struct {
	LastAttempt     int64
	FailureCount    int64
	MaxAttempts     int
	InitialBackoff  time.Duration
	MaxBackoff      time.Duration
	Multiplier      float64
	DeclineCooldown time.Duration
}

func (s *ledgerStore) SchedulingRevokedRunIDs(
	ctx context.Context,
	tx database.DBTX,
	runIDs []RunID,
	dispatcherID string,
	dispatcherFencingToken FencingToken,
	observedAt time.Time,
) ([]RunID, error) {
	if len(runIDs) == 0 {
		return []RunID{}, nil
	}
	values := make([]string, len(runIDs))
	arguments := make([]any, 0, len(runIDs)*2+3)
	for index, runID := range runIDs {
		values[index] = "(?, ?)"
		arguments = append(arguments, index, runID)
	}
	arguments = append(arguments, dispatcherID, dispatcherFencingToken, observedAt.UnixNano())
	rows, err := tx.QueryContext(ctx, `
		WITH observed(request_index, run_id) AS (VALUES `+strings.Join(values, ",")+`)
		SELECT observed.run_id
		FROM observed
		LEFT JOIN execution_runs run ON run.id = observed.run_id
		LEFT JOIN projects project ON project.id = run.project_id
		LEFT JOIN execution_claims claim ON claim.run_id = run.id
		WHERE run.id IS NULL
		   OR run.status <> 'active'
		   OR run.dispatcher_id <> ?
		   OR run.dispatcher_fencing_token <> ?
		   OR project.status IS NULL OR project.status <> 'active'
		   OR claim.run_id IS NULL
		   OR claim.dispatcher_id <> run.dispatcher_id
		   OR claim.fencing_token <> run.fencing_token
		   OR COALESCE(claim.lease_expires_unix_nano, 0) <= ?
		ORDER BY observed.request_index`, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	revoked := make([]RunID, 0, len(runIDs))
	for rows.Next() {
		var runID RunID
		if err := rows.Scan(&runID); err != nil {
			return nil, err
		}
		revoked = append(revoked, runID)
	}
	return revoked, rows.Err()
}

func (s *ledgerStore) GetExpiredClaimRun(
	ctx context.Context,
	tx database.DBTX,
	claimScope ClaimScope,
	nowUnixNano int64,
) (storedRun, error) {
	var runID RunID
	if err := tx.QueryRowContext(ctx, `
		SELECT run_id FROM execution_claims
		WHERE claim_scope = ? AND lease_expires_unix_nano <= ?`,
		claimScope, nowUnixNano,
	).Scan(&runID); err != nil {
		return storedRun{}, err
	}
	return s.GetForFinish(ctx, tx, runID)
}

func (s *ledgerStore) WorkRetryState(ctx context.Context, tx database.DBTX, workKey WorkKey) (RetryState, error) {
	var state RetryState
	err := tx.QueryRowContext(ctx,
		"SELECT retry_state FROM execution_work_keys WHERE work_key = ?", workKey,
	).Scan(&state)
	return state, err
}

type reasonSchedulingCheckpoint struct {
	Checkpoint sql.NullInt64
	Baseline   sql.NullInt64
}

func (s *ledgerStore) ReasonSchedulingCheckpoints(
	ctx context.Context,
	tx database.DBTX,
	queries []SchedulingWorkQuery,
) ([]reasonSchedulingCheckpoint, error) {
	result := make([]reasonSchedulingCheckpoint, len(queries))
	const queryChunkSize = 300
	for offset := 0; offset < len(queries); offset += queryChunkSize {
		end := min(offset+queryChunkSize, len(queries))
		values := make([]string, 0, end-offset)
		arguments := make([]any, 0, (end-offset)*3)
		for index, query := range queries[offset:end] {
			values = append(values, "(?, ?, ?)")
			arguments = append(arguments, offset+index, query.ProjectID, query.Generation)
		}
		rows, err := tx.QueryContext(ctx, `
			WITH requested(request_index, project_id, generation) AS (VALUES `+strings.Join(values, ",")+`)
			SELECT requested.request_index, checkpoint.input_revision, baseline.input_revision
			FROM requested
			LEFT JOIN reason_checkpoints checkpoint
			  ON checkpoint.project_id = requested.project_id AND checkpoint.generation = requested.generation
			LEFT JOIN reason_scheduling_baselines baseline
			  ON baseline.project_id = requested.project_id AND baseline.generation = requested.generation
			ORDER BY requested.request_index`, arguments...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var requestIndex int
			var evidence reasonSchedulingCheckpoint
			if err := rows.Scan(&requestIndex, &evidence.Checkpoint, &evidence.Baseline); err != nil {
				rows.Close()
				return nil, err
			}
			if requestIndex < 0 || requestIndex >= len(result) {
				rows.Close()
				return nil, fmt.Errorf("Reason scheduling checkpoint returned an unknown request index")
			}
			result[requestIndex] = evidence
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *ledgerStore) SchedulingWorkStates(
	ctx context.Context,
	tx database.DBTX,
	workKeys []WorkKey,
	observedAt time.Time,
) ([]SchedulingWorkState, error) {
	states := make(map[WorkKey]*SchedulingWorkState, len(workKeys))
	for _, workKey := range workKeys {
		states[workKey] = &SchedulingWorkState{
			WorkKey: workKey, RetryState: RetryEligible,
			WorkerRetryAfter: make(map[string]time.Time),
		}
	}
	const queryChunkSize = 400
	for offset := 0; offset < len(workKeys); offset += queryChunkSize {
		end := min(offset+queryChunkSize, len(workKeys))
		chunk := workKeys[offset:end]
		placeholders, arguments := workKeyQueryArguments(chunk)
		stateArguments := make([]any, 0, len(arguments)+1)
		stateArguments = append(stateArguments, observedAt.UnixNano())
		stateArguments = append(stateArguments, arguments...)
		rows, err := tx.QueryContext(ctx, `
				SELECT work_key, retry_state, last_attempt, next_eligible_at,
					COALESCE((
					SELECT worker_name FROM execution_runs
					WHERE work_key = execution_work_keys.work_key
					ORDER BY attempt DESC LIMIT 1
					), ''),
					EXISTS(
						SELECT 1 FROM execution_claims
						WHERE execution_claims.work_key = execution_work_keys.work_key
						  AND COALESCE(execution_claims.lease_expires_unix_nano, 0) > ?
					)
				FROM execution_work_keys WHERE work_key IN (`+placeholders+`)`, stateArguments...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var workKey WorkKey
			var nextEligible sql.NullString
			var retryState RetryState
			var attempt int64
			var previousWorker string
			var claimed bool
			if err := rows.Scan(&workKey, &retryState, &attempt, &nextEligible, &previousWorker, &claimed); err != nil {
				rows.Close()
				return nil, err
			}
			state := states[workKey]
			if state == nil {
				rows.Close()
				return nil, fmt.Errorf("scheduling state returned unrequested Work Key %s", workKey)
			}
			state.RetryState, state.Claimed, state.Attempt, state.PreviousWorker = retryState, claimed, attempt, previousWorker
			if nextEligible.Valid {
				value, err := time.Parse(time.RFC3339Nano, nextEligible.String)
				if err != nil {
					rows.Close()
					return nil, fmt.Errorf("parse Work eligibility for %s: %w", workKey, err)
				}
				state.NextEligibleAt = &value
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}

		rows, err = tx.QueryContext(ctx, `
			SELECT work_key, worker_name, retry_after
			FROM execution_worker_declines WHERE work_key IN (`+placeholders+`)`, arguments...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var workKey WorkKey
			var workerName, retryAfterText string
			if err := rows.Scan(&workKey, &workerName, &retryAfterText); err != nil {
				rows.Close()
				return nil, err
			}
			retryAfter, err := time.Parse(time.RFC3339Nano, retryAfterText)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("parse Worker retry-after for %s: %w", workKey, err)
			}
			state := states[workKey]
			if state == nil {
				rows.Close()
				return nil, fmt.Errorf("Worker cooldown returned unrequested Work Key %s", workKey)
			}
			state.WorkerRetryAfter[workerName] = retryAfter
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	ordered := make([]SchedulingWorkState, 0, len(workKeys))
	for _, workKey := range workKeys {
		ordered = append(ordered, *states[workKey])
	}
	return ordered, nil
}

func workKeyQueryArguments(workKeys []WorkKey) (string, []any) {
	placeholders := make([]string, len(workKeys))
	arguments := make([]any, len(workKeys))
	for index, workKey := range workKeys {
		placeholders[index], arguments[index] = "?", workKey
	}
	return strings.Join(placeholders, ","), arguments
}

func (s *ledgerStore) DispatcherLeaseMatches(
	ctx context.Context,
	tx database.DBTX,
	dispatcherID string,
	fencingToken FencingToken,
	nowUnixNano int64,
) (bool, error) {
	var matched int
	err := tx.QueryRowContext(ctx, `
		SELECT 1 FROM dispatcher_lease
		WHERE lease_name = 'global' AND instance_id = ? AND fencing_token = ?
		  AND lease_expires_unix_nano > ?`,
		dispatcherID, fencingToken, nowUnixNano,
	).Scan(&matched)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *ledgerStore) DispatcherInstanceOwnsLease(
	ctx context.Context,
	tx database.DBTX,
	dispatcherID string,
	nowUnixNano int64,
) (bool, error) {
	var matched int
	err := tx.QueryRowContext(ctx, `
		SELECT 1 FROM dispatcher_lease
		WHERE lease_name = 'global' AND instance_id = ?
		  AND lease_expires_unix_nano > ?`,
		dispatcherID, nowUnixNano,
	).Scan(&matched)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *ledgerStore) HasCompetingClaim(
	ctx context.Context,
	tx database.DBTX,
	claimScope ClaimScope,
) (bool, error) {
	var claimed int
	err := tx.QueryRowContext(ctx,
		"SELECT 1 FROM execution_claims WHERE claim_scope = ?", claimScope,
	).Scan(&claimed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *ledgerStore) AllocateExecutionRight(
	ctx context.Context,
	tx database.DBTX,
	workKey WorkKey,
	claimScope ClaimScope,
	projectID string,
	task TaskType,
	targetIntentID *string,
	workerName string,
	policy RetryPolicy,
	createdAt string,
) (fencingToken FencingToken, attempt int64, previousRunID *RunID, err error) {
	policy, err = policy.Normalized()
	if err != nil {
		return 0, 0, nil, err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO execution_work_keys
			(work_key, project_id, task_type, target_intent_id, created_at,
			 retry_state, max_attempts, initial_backoff_ms, max_backoff_ms,
			 backoff_multiplier, decline_cooldown_ms, updated_at)
		VALUES (?, ?, ?, ?, ?, 'eligible', ?, ?, ?, ?, ?, ?)`,
		workKey, projectID, task, targetIntentID, createdAt,
		policy.MaxAttempts, policy.InitialBackoff.Milliseconds(), policy.MaxBackoff.Milliseconds(),
		policy.Multiplier, policy.DeclineCooldown.Milliseconds(), createdAt,
	); err != nil {
		return 0, 0, nil, err
	}
	var retryState RetryState
	var nextEligibleAt sql.NullString
	if err = tx.QueryRowContext(ctx, `
		SELECT retry_state, next_eligible_at
		FROM execution_work_keys WHERE work_key = ?`, workKey,
	).Scan(&retryState, &nextEligibleAt); err != nil {
		return 0, 0, nil, err
	}
	if retryState == RetryExhausted {
		return 0, 0, nil, &DomainError{Code: ErrorRetryExhausted, Detail: "technical retry budget is exhausted"}
	}
	if retryState == RetryClosed {
		return 0, 0, nil, &DomainError{Code: ErrorRetryExhausted, Detail: "work item is already closed"}
	}
	if nextEligibleAt.Valid {
		next, parseErr := time.Parse(time.RFC3339Nano, nextEligibleAt.String)
		if parseErr != nil {
			return 0, 0, nil, fmt.Errorf("parse work key eligibility: %w", parseErr)
		}
		now, parseErr := time.Parse(time.RFC3339Nano, createdAt)
		if parseErr != nil {
			return 0, 0, nil, fmt.Errorf("parse run allocation time: %w", parseErr)
		}
		if next.After(now) {
			return 0, 0, nil, &DomainError{Code: ErrorRetryDeferred, Detail: "technical retry backoff has not elapsed"}
		}
	}
	var workerRetryAfter sql.NullString
	if err = tx.QueryRowContext(ctx, `
		SELECT retry_after FROM execution_worker_declines
		WHERE work_key = ? AND worker_name = ?`, workKey, workerName,
	).Scan(&workerRetryAfter); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil, err
	}
	if workerRetryAfter.Valid {
		retryAfter, parseErr := time.Parse(time.RFC3339Nano, workerRetryAfter.String)
		if parseErr != nil {
			return 0, 0, nil, fmt.Errorf("parse Worker decline cooldown: %w", parseErr)
		}
		now, parseErr := time.Parse(time.RFC3339Nano, createdAt)
		if parseErr != nil {
			return 0, 0, nil, fmt.Errorf("parse run allocation time: %w", parseErr)
		}
		if retryAfter.After(now) {
			return 0, 0, nil, &DomainError{Code: ErrorRetryDeferred, Detail: "Worker decline cooldown has not elapsed"}
		}
	}
	var previous sql.NullString
	if err = tx.QueryRowContext(ctx, `
		SELECT id FROM execution_runs
		WHERE work_key = ? ORDER BY attempt DESC LIMIT 1`, workKey,
	).Scan(&previous); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil, err
	}
	if previous.Valid {
		value := RunID(previous.String)
		previousRunID = &value
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE execution_work_keys
		SET last_attempt = last_attempt + 1, retry_state = 'eligible',
			next_eligible_at = NULL, updated_at = ?
		WHERE work_key = ?`, createdAt, workKey,
	); err != nil {
		return 0, 0, nil, err
	}
	scopeType := "intent"
	if task == TaskReason {
		scopeType = "reason"
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO execution_claim_scopes
			(claim_scope, project_id, scope_type, target_intent_id, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		claimScope, projectID, scopeType, targetIntentID, createdAt,
	); err != nil {
		return 0, 0, nil, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE execution_claim_scopes
		SET last_fencing_token = last_fencing_token + 1
		WHERE claim_scope = ?`, claimScope,
	); err != nil {
		return 0, 0, nil, err
	}
	if err = tx.QueryRowContext(ctx, `
		SELECT w.last_attempt, s.last_fencing_token
		FROM execution_work_keys w, execution_claim_scopes s
		WHERE w.work_key = ? AND s.claim_scope = ?`, workKey, claimScope,
	).Scan(&attempt, &fencingToken); err != nil {
		return 0, 0, nil, err
	}
	return fencingToken, attempt, previousRunID, nil
}

func (s *ledgerStore) CreateRunAndClaim(ctx context.Context, tx database.DBTX, started StartedRun) error {
	spec, err := started.Run.Spec.normalized()
	if err != nil {
		return fmt.Errorf("normalize execution spec snapshot: %w", err)
	}
	snapshot, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal execution spec snapshot: %w", err)
	}
	targetIntentID := started.Run.TargetIntentID
	var previousRunID any
	if started.Run.PreviousRunID != nil {
		previousRunID = string(*started.Run.PreviousRunID)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO execution_runs (
			id, project_id, target_intent_id, work_key, claim_scope, attempt, previous_run_id, status,
			fencing_token, dispatcher_id, dispatcher_fencing_token, task_type,
			execution_spec_schema, execution_spec_json, started_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		started.Run.ID, started.Run.ProjectID, targetIntentID, started.Run.WorkKey,
		started.Run.ClaimScope, started.Run.Attempt, previousRunID, started.Run.Status, started.Run.FencingToken,
		started.Run.DispatcherID, started.Run.DispatcherFencingToken,
		started.Run.Task, spec.Schema, string(snapshot), started.Run.StartedAt.Format(time.RFC3339Nano),
	); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO execution_claims (
			claim_scope, work_key, run_id, project_id, target_intent_id, task_type,
			fencing_token, dispatcher_id, claimed_at, lease_expires_at, lease_expires_unix_nano
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		started.Claim.Scope, started.Claim.WorkKey, started.Claim.RunID, started.Claim.ProjectID,
		started.Claim.TargetIntentID, started.Claim.Task,
		started.Claim.FencingToken, started.Claim.DispatcherID,
		started.Claim.ClaimedAt.Format(time.RFC3339Nano),
		started.Claim.LeaseExpiresAt.Format(time.RFC3339Nano),
		started.Claim.LeaseExpiresAt.UnixNano(),
	)
	return err
}

func (s *ledgerStore) GetForFinish(ctx context.Context, conn database.DBTX, runID RunID) (storedRun, error) {
	var stored storedRun
	var targetIntentID, previousRunID, nextEligibleAt sql.NullString
	var snapshotSchema, snapshot, startedAt string
	var finishedAt, resultHash, outcomeKind, errorCode, captureFailureReason, captureDiagnostic, resultEvidenceSource sql.NullString
	var recoveryPhase, recoveryMode, recoveryStartedAt sql.NullString
	var recoveryUsed int
	err := conn.QueryRowContext(ctx, `
		SELECT id, project_id, target_intent_id, work_key, claim_scope, attempt,
			previous_run_id, next_eligible_at, status,
			fencing_token, dispatcher_id, dispatcher_fencing_token, task_type,
			execution_spec_schema, execution_spec_json, started_at,
			finished_at, result_hash, outcome_kind, error_code, capture_failure_reason,
			capture_diagnostic, result_evidence_source, recovery_used, recovery_phase, recovery_mode, recovery_started_at,
			result_json, finish_receipt_json
		FROM execution_runs WHERE id = ?`, runID,
	).Scan(
		&stored.Run.ID, &stored.Run.ProjectID, &targetIntentID, &stored.Run.WorkKey,
		&stored.Run.ClaimScope, &stored.Run.Attempt, &previousRunID, &nextEligibleAt,
		&stored.Run.Status,
		&stored.Run.FencingToken, &stored.Run.DispatcherID, &stored.Run.DispatcherFencingToken, &stored.Run.Task,
		&snapshotSchema, &snapshot, &startedAt, &finishedAt, &resultHash, &outcomeKind, &errorCode,
		&captureFailureReason, &captureDiagnostic, &resultEvidenceSource, &recoveryUsed, &recoveryPhase, &recoveryMode, &recoveryStartedAt,

		&stored.ResultJSON, &stored.ReceiptJSON,
	)
	if err != nil {
		return storedRun{}, err
	}
	if targetIntentID.Valid {
		stored.Run.TargetIntentID = &targetIntentID.String
	}
	if previousRunID.Valid {
		value := RunID(previousRunID.String)
		stored.Run.PreviousRunID = &value
	}
	if nextEligibleAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, nextEligibleAt.String)
		if parseErr != nil {
			return storedRun{}, fmt.Errorf("parse execution run eligibility: %w", parseErr)
		}
		stored.Run.NextEligibleAt = &parsed
	}
	if err := json.Unmarshal([]byte(snapshot), &stored.Run.Spec); err != nil {
		return storedRun{}, fmt.Errorf("decode execution spec snapshot: %w", err)
	}
	if stored.Run.Spec.Schema != snapshotSchema {
		return storedRun{}, fmt.Errorf(
			"execution spec row schema %q disagrees with document schema %q",
			snapshotSchema, stored.Run.Spec.Schema,
		)
	}
	stored.Run.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return storedRun{}, fmt.Errorf("parse execution run start time: %w", err)
	}
	if finishedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, finishedAt.String)
		if err != nil {
			return storedRun{}, fmt.Errorf("parse execution run finish time: %w", err)
		}
		stored.Run.FinishedAt = &parsed
	}
	if resultHash.Valid {
		stored.Run.ResultHash = resultHash.String
	}
	if outcomeKind.Valid {
		stored.Run.OutcomeKind = OutcomeKind(outcomeKind.String)
	}
	if errorCode.Valid {
		stored.Run.ErrorCode = ErrorCode(errorCode.String)
	}
	if captureFailureReason.Valid {
		stored.Run.CaptureFailureReason = CaptureFailureReason(captureFailureReason.String)
	}
	if captureDiagnostic.Valid {
		stored.Run.CaptureDiagnostic = CaptureDiagnostic(captureDiagnostic.String)
	}
	if resultEvidenceSource.Valid {
		stored.Run.ResultEvidenceSource = ResultEvidenceSource(resultEvidenceSource.String)
	}
	stored.Run.RecoveryUsed = recoveryUsed != 0
	if recoveryMode.Valid {
		stored.Run.RecoveryPhase = processRecoveryPhase(ProcessRecoveryMode(recoveryMode.String))
		if stored.Run.RecoveryPhase == "" {
			return storedRun{}, fmt.Errorf("invalid process recovery mode %q", recoveryMode.String)
		}
	} else if recoveryPhase.Valid {
		stored.Run.RecoveryPhase = ExecutionPhase(recoveryPhase.String)
	}
	if recoveryStartedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, recoveryStartedAt.String)
		if err != nil {
			return storedRun{}, fmt.Errorf("parse process recovery start time: %w", err)
		}
		stored.Run.RecoveryStartedAt = &parsed
	}
	return stored, nil
}

func (s *ledgerStore) ClaimMatches(
	ctx context.Context,
	tx database.DBTX,
	run ExecutionRun,
	nowUnixNano int64,
) (bool, error) {
	var matches int
	err := tx.QueryRowContext(ctx, `
		SELECT 1 FROM execution_claims
		WHERE claim_scope = ? AND work_key = ? AND run_id = ? AND project_id = ? AND task_type = ?
		  AND fencing_token = ? AND dispatcher_id = ?
		  AND lease_expires_unix_nano > ?`,
		run.ClaimScope, run.WorkKey, run.ID, run.ProjectID, run.Task,
		run.FencingToken, run.DispatcherID, nowUnixNano,
	).Scan(&matches)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *ledgerStore) ReserveProcessRecovery(
	ctx context.Context,
	tx database.DBTX,
	request BeginProcessRecoveryRequest,
	reservedAt string,
) (int64, error) {
	recoveryPhase := processRecoveryPhase(request.Mode)
	result, err := tx.ExecContext(ctx, `
		UPDATE execution_runs
		SET recovery_used = 1, recovery_phase = ?, recovery_mode = ?, recovery_started_at = ?
		WHERE id = ? AND status = 'active' AND fencing_token = ?
		  AND dispatcher_id = ? AND dispatcher_fencing_token = ? AND recovery_used = 0`,
		recoveryPhase, request.Mode, reservedAt, request.RunID, request.FencingToken,
		request.DispatcherID, request.DispatcherFencingToken,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *ledgerStore) RenewClaim(
	ctx context.Context,
	tx database.DBTX,
	request RenewRunRequest,
	now, leaseExpiresAt time.Time,
) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		UPDATE execution_claims
		SET lease_expires_at = ?, lease_expires_unix_nano = ?
		WHERE claim_scope = (
			SELECT claim_scope FROM execution_runs
			WHERE id = ? AND status = 'active'
		)
		AND run_id = ? AND fencing_token = ? AND dispatcher_id = ?
		AND lease_expires_unix_nano > ?`,
		leaseExpiresAt.Format(time.RFC3339Nano), leaseExpiresAt.UnixNano(),
		request.RunID, request.RunID, request.FencingToken,
		request.DispatcherID, now.UnixNano(),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *ledgerStore) ListActiveRuns(ctx context.Context, tx database.DBTX) ([]storedRun, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM execution_runs
		WHERE status = 'active'
		ORDER BY started_at, id`)
	if err != nil {
		return nil, err
	}
	var ids []RunID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, RunID(id))
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	active := make([]storedRun, 0, len(ids))
	for _, id := range ids {
		stored, err := s.GetForFinish(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		active = append(active, stored)
	}
	return active, nil
}

func (s *ledgerStore) RecordRecoveryEvent(
	ctx context.Context,
	tx database.DBTX,
	runID RunID,
	dispatcherID, status, reason, observedAt string,
) error {
	if status != "observed" && status != "disposed" && status != "interrupted" && status != "dispose_failed" {
		return fmt.Errorf("invalid recovery event status %q", status)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO execution_recovery_events (run_id, dispatcher_id, status, reason, observed_at)
		VALUES (?, ?, ?, ?, ?)`, runID, dispatcherID, status, reason, observedAt)
	return err
}

func (s *ledgerStore) ListPendingRecoveryRunIDs(ctx context.Context, tx database.DBTX) ([]RunID, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT r.id
		FROM execution_runs r
		WHERE (r.status = 'interrupted'
		  AND r.error_code = ?
		  AND NOT EXISTS (
			SELECT 1 FROM execution_recovery_events e
			WHERE e.run_id = r.id AND e.status = 'disposed'
		  )) OR (r.status != 'active' AND EXISTS (
			SELECT 1 FROM execution_runtime_handles h WHERE h.run_id = r.id AND h.status = 'active'
		  ))
		ORDER BY r.started_at, r.id`, ErrorDispatcherRestarted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runIDs []RunID
	for rows.Next() {
		var runID RunID
		if err := rows.Scan(&runID); err != nil {
			return nil, err
		}
		runIDs = append(runIDs, runID)
	}
	return runIDs, rows.Err()
}

func (s *ledgerStore) Finish(
	ctx context.Context,
	tx database.DBTX,
	runID RunID,
	status Status,
	resultHash string,
	outcomeKind OutcomeKind,
	errorCode ErrorCode,
	captureDiagnostic CaptureDiagnostic,
	resultEvidenceSource ResultEvidenceSource,
	resultJSON, receiptJSON, finishedAt string,
) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		UPDATE execution_runs
		SET status = ?, result_hash = ?, outcome_kind = ?, result_json = ?,
			error_code = NULLIF(?, ''), capture_diagnostic = NULLIF(?, ''),
			result_evidence_source = NULLIF(?, ''),
			finish_receipt_json = ?, finished_at = ?
		WHERE id = ? AND status = 'active'`,
		status, resultHash, outcomeKind, resultJSON, errorCode, captureDiagnostic,
		resultEvidenceSource, receiptJSON, finishedAt, runID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *ledgerStore) FinishWithoutResult(
	ctx context.Context,
	tx database.DBTX,
	runID RunID,
	status Status,
	errorCode ErrorCode,
	captureFailureReason CaptureFailureReason,
	captureDiagnostic CaptureDiagnostic,
	receiptJSON, finishedAt string,
) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		UPDATE execution_runs
		SET status = ?, result_hash = NULL, outcome_kind = NULL, result_json = NULL,
			error_code = NULLIF(?, ''), capture_failure_reason = NULLIF(?, ''),
			capture_diagnostic = NULLIF(?, ''), result_evidence_source = NULL,
			finish_receipt_json = ?, finished_at = ?
		WHERE id = ? AND status = 'active'`,
		status, errorCode, captureFailureReason, captureDiagnostic, receiptJSON, finishedAt, runID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ApplyRetryOutcome advances the persisted Work Key chain in the same
// transaction that commits the Run terminal state.
func (s *ledgerStore) ApplyRetryOutcome(
	ctx context.Context,
	tx database.DBTX,
	run ExecutionRun,
	status Status,
	code ErrorCode,
	finishedAt string,
) error {
	policy, err := s.retryPolicy(ctx, tx, run.WorkKey)
	if err != nil {
		return err
	}
	if run.Attempt != policy.LastAttempt {
		return nil
	}
	now, err := time.Parse(time.RFC3339Nano, finishedAt)
	if err != nil {
		return fmt.Errorf("parse retry outcome time: %w", err)
	}

	transition, err := nextRetryOutcome(run, status, code, RetryPolicy{
		MaxAttempts: policy.MaxAttempts, InitialBackoff: policy.InitialBackoff,
		MaxBackoff: policy.MaxBackoff, Multiplier: policy.Multiplier,
		DeclineCooldown: policy.DeclineCooldown,
	}, policy.FailureCount, now)
	if err != nil {
		return err
	}
	if status == StatusDeclined {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO execution_worker_declines
				(work_key, worker_name, retry_after, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(work_key, worker_name) DO UPDATE SET
				retry_after = excluded.retry_after,
				updated_at = excluded.updated_at`,
			run.WorkKey, run.Spec.Worker.Name, transition.runEligibleAt.Format(time.RFC3339Nano), finishedAt,
		); err != nil {
			return err
		}
	}

	var workNext, runNext any
	if transition.workEligibleAt != nil {
		workNext = transition.workEligibleAt.Format(time.RFC3339Nano)
	}
	if transition.runEligibleAt != nil {
		runNext = transition.runEligibleAt.Format(time.RFC3339Nano)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE execution_work_keys
		SET retry_state = ?, failure_count = ?, next_eligible_at = ?, updated_at = ?
		WHERE work_key = ?`,
		transition.state, transition.failureCount, workNext, finishedAt, run.WorkKey,
	); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE execution_runs SET next_eligible_at = ? WHERE id = ?`, runNext, run.ID,
	)
	return err
}

func (s *ledgerStore) retryPolicy(ctx context.Context, tx database.DBTX, workKey WorkKey) (storedRetryPolicy, error) {
	var policy storedRetryPolicy
	var initialBackoffMS, maxBackoffMS, declineCooldownMS int64
	err := tx.QueryRowContext(ctx, `
		SELECT last_attempt, failure_count, max_attempts, initial_backoff_ms, max_backoff_ms,
			backoff_multiplier, decline_cooldown_ms
		FROM execution_work_keys WHERE work_key = ?`, workKey,
	).Scan(
		&policy.LastAttempt, &policy.FailureCount, &policy.MaxAttempts, &initialBackoffMS, &maxBackoffMS,
		&policy.Multiplier, &declineCooldownMS,
	)
	if err != nil {
		return storedRetryPolicy{}, err
	}
	policy.InitialBackoff = time.Duration(initialBackoffMS) * time.Millisecond
	policy.MaxBackoff = time.Duration(maxBackoffMS) * time.Millisecond
	policy.DeclineCooldown = time.Duration(declineCooldownMS) * time.Millisecond
	if _, err := (RetryPolicy{
		MaxAttempts: policy.MaxAttempts, InitialBackoff: policy.InitialBackoff,
		MaxBackoff: policy.MaxBackoff, Multiplier: policy.Multiplier,
		DeclineCooldown: policy.DeclineCooldown,
	}).Normalized(); err != nil {
		return storedRetryPolicy{}, fmt.Errorf("stored retry policy is invalid: %w", err)
	}
	return policy, nil
}

func (s *ledgerStore) ReleaseClaim(ctx context.Context, tx database.DBTX, run ExecutionRun) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		DELETE FROM execution_claims
		WHERE claim_scope = ? AND run_id = ? AND fencing_token = ?`,
		run.ClaimScope, run.ID, run.FencingToken,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *ledgerStore) RecordReasonCheckpoint(
	ctx context.Context,
	tx database.DBTX,
	projectID string,
	generation int,
	inputRevision int64,
	runID RunID,
	checkedAt string,
) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO reason_checkpoints
			(project_id, generation, input_revision, run_id, checked_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, generation) DO UPDATE SET
			input_revision = excluded.input_revision,
			run_id = excluded.run_id,
			checked_at = excluded.checked_at`,
		projectID, generation, inputRevision, runID, checkedAt,
	)
	return err
}

func (s *ledgerStore) EnsureReasonSchedulingBaseline(
	ctx context.Context,
	tx database.DBTX,
	projectID string,
	generation int,
	inputRevision int64,
	createdAt string,
) (int64, error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO reason_scheduling_baselines
			(project_id, generation, input_revision, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id, generation) DO NOTHING`,
		projectID, generation, inputRevision, createdAt,
	); err != nil {
		return 0, err
	}
	var baseline int64
	err := tx.QueryRowContext(ctx, `
		SELECT input_revision FROM reason_scheduling_baselines
		WHERE project_id = ? AND generation = ?`, projectID, generation,
	).Scan(&baseline)
	return baseline, err
}

func (s *ledgerStore) Get(ctx context.Context, conn database.DBTX, runID RunID) (ExecutionRun, error) {
	stored, err := s.GetForFinish(ctx, conn, runID)
	return stored.Run, err
}
