package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
)

// EnsureReasonSchedulingBaseline records the Project revision from which the
// next Reason scheduling decision must replay changes.
func (s *Service) EnsureReasonSchedulingBaseline(
	ctx context.Context,
	tx database.DBTX,
	projectID string,
	generation int,
	inputRevision int64,
	createdAt string,
) error {
	_, err := s.ledger.EnsureReasonSchedulingBaseline(
		ctx, tx, projectID, generation, inputRevision, createdAt,
	)
	return err
}

// RunCancellationClause selects affected Runs. Clauses are ORed and fields
// inside a clause are ANDed.
type RunCancellationClause struct {
	Tasks               []TaskType
	TargetIntentID      *string
	SourceFactID        *string
	InputRevisionAtMost *int64
}

type RunCancellationSelector struct {
	Clauses []RunCancellationClause
}

func (s *Service) PreviewRunCancellation(
	ctx context.Context,
	tx database.DBTX,
	projectID string,
	selector RunCancellationSelector,
) (int, error) {
	active, err := s.ledger.ListActiveRuns(ctx, tx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, stored := range active {
		if stored.Run.ProjectID != projectID {
			continue
		}
		matches, err := runMatchesCancellation(ctx, tx, stored.Run, selector)
		if err != nil {
			return 0, err
		}
		if matches {
			count++
		}
	}
	return count, nil
}

func (s *Service) CancelRuns(
	ctx context.Context,
	tx database.DBTX,
	projectID string,
	projectRevision int64,
	code ErrorCode,
	selector RunCancellationSelector,
) (int, error) {
	if !validCancellationCode(code) {
		return 0, &DomainError{Code: ErrorInvalidArgument, Detail: "Run cancellation code is invalid"}
	}
	active, err := s.ledger.ListActiveRuns(ctx, tx)
	if err != nil {
		return 0, err
	}
	now := s.clock()
	nowText := now.Format(time.RFC3339Nano)
	cancelled := 0
	for _, stored := range active {
		run := stored.Run
		if run.ProjectID != projectID {
			continue
		}
		matches, err := runMatchesCancellation(ctx, tx, run, selector)
		if err != nil {
			return 0, err
		}
		if !matches {
			continue
		}
		receipt := FinishReceipt{
			RunID: run.ID, Status: StatusCancelled, Code: code,
			ProjectRevision: projectRevision, FinishedAt: now,
		}
		receiptJSON, err := json.Marshal(receipt)
		if err != nil {
			return 0, fmt.Errorf("marshal Project cancellation receipt: %w", err)
		}
		rows, err := s.ledger.FinishWithoutResult(
			ctx, tx, run.ID, receipt.Status, receipt.Code, "", "", string(receiptJSON), nowText,
		)
		if err != nil {
			return 0, err
		}
		if rows != 1 {
			return 0, &DomainError{Code: ErrorConflict, Detail: "execution Run changed during cancellation"}
		}
		if err := s.ledger.ApplyRetryOutcome(ctx, tx, run, receipt.Status, receipt.Code, nowText); err != nil {
			return 0, err
		}
		if _, err := s.ledger.ReleaseClaim(ctx, tx, run); err != nil {
			return 0, err
		}
		cancelled++
	}
	return cancelled, nil
}

func validCancellationCode(code ErrorCode) bool {
	switch code {
	case ErrorProjectPaused, ErrorProjectCompleted, ErrorBlackboardChanged,
		ErrorFactRetracted, ErrorIntentCancelled, ErrorOperatorCompleted, ErrorProjectDeleted:
		return true
	default:
		return false
	}
}

// QueueProjectCleanup copies runtime evidence into a Project-independent job
// before the Project cascade removes the Run ledger.
func (s *Service) QueueProjectCleanup(
	ctx context.Context, tx database.DBTX, projectID, createdAt string,
) (string, error) {
	var jobID string
	if err := tx.QueryRowContext(ctx, `
			INSERT INTO project_cleanup_jobs (id, project_id, container_id, created_at, updated_at)
			VALUES (
				'cleanup-' || lower(hex(randomblob(16))), ?,
				COALESCE(
					(SELECT container_id FROM project_runtime_containers WHERE project_id = ?),
					(SELECT container_id FROM execution_runtime_handles
					 WHERE project_id = ? ORDER BY started_at DESC, exec_id DESC LIMIT 1)
				),
				?, ?
			)
			RETURNING id`, projectID, projectID, projectID, createdAt, createdAt).Scan(&jobID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO project_cleanup_handles (job_id, run_id, phase, container_id, exec_id)
		SELECT ?, run_id, phase, container_id, exec_id
		FROM execution_runtime_handles
		WHERE project_id = ? AND status = 'active'`, jobID, projectID); err != nil {
		return "", err
	}
	return jobID, nil
}

func runMatchesCancellation(
	ctx context.Context,
	tx database.DBTX,
	run ExecutionRun,
	selector RunCancellationSelector,
) (bool, error) {
	for _, clause := range selector.Clauses {
		if len(clause.Tasks) > 0 && !containsTask(clause.Tasks, run.Task) {
			continue
		}
		if clause.TargetIntentID != nil && (run.TargetIntentID == nil || *run.TargetIntentID != *clause.TargetIntentID) {
			continue
		}
		if clause.InputRevisionAtMost != nil && run.Spec.Input.Revision > *clause.InputRevisionAtMost {
			continue
		}
		if clause.SourceFactID != nil {
			if run.TargetIntentID == nil {
				continue
			}
			var exists int
			err := tx.QueryRowContext(ctx, `
				SELECT 1 FROM intent_sources
				WHERE project_id = ? AND intent_id = ? AND fact_id = ?`,
				run.ProjectID, *run.TargetIntentID, *clause.SourceFactID,
			).Scan(&exists)
			if err != nil {
				if err == sql.ErrNoRows {
					continue
				}
				return false, err
			}
		}
		return true, nil
	}
	return false, nil
}

func containsTask(tasks []TaskType, target TaskType) bool {
	for _, task := range tasks {
		if task == target {
			return true
		}
	}
	return false
}
