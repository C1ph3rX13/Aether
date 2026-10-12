package run

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type ProjectCleanupJob struct {
	ID          string            `json:"id"`
	ProjectID   string            `json:"project_id"`
	ContainerID string            `json:"container_id,omitempty"`
	Attempts    int               `json:"attempts"`
	Handles     []ExecutionHandle `json:"handles"`
}

type CleanupLeaseRequest struct {
	DispatcherID           string       `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken `json:"dispatcher_fencing_token"`
	Limit                  int          `json:"limit"`
}

type CleanupCompletionRequest struct {
	JobID                  string       `json:"job_id"`
	DispatcherID           string       `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken `json:"dispatcher_fencing_token"`
	Error                  string       `json:"error,omitempty"`
}

func (s *Service) LeaseProjectCleanupJobs(ctx context.Context, request CleanupLeaseRequest) ([]ProjectCleanupJob, error) {
	if request.DispatcherID == "" || request.DispatcherFencingToken <= 0 {
		return nil, newError(ErrorInvalidArgument, "Dispatcher Lease identity is required")
	}
	if request.Limit == 0 {
		request.Limit = 10
	}
	if request.Limit < 1 || request.Limit > 100 {
		return nil, newError(ErrorInvalidArgument, "cleanup limit is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.clock()
	authorized, err := s.ledger.DispatcherLeaseMatches(ctx, tx, request.DispatcherID, request.DispatcherFencingToken, now.UnixNano())
	if err != nil {
		return nil, err
	}
	if !authorized {
		return nil, newError(ErrorFenced, "Dispatcher Lease does not authorize cleanup")
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM project_cleanup_jobs
		WHERE status = 'pending' OR (status = 'leased' AND lease_expires_at <= ?)
		ORDER BY created_at, id LIMIT ?`, now.Format(time.RFC3339Nano), request.Limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	jobs := make([]ProjectCleanupJob, 0, len(ids))
	expires := now.Add(30 * time.Second).Format(time.RFC3339Nano)
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE project_cleanup_jobs SET status='leased', attempts=attempts+1,
			lease_owner=?, lease_fencing_token=?, lease_expires_at=?, updated_at=?
			WHERE id=? AND (status='pending' OR (status='leased' AND lease_expires_at <= ?))`,
			request.DispatcherID, request.DispatcherFencingToken, expires, now.Format(time.RFC3339Nano), id, now.Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			continue
		}
		var job ProjectCleanupJob
		var containerID sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT id, project_id, container_id, attempts FROM project_cleanup_jobs WHERE id=?", id).Scan(&job.ID, &job.ProjectID, &containerID, &job.Attempts); err != nil {
			return nil, err
		}
		if containerID.Valid {
			job.ContainerID = containerID.String
		}
		handleRows, err := tx.QueryContext(ctx, `SELECT run_id, phase, container_id, exec_id FROM project_cleanup_handles WHERE job_id=? ORDER BY run_id, phase, exec_id`, id)
		if err != nil {
			return nil, err
		}
		for handleRows.Next() {
			var handle ExecutionHandle
			handle.ProjectID = job.ProjectID
			if err := handleRows.Scan(&handle.RunID, &handle.Phase, &handle.ContainerID, &handle.ExecID); err != nil {
				handleRows.Close()
				return nil, err
			}
			job.Handles = append(job.Handles, handle)
		}
		if err := handleRows.Close(); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Service) CompleteProjectCleanupJob(ctx context.Context, request CleanupCompletionRequest) error {
	request.JobID, request.Error = strings.TrimSpace(request.JobID), strings.TrimSpace(request.Error)
	if request.JobID == "" || request.DispatcherID == "" || request.DispatcherFencingToken <= 0 {
		return newError(ErrorInvalidArgument, "cleanup completion is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	authorized, err := s.ledger.DispatcherLeaseMatches(ctx, tx, request.DispatcherID, request.DispatcherFencingToken, s.clock().UnixNano())
	if err != nil {
		return err
	}
	if !authorized {
		return newError(ErrorFenced, "Dispatcher Lease does not authorize cleanup")
	}
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM project_cleanup_jobs WHERE id=?", request.JobID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// A successful completion removes the job and its handles. Treat a
			// repeated success acknowledgement as idempotent while preserving
			// NotFound for failed acknowledgements against unknown jobs.
			if request.Error == "" {
				return tx.Commit()
			}
			return newError(ErrorNotFound, "cleanup job not found")
		}
		return err
	}
	now := s.clock().Format(time.RFC3339Nano)
	if status == "completed" && request.Error == "" {
		if _, err := tx.ExecContext(ctx, "DELETE FROM project_cleanup_jobs WHERE id=?", request.JobID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if request.Error != "" {
		result, err := tx.ExecContext(ctx, `UPDATE project_cleanup_jobs SET status='pending', last_error=?,
			lease_owner=NULL, lease_fencing_token=NULL, lease_expires_at=NULL, updated_at=?
			WHERE id=? AND status='leased' AND lease_owner=? AND lease_fencing_token=?`,
			request.Error, now, request.JobID, request.DispatcherID, request.DispatcherFencingToken)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return newError(ErrorFenced, "cleanup job lease is no longer owned")
		}
		return tx.Commit()
	}
	if status != "leased" {
		return newError(ErrorFenced, "cleanup job lease is no longer owned")
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM project_cleanup_jobs WHERE id=? AND status='leased' AND lease_owner=? AND lease_fencing_token=?",
		request.JobID, request.DispatcherID, request.DispatcherFencingToken)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return newError(ErrorFenced, "cleanup job lease is no longer owned")
	}
	return tx.Commit()
}
