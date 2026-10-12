package project

import (
	"context"
	"database/sql"
	"errors"
)

type CreateHintResult struct {
	Hint             Hint  `json:"hint"`
	ProjectRevision  int64 `json:"project_revision"`
	AffectedRunCount int   `json:"affected_run_count"`
}

type WithdrawHintResult struct {
	Hint
	ProjectRevision  int64 `json:"project_revision"`
	AffectedRunCount int   `json:"affected_run_count"`
}

func (s *Service) CreateHint(ctx context.Context, projectID string, req CreateHintRequest) (*CreateHintResult, error) {
	validated, appErr := ValidateCreateHintRequest(req)
	if appErr != nil {
		return nil, appErr
	}
	receipt, err := commitProjectRevision(
		ctx, s, existingProjectRevisionTarget(projectID, validated.ExpectedRevision),
		createHintRevisionCommand{Content: validated.Content},
	)
	if err != nil {
		return nil, err
	}
	return &CreateHintResult{
		Hint: receipt.Mutation.Hint, ProjectRevision: receipt.Project.Revision,
		AffectedRunCount: receipt.AffectedRunCount,
	}, nil
}

func (s *Service) WithdrawHint(ctx context.Context, projectID, hintID string, req WithdrawHintRequest) (*WithdrawHintResult, error) {
	reason := normalizeReason(req.Reason)
	if reason == "" {
		return nil, InvalidArgument("reason is required")
	}
	if req.ExpectedRevision < 0 {
		return nil, InvalidArgument("expected_revision must be a non-negative integer")
	}
	receipt, err := commitProjectRevision(
		ctx, s, existingProjectRevisionTarget(projectID, req.ExpectedRevision),
		withdrawHintRevisionCommand{HintID: hintID, Reason: reason},
	)
	if err != nil {
		return nil, err
	}
	return &WithdrawHintResult{
		Hint: receipt.Mutation.After, ProjectRevision: receipt.Project.Revision,
		AffectedRunCount: receipt.AffectedRunCount,
	}, nil
}

type createHintRevisionCommand struct {
	Content string
}

func (createHintRevisionCommand) validateRevisionBeforeExpected() bool { return false }

func (createHintRevisionCommand) validateRevisionProject(projectMeta ProjectMeta) error {
	if projectMeta.Status == ProjectStatusCompleted {
		return Conflict("completed projects accept feedback only through Reopen")
	}
	return nil
}

func (command createHintRevisionCommand) applyRevision(
	ctx context.Context, service *serviceCore, _ ExecutionPort, tx DBTX, revision projectRevisionContext,
) (hintCreatedRevision, error) {
	hintID, err := NextHintID(ctx, tx, revision.Project.ID)
	if err != nil {
		return hintCreatedRevision{}, err
	}
	if err := service.hints.Create(
		ctx, tx, hintID, revision.Project.ID, command.Content,
		service.operatorName, revision.OccurredAt, revision.Project.Generation,
	); err != nil {
		return hintCreatedRevision{}, err
	}
	hint, err := service.hints.GetByID(ctx, tx, revision.Project.ID, hintID)
	if err != nil {
		return hintCreatedRevision{}, err
	}
	mutation := hintCreatedRevision{Hint: *hint}
	return mutation, nil
}

type hintCreatedRevision struct {
	Hint Hint
}

func (hintCreatedRevision) revisionCancellation(projectMeta ProjectMeta) projectRevisionCancellation {
	return hintRevisionCancellation(projectMeta)
}

func (transition hintCreatedRevision) revisionEvidence(affectedRuns int) projectRevisionEvidence {
	hintID := transition.Hint.ID
	return projectRevisionEvidence{
		Kind: ChangeHintCreated, EntityType: "hint", EntityID: &hintID,
		After:  projectRevisionEntities{Hints: []Hint{transition.Hint}},
		Result: map[string]any{"affected_run_count": affectedRuns},
	}
}

type withdrawHintRevisionCommand struct {
	HintID string
	Reason string
}

func (withdrawHintRevisionCommand) validateRevisionBeforeExpected() bool { return false }

func (withdrawHintRevisionCommand) validateRevisionProject(ProjectMeta) error {
	return nil
}

func (command withdrawHintRevisionCommand) applyRevision(
	ctx context.Context, service *serviceCore, _ ExecutionPort, tx DBTX, revision projectRevisionContext,
) (hintWithdrawnRevision, error) {
	hint, err := service.hints.GetByID(ctx, tx, revision.Project.ID, command.HintID)
	if errors.Is(err, sql.ErrNoRows) {
		return hintWithdrawnRevision{}, NotFound("Hint not found")
	}
	if err != nil {
		return hintWithdrawnRevision{}, err
	}
	if !hint.IsActive() {
		return hintWithdrawnRevision{}, Conflict("hint is already withdrawn")
	}
	rows, err := service.hints.Withdraw(
		ctx, tx, revision.Project.ID, command.HintID,
		service.operatorName, command.Reason, revision.OccurredAt,
	)
	if err != nil {
		return hintWithdrawnRevision{}, err
	}
	if rows != 1 {
		return hintWithdrawnRevision{}, Conflict("hint changed while withdrawing")
	}
	after, err := service.hints.GetByID(ctx, tx, revision.Project.ID, command.HintID)
	if err != nil {
		return hintWithdrawnRevision{}, err
	}
	mutation := hintWithdrawnRevision{Before: *hint, After: *after, Reason: command.Reason}
	return mutation, nil
}

type hintWithdrawnRevision struct {
	Before Hint
	After  Hint
	Reason string
}

func (hintWithdrawnRevision) revisionCancellation(projectMeta ProjectMeta) projectRevisionCancellation {
	return hintRevisionCancellation(projectMeta)
}

func hintRevisionCancellation(projectMeta ProjectMeta) projectRevisionCancellation {
	return projectRevisionCancellation{
		Reason: CancellationBlackboardChanged, Selector: hintChangeSelector(projectMeta.Revision),
	}
}

func (transition hintWithdrawnRevision) revisionEvidence(affectedRuns int) projectRevisionEvidence {
	hintID := transition.After.ID
	return projectRevisionEvidence{
		Kind: ChangeHintWithdrawn, EntityType: "hint", EntityID: &hintID,
		Before: projectRevisionEntities{Hints: []Hint{transition.Before}},
		After:  projectRevisionEntities{Hints: []Hint{transition.After}},
		Result: map[string]any{
			"reason": transition.Reason, "affected_run_count": affectedRuns,
		},
	}
}

func hintChangeSelector(revision int64) RunCancellationSelector {
	return RunCancellationSelector{Clauses: []RunCancellationClause{{
		Tasks: []RunTask{RunTaskReason, RunTaskBootstrap}, InputRevisionAtMost: &revision,
	}}}
}

func normalizeReason(value string) string {
	for len(value) > 0 && (value[0] == ' ' || value[0] == '\t' || value[0] == '\n' || value[0] == '\r') {
		value = value[1:]
	}
	for len(value) > 0 {
		last := value[len(value)-1]
		if last != ' ' && last != '\t' && last != '\n' && last != '\r' {
			break
		}
		value = value[:len(value)-1]
	}
	return value
}

func (s *Service) ListHints(ctx context.Context, projectID string) ([]Hint, error) {
	conn, err := s.db.GetReadConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := s.requireProjectExists(ctx, conn, projectID); err != nil {
		return nil, err
	}
	return s.hints.ListByProject(ctx, conn, projectID)
}
