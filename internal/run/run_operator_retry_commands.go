package run

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
)

type RetryExhaustedRequest struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type RetryExhaustedReceipt struct {
	RequestID       string  `json:"request_id"`
	ProjectRevision int64   `json:"project_revision"`
	RunIDs          []RunID `json:"run_ids"`
	Replayed        bool    `json:"replayed"`
}

// RetryExhaustedWork renews only the budgets of work still applicable to the
// current Project. It never starts a Run or changes the Blackboard Revision.
// A persisted request receipt prevents replay from resetting a later failure.
func (s *Service) RetryExhaustedWork(ctx context.Context, projectID string, request RetryExhaustedRequest) (RetryExhaustedReceipt, error) {
	if !bounded(projectID, 256) || !bounded(request.RequestID, 256) || request.ExpectedRevision < 1 {
		return RetryExhaustedReceipt{}, newError(ErrorInvalidArgument, "Project, request ID and expected Revision are required")
	}
	if strings.TrimSpace(s.operatorName) == "" || s.projects == nil {
		return RetryExhaustedReceipt{}, newError(ErrorNotReady, "Operator retry is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RetryExhaustedReceipt{}, err
	}
	defer tx.Rollback()
	var storedProject string
	var storedRevision int64
	err = tx.QueryRowContext(ctx, `SELECT project_id, expected_revision FROM execution_retry_commands WHERE request_id = ?`, request.RequestID).Scan(&storedProject, &storedRevision)
	if err == nil {
		if storedProject != projectID || storedRevision != request.ExpectedRevision {
			return RetryExhaustedReceipt{}, newError(ErrorConflict, "retry request ID was already used for a different command")
		}
		receipt, err := retryCommandReceipt(ctx, tx, request)
		receipt.Replayed = true
		return receipt, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RetryExhaustedReceipt{}, err
	}
	current, err := s.projects.Admission(ctx, tx, projectID, TaskReason, nil)
	if errors.Is(err, sql.ErrNoRows) {
		return RetryExhaustedReceipt{}, newError(ErrorNotFound, "Project not found")
	}
	if err != nil {
		return RetryExhaustedReceipt{}, err
	}
	if current.Revision != request.ExpectedRevision {
		return RetryExhaustedReceipt{}, newError(ErrorConflict, "Project changed before retry")
	}
	if !current.Active {
		return RetryExhaustedReceipt{}, newError(ErrorProjectInactive, "only an Active Project can retry exhausted work")
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT r.id FROM execution_runs r JOIN execution_work_keys w ON w.work_key = r.work_key
		WHERE w.project_id = ? AND w.retry_state = 'exhausted' AND r.attempt = w.last_attempt
		ORDER BY r.id`, projectID)
	if err != nil {
		return RetryExhaustedReceipt{}, err
	}
	var exhausted []RunID
	for rows.Next() {
		var id RunID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return RetryExhaustedReceipt{}, err
		}
		exhausted = append(exhausted, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return RetryExhaustedReceipt{}, err
	}
	now := s.clock().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_retry_commands (request_id, project_id, expected_revision, operator_name, created_at) VALUES (?, ?, ?, ?, ?)`,
		request.RequestID, projectID, request.ExpectedRevision, s.operatorName, now); err != nil {
		return RetryExhaustedReceipt{}, err
	}
	for _, id := range exhausted {
		stored, err := s.ledger.GetForFinish(ctx, tx, id)
		if err != nil {
			return RetryExhaustedReceipt{}, err
		}
		run := stored.Run
		state, err := s.projects.Admission(ctx, tx, projectID, run.Task, run.TargetIntentID)
		if err != nil {
			return RetryExhaustedReceipt{}, err
		}
		if !state.Active || !state.TargetAvailable || state.Generation != run.Spec.Input.Generation ||
			(run.Task == TaskReason && state.Revision != run.Spec.Input.Revision) {
			continue
		}
		workKey, err := WorkKeyFor(projectID, run.Task, InputSnapshot{
			Generation: state.Generation, Revision: state.Revision, TargetIntentID: run.TargetIntentID,
		})
		if err != nil {
			return RetryExhaustedReceipt{}, err
		}
		if workKey != run.WorkKey {
			return RetryExhaustedReceipt{}, newError(ErrorInternal, "exhausted work identity is inconsistent with Project admission")
		}
		unresolved, err := s.ledger.HasUnresolvedExecutionHandles(ctx, tx, run.ClaimScope)
		if err != nil {
			return RetryExhaustedReceipt{}, err
		}
		if unresolved {
			return RetryExhaustedReceipt{}, newError(ErrorRetryDeferred, "previous execution must be cleaned up before retry")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO execution_retry_resets (request_id, run_id, previous_failure_count)
			SELECT ?, ?, failure_count FROM execution_work_keys WHERE work_key = ?`, request.RequestID, id, run.WorkKey); err != nil {
			return RetryExhaustedReceipt{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE execution_work_keys SET failure_count = 0, retry_state = 'eligible', next_eligible_at = NULL, updated_at = ?
			WHERE work_key = ?`, now, run.WorkKey); err != nil {
			return RetryExhaustedReceipt{}, err
		}
	}
	receipt, err := retryCommandReceipt(ctx, tx, request)
	if err != nil {
		return RetryExhaustedReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return RetryExhaustedReceipt{}, err
	}
	s.publishActivityChanged(projectID)
	return receipt, nil
}

func retryCommandReceipt(ctx context.Context, tx database.DBTX, request RetryExhaustedRequest) (RetryExhaustedReceipt, error) {
	receipt := RetryExhaustedReceipt{RequestID: request.RequestID, ProjectRevision: request.ExpectedRevision, RunIDs: []RunID{}}
	rows, err := tx.QueryContext(ctx, `SELECT run_id FROM execution_retry_resets WHERE request_id = ? ORDER BY run_id`, request.RequestID)
	if err != nil {
		return receipt, err
	}
	defer rows.Close()
	for rows.Next() {
		var id RunID
		if err := rows.Scan(&id); err != nil {
			return receipt, err
		}
		receipt.RunIDs = append(receipt.RunIDs, id)
	}
	return receipt, rows.Err()
}

func readOperatorRetries(ctx context.Context, tx database.DBTX, runID RunID) ([]OperatorRetryEvidence, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT c.request_id, c.operator_name, r.previous_failure_count, c.created_at
		FROM execution_retry_commands c JOIN execution_retry_resets r ON r.request_id = c.request_id
		WHERE r.run_id = ? ORDER BY c.created_at, c.request_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evidence := []OperatorRetryEvidence{}
	for rows.Next() {
		var entry OperatorRetryEvidence
		var createdAt string
		if err := rows.Scan(&entry.RequestID, &entry.OperatorName, &entry.PreviousFailureCount, &createdAt); err != nil {
			return nil, err
		}
		entry.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, entry)
	}
	return evidence, rows.Err()
}
