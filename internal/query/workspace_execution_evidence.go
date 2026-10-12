package query

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type WorkspaceExecutionRun struct {
	RunID                string                         `json:"run_id"`
	Task                 rundomain.TaskType             `json:"task"`
	Status               rundomain.Status               `json:"status"`
	Attempt              int64                          `json:"attempt"`
	Worker               string                         `json:"worker"`
	StartedAt            string                         `json:"started_at"`
	OutcomeKind          rundomain.OutcomeKind          `json:"outcome_kind,omitempty"`
	ErrorCode            rundomain.ErrorCode            `json:"error_code,omitempty"`
	CaptureFailureReason rundomain.CaptureFailureReason `json:"capture_failure_reason,omitempty"`
	CaptureDiagnostic    rundomain.CaptureDiagnostic    `json:"capture_diagnostic,omitempty"`
	ResultEvidenceSource rundomain.ResultEvidenceSource `json:"result_evidence_source,omitempty"`
}

type WorkspaceExecutionEvidence struct {
	ProjectID         string                 `json:"project_id"`
	ActiveReason      *WorkspaceExecutionRun `json:"active_reason,omitempty"`
	SelectedIntentRun *WorkspaceExecutionRun `json:"selected_intent_run,omitempty"`
}

func (s *Service) GetWorkspaceExecutionEvidence(
	ctx context.Context,
	projectID string,
	selectedIntentID *string,
) (WorkspaceExecutionEvidence, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WorkspaceExecutionEvidence{}, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id = ?", projectID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WorkspaceExecutionEvidence{}, project.NotFound("Project not found")
		}
		return WorkspaceExecutionEvidence{}, err
	}
	if s.activityReader == nil {
		return WorkspaceExecutionEvidence{}, project.Internal("Project Activity reader is not configured")
	}
	activity, err := s.activityReader.ReadWorkspaceActivityEvidence(ctx, tx, projectID, selectedIntentID)
	if err != nil {
		return WorkspaceExecutionEvidence{}, project.Internal(err.Error())
	}

	evidence := WorkspaceExecutionEvidence{ProjectID: projectID}
	if activity.ActiveReason != nil {
		evidence.ActiveReason = mapWorkspaceExecutionRun(activity.ActiveReason)
	}
	if activity.SelectedIntentRun != nil {
		evidence.SelectedIntentRun = mapWorkspaceExecutionRun(activity.SelectedIntentRun)
	}
	if err := tx.Commit(); err != nil {
		return WorkspaceExecutionEvidence{}, err
	}
	return evidence, nil
}

func mapWorkspaceExecutionRun(run *rundomain.ActivityRun) *WorkspaceExecutionRun {
	if run == nil {
		return nil
	}
	return &WorkspaceExecutionRun{
		RunID: string(run.ID), Task: run.Task, Status: run.Status, Attempt: run.Attempt,
		Worker: run.Worker, StartedAt: run.StartedAt.Format(time.RFC3339Nano),
		OutcomeKind: run.OutcomeKind, ErrorCode: run.ErrorCode,
		CaptureFailureReason: run.CaptureFailureReason, CaptureDiagnostic: run.CaptureDiagnostic,
		ResultEvidenceSource: run.ResultEvidenceSource,
	}
}
