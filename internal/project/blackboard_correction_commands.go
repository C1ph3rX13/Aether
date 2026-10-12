package project

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type RetractFactResult struct {
	Fact
	ProjectRevision  int64 `json:"project_revision"`
	AffectedRunCount int   `json:"affected_run_count"`
}

type SupersedeFactResult struct {
	Original         Fact  `json:"original"`
	Replacement      Fact  `json:"replacement"`
	ProjectRevision  int64 `json:"project_revision"`
	AffectedRunCount int   `json:"affected_run_count"`
}

type CancelIntentResult struct {
	Intent
	ProjectRevision  int64 `json:"project_revision"`
	AffectedRunCount int   `json:"affected_run_count"`
}

func (s *Service) RetractFact(ctx context.Context, projectID, factID string, req RetractFactRequest) (*RetractFactResult, error) {
	reason, appErr := validateCorrection(req.Reason, req.ExpectedRevision)
	if appErr != nil {
		return nil, appErr
	}
	receipt, err := commitProjectRevision(
		ctx, s, existingProjectRevisionTarget(projectID, req.ExpectedRevision),
		retractFactRevisionCommand{FactID: factID, Reason: reason},
	)
	if err != nil {
		return nil, err
	}
	return &RetractFactResult{
		Fact: receipt.Mutation.After, ProjectRevision: receipt.Project.Revision,
		AffectedRunCount: receipt.AffectedRunCount,
	}, nil
}

func (s *Service) SupersedeFact(ctx context.Context, projectID, factID string, req SupersedeFactRequest) (*SupersedeFactResult, error) {
	description := strings.TrimSpace(req.Description)
	if description == "" {
		return nil, InvalidArgument("description is required")
	}
	reason, appErr := validateCorrection(req.Reason, req.ExpectedRevision)
	if appErr != nil {
		return nil, appErr
	}
	receipt, err := commitProjectRevision(
		ctx, s, existingProjectRevisionTarget(projectID, req.ExpectedRevision),
		supersedeFactRevisionCommand{FactID: factID, Description: description, Reason: reason},
	)
	if err != nil {
		return nil, err
	}
	return &SupersedeFactResult{
		Original: receipt.Mutation.Original, Replacement: receipt.Mutation.Replacement,
		ProjectRevision: receipt.Project.Revision, AffectedRunCount: receipt.AffectedRunCount,
	}, nil
}

func (s *Service) CancelIntent(ctx context.Context, projectID, intentID string, req CancelIntentRequest) (*CancelIntentResult, error) {
	reason, appErr := validateCorrection(req.Reason, req.ExpectedRevision)
	if appErr != nil {
		return nil, appErr
	}
	receipt, err := commitProjectRevision(
		ctx, s, existingProjectRevisionTarget(projectID, req.ExpectedRevision),
		cancelIntentRevisionCommand{IntentID: intentID, Reason: reason},
	)
	if err != nil {
		return nil, err
	}
	return &CancelIntentResult{
		Intent: receipt.Mutation.After, ProjectRevision: receipt.Project.Revision,
		AffectedRunCount: receipt.AffectedRunCount,
	}, nil
}

type retractFactRevisionCommand struct {
	FactID string
	Reason string
}

func (retractFactRevisionCommand) validateRevisionBeforeExpected() bool { return false }

func (retractFactRevisionCommand) validateRevisionProject(projectMeta ProjectMeta) error {
	if projectMeta.Status != ProjectStatusActive {
		return Forbidden("Project is " + string(projectMeta.Status))
	}
	return nil
}

func (command retractFactRevisionCommand) applyRevision(
	ctx context.Context, service *serviceCore, _ ExecutionPort, tx DBTX, revision projectRevisionContext,
) (factRetractedRevision, error) {
	fact, err := service.facts.GetByID(ctx, tx, revision.Project.ID, command.FactID)
	if errors.Is(err, sql.ErrNoRows) {
		return factRetractedRevision{}, NotFound("Fact not found")
	}
	if err != nil {
		return factRetractedRevision{}, err
	}
	if !fact.IsActive() {
		return factRetractedRevision{}, Conflict("fact is already retracted")
	}
	if fact.ID == "goal" {
		return factRetractedRevision{}, Forbidden("the goal fact cannot be retracted")
	}
	before := fact
	rows, err := service.facts.Retract(
		ctx, tx, revision.Project.ID, command.FactID, service.operatorName,
		command.Reason, revision.OccurredAt, nil,
	)
	if err != nil {
		return factRetractedRevision{}, err
	}
	if rows != 1 {
		return factRetractedRevision{}, Conflict("fact changed while retracting")
	}
	after, err := service.facts.GetByID(ctx, tx, revision.Project.ID, command.FactID)
	if err != nil {
		return factRetractedRevision{}, err
	}
	mutation := factRetractedRevision{Before: before, After: after, Reason: command.Reason}
	return mutation, nil
}

type factRetractedRevision struct {
	Before Fact
	After  Fact
	Reason string
}

func (transition factRetractedRevision) revisionCancellation(projectMeta ProjectMeta) projectRevisionCancellation {
	return projectRevisionCancellation{
		Reason:   CancellationFactRetracted,
		Selector: factCorrectionSelector(transition.Before.ID, projectMeta.Revision),
	}
}

func (transition factRetractedRevision) revisionEvidence(affectedRuns int) projectRevisionEvidence {
	factID := transition.After.ID
	return projectRevisionEvidence{
		Kind: ChangeFactRetracted, EntityType: "fact", EntityID: &factID,
		Before: projectRevisionEntities{Facts: []Fact{transition.Before}},
		After:  projectRevisionEntities{Facts: []Fact{transition.After}},
		Result: map[string]any{
			"reason": transition.Reason, "affected_run_count": affectedRuns,
		},
	}
}

type supersedeFactRevisionCommand struct {
	FactID      string
	Description string
	Reason      string
}

func (supersedeFactRevisionCommand) validateRevisionBeforeExpected() bool { return false }

func (supersedeFactRevisionCommand) validateRevisionProject(projectMeta ProjectMeta) error {
	if projectMeta.Status != ProjectStatusActive {
		return Forbidden("Project is " + string(projectMeta.Status))
	}
	return nil
}

func (command supersedeFactRevisionCommand) applyRevision(
	ctx context.Context, service *serviceCore, _ ExecutionPort, tx DBTX, revision projectRevisionContext,
) (factSupersededRevision, error) {
	original, err := service.facts.GetByID(ctx, tx, revision.Project.ID, command.FactID)
	if errors.Is(err, sql.ErrNoRows) {
		return factSupersededRevision{}, NotFound("Fact not found")
	}
	if err != nil {
		return factSupersededRevision{}, err
	}
	if !original.IsActive() {
		return factSupersededRevision{}, Conflict("fact is already retracted")
	}
	if original.ID == "goal" {
		return factSupersededRevision{}, Forbidden("the goal fact cannot be superseded")
	}
	replacementID, err := NextFactID(ctx, tx, revision.Project.ID)
	if err != nil {
		return factSupersededRevision{}, err
	}
	if err := service.facts.CreateSuperseding(
		ctx, tx, replacementID, revision.Project.ID, command.Description, command.FactID,
	); err != nil {
		return factSupersededRevision{}, err
	}
	rows, err := service.facts.Retract(
		ctx, tx, revision.Project.ID, command.FactID, service.operatorName,
		command.Reason, revision.OccurredAt, &replacementID,
	)
	if err != nil {
		return factSupersededRevision{}, err
	}
	if rows != 1 {
		return factSupersededRevision{}, Conflict("fact changed while superseding")
	}
	replacement, err := service.facts.GetByID(ctx, tx, revision.Project.ID, replacementID)
	if err != nil {
		return factSupersededRevision{}, err
	}
	updatedOriginal, err := service.facts.GetByID(ctx, tx, revision.Project.ID, command.FactID)
	if err != nil {
		return factSupersededRevision{}, err
	}
	mutation := factSupersededRevision{
		Original: updatedOriginal, Replacement: replacement,
		Before: original, Reason: command.Reason,
	}
	return mutation, nil
}

type factSupersededRevision struct {
	Before      Fact
	Original    Fact
	Replacement Fact
	Reason      string
}

func (transition factSupersededRevision) revisionCancellation(projectMeta ProjectMeta) projectRevisionCancellation {
	return projectRevisionCancellation{
		Reason:   CancellationFactRetracted,
		Selector: factCorrectionSelector(transition.Before.ID, projectMeta.Revision),
	}
}

func (transition factSupersededRevision) revisionEvidence(affectedRuns int) projectRevisionEvidence {
	factID := transition.Original.ID
	return projectRevisionEvidence{
		Kind: ChangeFactSuperseded, EntityType: "fact", EntityID: &factID,
		Before: projectRevisionEntities{Facts: []Fact{transition.Before}},
		After:  projectRevisionEntities{Facts: []Fact{transition.Original, transition.Replacement}},
		Result: map[string]any{
			"reason":              transition.Reason,
			"replacement_fact_id": transition.Replacement.ID,
			"affected_run_count":  affectedRuns,
		},
	}
}

type cancelIntentRevisionCommand struct {
	IntentID string
	Reason   string
}

func (cancelIntentRevisionCommand) validateRevisionBeforeExpected() bool { return false }

func (cancelIntentRevisionCommand) validateRevisionProject(projectMeta ProjectMeta) error {
	if projectMeta.Status != ProjectStatusActive {
		return Forbidden("Project is " + string(projectMeta.Status))
	}
	return nil
}

func (command cancelIntentRevisionCommand) applyRevision(
	ctx context.Context, service *serviceCore, _ ExecutionPort, tx DBTX, revision projectRevisionContext,
) (intentCancelledRevision, error) {
	intent, err := service.intents.GetByID(ctx, tx, revision.Project.ID, command.IntentID)
	if errors.Is(err, sql.ErrNoRows) {
		return intentCancelledRevision{}, NotFound("Intent not found")
	}
	if err != nil {
		return intentCancelledRevision{}, err
	}
	if !intent.IsOpen() {
		return intentCancelledRevision{}, Conflict("intent is already " + intentTerminalState(*intent))
	}
	before := *intent
	rows, err := service.intents.Cancel(
		ctx, tx, revision.Project.ID, command.IntentID, service.operatorName,
		command.Reason, revision.OccurredAt,
	)
	if err != nil {
		return intentCancelledRevision{}, err
	}
	if rows != 1 {
		return intentCancelledRevision{}, Conflict("intent changed while cancelling")
	}
	after, err := service.intents.GetByID(ctx, tx, revision.Project.ID, command.IntentID)
	if err != nil {
		return intentCancelledRevision{}, err
	}
	mutation := intentCancelledRevision{Before: before, After: *after, Reason: command.Reason}
	return mutation, nil
}

type intentCancelledRevision struct {
	Before Intent
	After  Intent
	Reason string
}

func (transition intentCancelledRevision) revisionCancellation(ProjectMeta) projectRevisionCancellation {
	intentID := transition.After.ID
	return projectRevisionCancellation{
		Reason: CancellationIntentCancelled,
		Selector: RunCancellationSelector{Clauses: []RunCancellationClause{{
			Tasks: []RunTask{RunTaskExplore, RunTaskBootstrap}, TargetIntentID: &intentID,
		}}},
	}
}

func (transition intentCancelledRevision) revisionEvidence(affectedRuns int) projectRevisionEvidence {
	intentID := transition.After.ID
	return projectRevisionEvidence{
		Kind: ChangeIntentCancelled, EntityType: "intent", EntityID: &intentID,
		Before: projectRevisionEntities{Intents: []Intent{transition.Before}},
		After:  projectRevisionEntities{Intents: []Intent{transition.After}},
		Result: map[string]any{
			"reason": transition.Reason, "affected_run_count": affectedRuns,
		},
	}
}

func factCorrectionSelector(factID string, revision int64) RunCancellationSelector {
	return RunCancellationSelector{Clauses: []RunCancellationClause{
		{Tasks: []RunTask{RunTaskExplore}, SourceFactID: &factID},
		{Tasks: []RunTask{RunTaskReason, RunTaskBootstrap}, InputRevisionAtMost: &revision},
	}}
}

func validateCorrection(reasonValue string, expectedRevision int64) (string, *DomainError) {
	reason := strings.TrimSpace(reasonValue)
	if reason == "" {
		return "", InvalidArgument("reason is required")
	}
	if expectedRevision < 0 {
		return "", InvalidArgument("expected_revision must be a non-negative integer")
	}
	return reason, nil
}

func intentTerminalState(intent Intent) string {
	if intent.CancelledAt != nil {
		return "cancelled"
	}
	return "concluded"
}
