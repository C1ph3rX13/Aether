package run

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
)

// ExecutionActivityReader exposes Run-owned activity semantics to read-model
// modules. Callers own the read transaction so projections can compose a
// single database snapshot with Project and Blackboard data.
type ExecutionActivityReader interface {
	ReadProjectExecutionStatuses(context.Context, database.DBTX) (map[string]ProjectExecutionStatus, error)
	ReadWorkspaceActivityEvidence(context.Context, database.DBTX, string, *string) (WorkspaceActivityEvidence, error)
}

// ProjectExecutionStatus summarizes current Run evidence independently of
// Project lifecycle. Idle does not imply that no work is waiting for admission.
type ProjectExecutionStatus string

const (
	ProjectExecutionRetryExhausted      ProjectExecutionStatus = "retry_exhausted"
	ProjectExecutionFailedOrInterrupted ProjectExecutionStatus = "failed_or_interrupted"
	ProjectExecutionRetrying            ProjectExecutionStatus = "retrying"
	ProjectExecutionRunning             ProjectExecutionStatus = "running"
	ProjectExecutionIdle                ProjectExecutionStatus = "idle"
)

type WorkspaceActivityEvidence struct {
	ActiveReason      *ActivityRun
	SelectedIntentRun *ActivityRun
}

func (s *Service) ReadProjectExecutionStatuses(
	ctx context.Context,
	tx database.DBTX,
) (map[string]ProjectExecutionStatus, error) {
	if err := validateProjectBackoffTimes(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, projectExecutionStatusQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	statuses := make(map[string]ProjectExecutionStatus)
	for rows.Next() {
		var projectID string
		var activeReasonCount int
		var reasonRunID, reasonWorker, reasonStartedAt sql.NullString
		var reasonGeneration, reasonRevision sql.NullInt64
		var activeCount int
		var exhaustedCount, failedCount, backoffCount int
		if err := rows.Scan(
			&projectID,
			&activeReasonCount,
			&reasonRunID,
			&reasonWorker,
			&reasonStartedAt,
			&reasonGeneration,
			&reasonRevision,
			&activeCount,
			&exhaustedCount,
			&failedCount,
			&backoffCount,
		); err != nil {
			return nil, err
		}
		if activeReasonCount > 1 {
			return nil, fmt.Errorf("project %s has multiple active Reason runs", projectID)
		}
		hasReasonEvidence := reasonRunID.Valid || reasonWorker.Valid || reasonStartedAt.Valid || reasonGeneration.Valid || reasonRevision.Valid
		if activeReasonCount == 0 && hasReasonEvidence {
			return nil, fmt.Errorf("project %s active Reason evidence is inconsistent", projectID)
		}
		if activeReasonCount == 1 && (!reasonRunID.Valid || strings.TrimSpace(reasonRunID.String) == "" ||
			!reasonWorker.Valid || strings.TrimSpace(reasonWorker.String) == "" ||
			!reasonStartedAt.Valid || strings.TrimSpace(reasonStartedAt.String) == "" ||
			!reasonGeneration.Valid || reasonGeneration.Int64 < 1 ||
			!reasonRevision.Valid || reasonRevision.Int64 < 0) {
			return nil, fmt.Errorf("project %s active Reason evidence is incomplete", projectID)
		}
		if activeReasonCount == 1 {
			_, err := time.Parse(time.RFC3339Nano, reasonStartedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse active Reason start for project %s: %w", projectID, err)
			}
		}

		switch {
		case exhaustedCount > 0:
			statuses[projectID] = ProjectExecutionRetryExhausted
		case failedCount > 0:
			statuses[projectID] = ProjectExecutionFailedOrInterrupted
		case backoffCount > 0:
			statuses[projectID] = ProjectExecutionRetrying
		case activeCount > 0:
			statuses[projectID] = ProjectExecutionRunning
		default:
			statuses[projectID] = ProjectExecutionIdle
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return statuses, nil
}

func validateProjectBackoffTimes(ctx context.Context, tx database.DBTX) error {
	rows, err := tx.QueryContext(ctx, `
SELECT project_id, next_eligible_at FROM execution_work_keys WHERE retry_state = 'backoff'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var projectID string
		var nextEligibleAt sql.NullString
		if err := rows.Scan(&projectID, &nextEligibleAt); err != nil {
			return err
		}
		if !nextEligibleAt.Valid {
			return fmt.Errorf("project %s retry backoff time is missing", projectID)
		}
		if _, err := time.Parse(time.RFC3339Nano, nextEligibleAt.String); err != nil {
			return fmt.Errorf("parse project %s retry backoff time: %w", projectID, err)
		}
	}
	return rows.Err()
}

func (s *Service) ReadWorkspaceActivityEvidence(
	ctx context.Context,
	tx database.DBTX,
	projectID string,
	selectedIntentID *string,
) (WorkspaceActivityEvidence, error) {
	evidence := WorkspaceActivityEvidence{}
	rows, err := tx.QueryContext(ctx, activityRunQuery+`
WHERE r.project_id = ? AND r.task_type = 'reason' AND r.status = 'active'
ORDER BY r.started_at DESC, r.id DESC
LIMIT 2`, projectID)
	if err != nil {
		return WorkspaceActivityEvidence{}, err
	}
	activeReasons, err := scanActivityRuns(rows)
	if err != nil {
		return WorkspaceActivityEvidence{}, err
	}
	if len(activeReasons) > 1 {
		return WorkspaceActivityEvidence{}, fmt.Errorf("project %s has multiple active Reason runs", projectID)
	}
	if len(activeReasons) == 1 {
		evidence.ActiveReason = &activeReasons[0]
	}

	if selectedIntentID != nil && *selectedIntentID != "" {
		run, err := scanActivityRun(tx.QueryRowContext(ctx, activityRunQuery+`
WHERE r.project_id = ? AND r.target_intent_id = ?
ORDER BY r.started_at DESC, r.id DESC
LIMIT 1`, projectID, *selectedIntentID))
		if err != nil && !isNoRows(err) {
			return WorkspaceActivityEvidence{}, err
		}
		if err == nil {
			evidence.SelectedIntentRun = &run
		}
	}
	return evidence, nil
}

func scanActivityRuns(rows *sql.Rows) ([]ActivityRun, error) {
	defer rows.Close()
	runs := make([]ActivityRun, 0, 1)
	for rows.Next() {
		run, err := scanActivityRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

const projectExecutionStatusQuery = `
SELECT
    p.id,
    (SELECT COUNT(*) FROM execution_runs r WHERE r.project_id = p.id AND r.status = 'active' AND r.task_type = 'reason'),
    (SELECT r.id FROM execution_runs r WHERE r.project_id = p.id AND r.status = 'active' AND r.task_type = 'reason' ORDER BY r.started_at DESC, r.id DESC LIMIT 1),
    (SELECT r.worker_name FROM execution_runs r WHERE r.project_id = p.id AND r.status = 'active' AND r.task_type = 'reason' ORDER BY r.started_at DESC, r.id DESC LIMIT 1),
    (SELECT r.started_at FROM execution_runs r WHERE r.project_id = p.id AND r.status = 'active' AND r.task_type = 'reason' ORDER BY r.started_at DESC, r.id DESC LIMIT 1),
    (SELECT r.input_generation FROM execution_runs r WHERE r.project_id = p.id AND r.status = 'active' AND r.task_type = 'reason' ORDER BY r.started_at DESC, r.id DESC LIMIT 1),
    (SELECT r.input_revision FROM execution_runs r WHERE r.project_id = p.id AND r.status = 'active' AND r.task_type = 'reason' ORDER BY r.started_at DESC, r.id DESC LIMIT 1),
    (SELECT COUNT(*) FROM execution_runs r WHERE r.project_id = p.id AND r.status = 'active' AND r.task_type IN ('reason', 'explore', 'bootstrap')),
    (SELECT COUNT(*) FROM execution_work_keys wk WHERE wk.project_id = p.id AND wk.retry_state = 'exhausted'),
    (
        SELECT COUNT(*)
        FROM execution_runs r
        JOIN execution_work_keys wk ON wk.work_key = r.work_key
        WHERE r.project_id = p.id
          AND r.status IN ('failed', 'interrupted')
          AND wk.retry_state NOT IN ('backoff', 'exhausted')
          AND r.attempt = (SELECT MAX(latest.attempt) FROM execution_runs latest WHERE latest.work_key = r.work_key)
    ),
    (SELECT COUNT(*) FROM execution_work_keys wk WHERE wk.project_id = p.id AND wk.retry_state = 'backoff')
FROM projects p
ORDER BY p.id`
