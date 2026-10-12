package project

import (
	"context"
	"database/sql"
)

const (
	projectStatusActive    = ProjectStatusActive
	projectStatusPaused    = ProjectStatusPaused
	projectStatusCompleted = ProjectStatusCompleted
)

// PauseProject transitions an active Project to paused, revokes active Run
// commit rights, and records the complete lifecycle revision atomically.
func (s *Service) PauseProject(ctx context.Context, projectID string, request ProjectLifecycleRequest) (ProjectMeta, error) {
	return s.transitionProjectLifecycle(
		ctx, projectID, request, projectStatusActive, projectStatusPaused,
		ChangeProjectPaused, CancellationProjectPaused,
	)
}

// ResumeProject transitions a paused Project to active and records the
// complete lifecycle revision atomically.
func (s *Service) ResumeProject(ctx context.Context, projectID string, request ProjectLifecycleRequest) (ProjectMeta, error) {
	return s.transitionProjectLifecycle(
		ctx, projectID, request, projectStatusPaused, projectStatusActive,
		ChangeProjectResumed, "",
	)
}

func (s *Service) transitionProjectLifecycle(
	ctx context.Context,
	projectID string,
	request ProjectLifecycleRequest,
	fromStatus, toStatus ProjectStatus,
	kind ChangeKind,
	cancellation CancellationReason,
) (ProjectMeta, error) {
	if request.ExpectedRevision < 0 {
		return ProjectMeta{}, InvalidArgument("expected_revision must be a non-negative integer")
	}
	receipt, err := commitProjectRevision(
		ctx, s, existingProjectRevisionTarget(projectID, request.ExpectedRevision),
		projectLifecycleRevisionCommand{
			FromStatus: fromStatus, ToStatus: toStatus, Kind: kind, Cancellation: cancellation,
		},
	)
	if err != nil {
		return ProjectMeta{}, err
	}
	return receipt.Project, nil
}

type projectLifecycleRevisionCommand struct {
	FromStatus   ProjectStatus
	ToStatus     ProjectStatus
	Kind         ChangeKind
	Cancellation CancellationReason
}

func (projectLifecycleRevisionCommand) validateRevisionBeforeExpected() bool { return true }

func (command projectLifecycleRevisionCommand) validateRevisionProject(projectMeta ProjectMeta) error {
	if projectMeta.Status != command.FromStatus {
		return lifecycleConflict(string(command.Kind), command.FromStatus, projectMeta.Status)
	}
	return nil
}

func (command projectLifecycleRevisionCommand) applyRevision(
	ctx context.Context, service *serviceCore, _ ExecutionPort, tx DBTX, revision projectRevisionContext,
) (projectLifecycleRevision, error) {
	rows, err := service.projects.TransitionStatus(
		ctx, tx, revision.Project.ID, command.FromStatus, command.ToStatus,
	)
	if err != nil {
		return projectLifecycleRevision{}, err
	}
	if rows != 1 {
		return projectLifecycleRevision{}, Conflict("project lifecycle changed")
	}
	mutation := projectLifecycleRevision{
		Kind: command.Kind, Cancellation: command.Cancellation,
	}
	return mutation, nil
}

type projectLifecycleRevision struct {
	Kind         ChangeKind
	Cancellation CancellationReason
}

func (transition projectLifecycleRevision) revisionCancellation(ProjectMeta) projectRevisionCancellation {
	return projectRevisionCancellation{
		Reason:   transition.Cancellation,
		Selector: RunCancellationSelector{Clauses: []RunCancellationClause{{}}},
	}
}

func (transition projectLifecycleRevision) revisionEvidence(_ int) projectRevisionEvidence {
	return projectRevisionEvidence{
		Kind: transition.Kind, EntityType: "project",
	}
}

// CompleteResult includes the compatibility Intent and its immutable record.
type CompleteResult struct {
	Project          ProjectMeta      `json:"project"`
	ProjectRevision  int64            `json:"project_revision"`
	Intent           Intent           `json:"intent"`
	Completion       CompletionRecord `json:"completion"`
	AffectedRunCount int              `json:"affected_run_count"`
}

// CompleteProject completes the current generation exactly once.
func (s *Service) CompleteProject(ctx context.Context, projectID string, req CompleteRequest) (*CompleteResult, error) {
	validated, appErr := ValidateCompleteRequest(req)
	if appErr != nil {
		return nil, appErr
	}
	receipt, err := commitProjectRevision(
		ctx, s, existingProjectRevisionTarget(projectID, validated.ExpectedRevision),
		completeProjectRevisionCommand{From: validated.From, Description: validated.Description},
	)
	if err != nil {
		return nil, err
	}
	return &CompleteResult{
		Project: receipt.Project, ProjectRevision: receipt.Project.Revision,
		Intent: receipt.Mutation.Intent, Completion: receipt.Mutation.Completion,
		AffectedRunCount: receipt.AffectedRunCount,
	}, nil
}

type completeProjectRevisionCommand struct {
	From        []string
	Description string
}

func (completeProjectRevisionCommand) validateRevisionBeforeExpected() bool { return false }

func (completeProjectRevisionCommand) validateRevisionProject(projectMeta ProjectMeta) error {
	if projectMeta.Status != projectStatusActive {
		return lifecycleConflict("complete", projectStatusActive, projectMeta.Status)
	}
	return nil
}

func (command completeProjectRevisionCommand) applyRevision(
	ctx context.Context, service *serviceCore, _ ExecutionPort, tx DBTX, revision projectRevisionContext,
) (projectCompletedRevision, error) {
	if err := service.validateFactsExist(ctx, tx, revision.Project.ID, command.From); err != nil {
		return projectCompletedRevision{}, InvalidArgument(err.Error())
	}
	if err := ValidateGoalNotInSources(command.From); err != nil {
		return projectCompletedRevision{}, InvalidArgument(err.Error())
	}
	intentID, err := NextIntentID(ctx, tx, revision.Project.ID)
	if err != nil {
		return projectCompletedRevision{}, err
	}
	goal := "goal"
	if err := service.intents.Create(
		ctx, tx, intentID, revision.Project.ID, IntentKindCompletion,
		command.Description, service.operatorName, command.From, &goal, revision.OccurredAt,
	); err != nil {
		return projectCompletedRevision{}, err
	}
	if err := service.completions.Create(
		ctx, tx, revision.Project.ID, revision.Project.Generation, intentID,
		command.Description, service.operatorName, revision.OccurredAt,
	); err != nil {
		return projectCompletedRevision{}, err
	}
	rows, err := service.projects.TransitionStatus(
		ctx, tx, revision.Project.ID, projectStatusActive, projectStatusCompleted,
	)
	if err != nil {
		return projectCompletedRevision{}, err
	}
	if rows != 1 {
		return projectCompletedRevision{}, Conflict("project lifecycle changed while completing")
	}
	intent, err := service.intents.GetByID(ctx, tx, revision.Project.ID, intentID)
	if err != nil {
		return projectCompletedRevision{}, err
	}
	completion, err := service.completions.GetGeneration(
		ctx, tx, revision.Project.ID, revision.Project.Generation,
	)
	if err != nil {
		return projectCompletedRevision{}, err
	}
	mutation := projectCompletedRevision{
		Intent: *intent, Completion: completion,
	}
	return mutation, nil
}

type projectCompletedRevision struct {
	Intent     Intent
	Completion CompletionRecord
}

func (projectCompletedRevision) revisionCancellation(ProjectMeta) projectRevisionCancellation {
	return projectRevisionCancellation{
		Reason:   CancellationOperatorCompleted,
		Selector: RunCancellationSelector{Clauses: []RunCancellationClause{{}}},
	}
}

func (transition projectCompletedRevision) revisionEvidence(affectedRuns int) projectRevisionEvidence {
	return projectRevisionEvidence{
		Kind: ChangeProjectCompleted, EntityType: "project",
		After: projectRevisionEntities{
			Intents:           []Intent{transition.Intent},
			CompletionRecords: []CompletionRecord{transition.Completion},
		},
		Result: map[string]any{
			"completion_intent_id": transition.Intent.ID,
			"affected_run_count":   affectedRuns,
		},
	}
}

// ReopenResult describes the new generation and preserved prior completion.
type ReopenResult struct {
	Project            ProjectMeta      `json:"project"`
	ProjectRevision    int64            `json:"project_revision"`
	Hint               Hint             `json:"hint"`
	PreviousCompletion CompletionRecord `json:"previous_completion"`
}

// ReopenProject starts a new generation and records external feedback as a Hint.
func (s *Service) ReopenProject(ctx context.Context, projectID string, req ReopenRequest) (*ReopenResult, error) {
	validated, appErr := ValidateReopenRequest(req)
	if appErr != nil {
		return nil, appErr
	}

	receipt, err := commitProjectRevision(
		ctx, s, existingProjectRevisionTarget(projectID, validated.ExpectedRevision),
		reopenProjectRevisionCommand{Feedback: validated.Feedback},
	)
	if err != nil {
		return nil, err
	}
	return &ReopenResult{
		Project: receipt.Project, ProjectRevision: receipt.Project.Revision,
		Hint: receipt.Mutation.Hint, PreviousCompletion: receipt.Mutation.PreviousCompletion,
	}, nil
}

type reopenProjectRevisionCommand struct {
	Feedback string
}

func (reopenProjectRevisionCommand) validateRevisionBeforeExpected() bool { return false }

func (reopenProjectRevisionCommand) validateRevisionProject(projectMeta ProjectMeta) error {
	if projectMeta.Status != projectStatusCompleted {
		return lifecycleConflict("reopen", projectStatusCompleted, projectMeta.Status)
	}
	return nil
}

func (command reopenProjectRevisionCommand) applyRevision(
	ctx context.Context, service *serviceCore, _ ExecutionPort, tx DBTX, revision projectRevisionContext,
) (projectReopenedRevision, error) {
	previous, err := service.completions.GetGeneration(
		ctx, tx, revision.Project.ID, revision.Project.Generation,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return projectReopenedRevision{}, Conflict("completed project is missing its completion record")
		}
		return projectReopenedRevision{}, err
	}
	rows, err := service.projects.ReopenGeneration(
		ctx, tx, revision.Project.ID, revision.Project.Generation,
	)
	if err != nil {
		return projectReopenedRevision{}, err
	}
	if rows != 1 {
		return projectReopenedRevision{}, Conflict("project lifecycle changed while reopening")
	}
	hintID, err := NextHintID(ctx, tx, revision.Project.ID)
	if err != nil {
		return projectReopenedRevision{}, err
	}
	if err := service.hints.Create(
		ctx, tx, hintID, revision.Project.ID, command.Feedback, service.operatorName,
		revision.OccurredAt, revision.Project.Generation+1,
	); err != nil {
		return projectReopenedRevision{}, err
	}
	hint, err := service.hints.GetByID(ctx, tx, revision.Project.ID, hintID)
	if err != nil {
		return projectReopenedRevision{}, err
	}
	mutation := projectReopenedRevision{
		Hint: *hint, PreviousCompletion: previous,
	}
	return mutation, nil
}

type projectReopenedRevision struct {
	Hint               Hint
	PreviousCompletion CompletionRecord
}

func (projectReopenedRevision) revisionCancellation(ProjectMeta) projectRevisionCancellation {
	return projectRevisionCancellation{}
}

func (transition projectReopenedRevision) revisionEvidence(_ int) projectRevisionEvidence {
	return projectRevisionEvidence{
		Kind: ChangeProjectReopened, EntityType: "project",
		After: projectRevisionEntities{Hints: []Hint{transition.Hint}},
		Result: map[string]any{
			"hint_id":             transition.Hint.ID,
			"previous_generation": transition.PreviousCompletion.Generation,
		},
	}
}

func lifecycleConflict(command string, expected, actual ProjectStatus) *DomainError {
	return Conflict("cannot " + command + " project: expected status " + string(expected) + ", got " + string(actual))
}
