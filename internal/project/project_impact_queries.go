package project

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func (s *Service) PreviewImpact(ctx context.Context, projectID string, req ImpactRequest) (ImpactPreview, error) {
	if req.ExpectedRevision < 0 {
		return ImpactPreview{}, InvalidArgument("expected_revision must be a non-negative integer")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImpactPreview{}, err
	}
	defer tx.Rollback()
	meta, err := s.projects.GetMeta(ctx, tx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return ImpactPreview{}, ErrProjectNotFound
	}
	if err != nil {
		return ImpactPreview{}, err
	}
	if meta.Revision != req.ExpectedRevision {
		return ImpactPreview{}, RevisionConflict("project revision changed", meta.Revision)
	}
	selector, appErr := s.impactSelector(ctx, tx, projectID, meta, req)
	if appErr != nil {
		return ImpactPreview{}, appErr
	}
	count, err := s.execution.PreviewRunCancellation(ctx, tx, projectID, selector)
	if err != nil {
		return ImpactPreview{}, err
	}
	return ImpactPreview{ProjectRevision: meta.Revision, AffectedRunCount: count}, nil
}

func (s *Service) impactSelector(
	ctx context.Context, tx DBTX, projectID string, meta ProjectMeta, req ImpactRequest,
) (RunCancellationSelector, *DomainError) {
	entityID := strings.TrimSpace(req.EntityID)
	switch req.Action {
	case ImpactHintChange:
		return hintChangeSelector(meta.Revision), nil
	case ImpactRetractFact, ImpactSupersedeFact:
		if meta.Status != ProjectStatusActive {
			return RunCancellationSelector{}, Forbidden("Project is " + string(meta.Status))
		}
		if entityID == "" {
			return RunCancellationSelector{}, InvalidArgument("entity_id is required")
		}
		fact, err := s.facts.GetByID(ctx, tx, projectID, entityID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return RunCancellationSelector{}, NotFound("Fact not found")
			}
			return RunCancellationSelector{}, Internal(err.Error())
		}
		if !fact.IsActive() || fact.ID == "goal" {
			return RunCancellationSelector{}, Conflict("fact is not correctable")
		}
		return factCorrectionSelector(entityID, meta.Revision), nil
	case ImpactCancelIntent:
		if meta.Status != ProjectStatusActive {
			return RunCancellationSelector{}, Forbidden("Project is " + string(meta.Status))
		}
		intent, err := s.intents.GetByID(ctx, tx, projectID, entityID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return RunCancellationSelector{}, NotFound("Intent not found")
			}
			return RunCancellationSelector{}, Internal(err.Error())
		}
		if !intent.IsOpen() {
			return RunCancellationSelector{}, Conflict("intent is not open")
		}
		return RunCancellationSelector{Clauses: []RunCancellationClause{{
			Tasks: []RunTask{RunTaskExplore, RunTaskBootstrap}, TargetIntentID: &entityID,
		}}}, nil
	case ImpactCompleteProject:
		if meta.Status != ProjectStatusActive {
			return RunCancellationSelector{}, Conflict("project is not active")
		}
		return RunCancellationSelector{Clauses: []RunCancellationClause{{}}}, nil
	default:
		return RunCancellationSelector{}, InvalidArgument("impact action is invalid")
	}
}
