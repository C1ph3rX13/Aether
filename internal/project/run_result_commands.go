package project

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// RunTask identifies the black-box task whose structured result is applied to
// the Blackboard. It intentionally does not depend on the Run aggregate.
type RunTask string

const (
	RunTaskReason    RunTask = "reason"
	RunTaskExplore   RunTask = "explore"
	RunTaskBootstrap RunTask = "bootstrap"
)

type RunOutcomeKind string

const (
	RunOutcomeCompleteProject RunOutcomeKind = "complete_project"
	RunOutcomeProposeIntents  RunOutcomeKind = "propose_intents"
	RunOutcomeNoChange        RunOutcomeKind = "no_change"
	RunOutcomeConcludeIntent  RunOutcomeKind = "conclude_intent"
	RunOutcomeDecline         RunOutcomeKind = "decline"
)

type ProposedRunIntent struct {
	From        []string
	Description string
}

type RunOutcome struct {
	Kind            RunOutcomeKind
	Description     string
	From            []string
	FactDescription string
	Intents         []ProposedRunIntent
}

// ApplyRunResultCommand contains only the immutable Run identity and validated
// structured outcome needed to update a Project.
type ApplyRunResultCommand struct {
	RunID         string
	ProjectID     string
	TargetIntent  *string
	Task          RunTask
	Generation    int
	InputRevision int64
	Worker        string
	Outcome       RunOutcome
	AppliedAt     string
}

type ApplyRunResult struct {
	ProjectRevision    int64
	Stale              bool
	Change             *BlackboardChange
	CreatedFactIDs     []string
	CreatedIntentIDs   []string
	CompletionIntentID string
}

type applyRunResultRevisionCommand struct {
	Command ApplyRunResultCommand
}

func (applyRunResultRevisionCommand) validateRevisionBeforeExpected() bool { return false }

func (applyRunResultRevisionCommand) validateRevisionProject(ProjectMeta) error { return nil }

func (command applyRunResultRevisionCommand) applyRevision(
	ctx context.Context, service *serviceCore, _ ExecutionPort, tx DBTX, revision projectRevisionContext,
) (runResultRevision, error) {
	input := command.Command
	meta := revision.Project

	var (
		mutation runResultRevision
		err      error
	)
	switch input.Outcome.Kind {
	case RunOutcomeProposeIntents:
		mutation, err = service.applyProposedRunIntents(ctx, tx, input)
	case RunOutcomeCompleteProject:
		if input.Task == RunTaskReason {
			mutation, err = service.applyReasonRunCompletion(ctx, tx, input, meta)
		} else {
			mutation, err = service.applyBootstrapRunCompletion(ctx, tx, input, meta)
		}
	case RunOutcomeConcludeIntent:
		mutation, err = service.applyRunIntentConclusion(ctx, tx, input)
	default:
		err = InvalidArgument(fmt.Sprintf("unsupported Run outcome %s", input.Outcome.Kind))
	}
	if err != nil {
		return runResultRevision{}, err
	}
	return mutation, nil
}

type runResultRevision struct {
	ChangeKind         ChangeKind
	EntityType         string
	EntityID           *string
	Before             projectRevisionEntities
	After              projectRevisionEntities
	Result             map[string]any
	CreatedFactIDs     []string
	CreatedIntentIDs   []string
	CompletionIntentID string
}

func (runResultRevision) revisionCancellation(ProjectMeta) projectRevisionCancellation {
	return projectRevisionCancellation{}
}

func (mutation runResultRevision) revisionEvidence(_ int) projectRevisionEvidence {
	return projectRevisionEvidence{
		Kind: mutation.ChangeKind, EntityType: mutation.EntityType, EntityID: mutation.EntityID,
		Before: mutation.Before, After: mutation.After, Result: mutation.Result,
	}
}

// ApplyRunResult owns all Blackboard decisions and mutations caused by one
// validated Agent result. The caller owns the surrounding cross-aggregate
// transaction and publishes Change only after that transaction commits.
func (s *serviceCore) ApplyRunResult(
	ctx context.Context,
	tx DBTX,
	command ApplyRunResultCommand,
) (ApplyRunResult, error) {
	command = normalizeApplyRunResultCommand(command)
	if err := validateApplyRunResultCommand(command); err != nil {
		return ApplyRunResult{}, err
	}
	meta, stale, err := s.runResultInputState(ctx, tx, command)
	if err != nil {
		return ApplyRunResult{}, err
	}
	result := ApplyRunResult{ProjectRevision: meta.Revision, Stale: stale}
	if stale || command.Outcome.Kind == RunOutcomeDecline || command.Outcome.Kind == RunOutcomeNoChange {
		return result, nil
	}
	receipt, err := applyProjectRevisionInTransaction(
		ctx, s, nil, tx, projectRevisionTarget{ProjectID: command.ProjectID},
		applyRunResultRevisionCommand{Command: command},
		projectRevisionCommitContext{
			OccurredAt: command.AppliedAt,
			Source:     ChangeSource{Kind: "run", Name: command.Worker, RunID: &command.RunID},
		},
	)
	if err != nil {
		return ApplyRunResult{}, err
	}
	mutation := receipt.Mutation
	result.ProjectRevision = receipt.Project.Revision
	result.Change = nil
	if receipt.Change.ProjectID != "" {
		result.Change = &receipt.Change
	}
	result.CreatedFactIDs = mutation.CreatedFactIDs
	result.CreatedIntentIDs = mutation.CreatedIntentIDs
	result.CompletionIntentID = mutation.CompletionIntentID
	return result, nil
}

func normalizeApplyRunResultCommand(command ApplyRunResultCommand) ApplyRunResultCommand {
	command.Worker = strings.TrimSpace(command.Worker)
	command.Outcome.Description = strings.TrimSpace(command.Outcome.Description)
	command.Outcome.FactDescription = strings.TrimSpace(command.Outcome.FactDescription)
	command.Outcome.From = append([]string(nil), command.Outcome.From...)
	command.Outcome.Intents = append([]ProposedRunIntent(nil), command.Outcome.Intents...)
	for index := range command.Outcome.Intents {
		command.Outcome.Intents[index].Description = strings.TrimSpace(command.Outcome.Intents[index].Description)
		command.Outcome.Intents[index].From = append([]string(nil), command.Outcome.Intents[index].From...)
	}
	return command
}

func validateApplyRunResultCommand(command ApplyRunResultCommand) *DomainError {
	if command.RunID == "" || command.ProjectID == "" || command.Generation <= 0 || command.InputRevision < 0 || command.AppliedAt == "" {
		return InvalidArgument("Run result Project identity is invalid")
	}
	switch command.Task {
	case RunTaskReason, RunTaskExplore, RunTaskBootstrap:
	default:
		return InvalidArgument("Run result task is invalid")
	}
	switch command.Outcome.Kind {
	case RunOutcomeCompleteProject, RunOutcomeProposeIntents, RunOutcomeNoChange,
		RunOutcomeConcludeIntent, RunOutcomeDecline:
	default:
		return InvalidArgument("Run result outcome is invalid")
	}
	if command.Outcome.Kind != RunOutcomeDecline && command.Outcome.Kind != RunOutcomeNoChange && command.Worker == "" {
		return InvalidArgument("Run result worker is required")
	}
	if command.Task == RunTaskReason && command.TargetIntent != nil {
		return InvalidArgument("Reason Run result cannot target an Intent")
	}
	if command.Task != RunTaskReason && (command.TargetIntent == nil || *command.TargetIntent == "") {
		return InvalidArgument("Explore and Bootstrap Run results require a target Intent")
	}
	switch command.Task {
	case RunTaskReason:
		if command.Outcome.Kind != RunOutcomeCompleteProject && command.Outcome.Kind != RunOutcomeProposeIntents &&
			command.Outcome.Kind != RunOutcomeNoChange && command.Outcome.Kind != RunOutcomeDecline {
			return InvalidArgument("Reason Run outcome is invalid")
		}
	case RunTaskExplore:
		if command.Outcome.Kind != RunOutcomeConcludeIntent && command.Outcome.Kind != RunOutcomeDecline {
			return InvalidArgument("Explore Run outcome is invalid")
		}
	case RunTaskBootstrap:
		if command.Outcome.Kind != RunOutcomeCompleteProject && command.Outcome.Kind != RunOutcomeConcludeIntent &&
			command.Outcome.Kind != RunOutcomeDecline {
			return InvalidArgument("Bootstrap Run outcome is invalid")
		}
	}
	switch command.Outcome.Kind {
	case RunOutcomeProposeIntents:
		if len(command.Outcome.Intents) == 0 {
			return InvalidArgument("proposed Intents are required")
		}
		for _, intent := range command.Outcome.Intents {
			if intent.Description == "" || len(intent.From) == 0 {
				return InvalidArgument("proposed Intent description and sources are required")
			}
			if err := ValidateGoalNotInSources(intent.From); err != nil {
				return InvalidArgument(err.Error())
			}
		}
	case RunOutcomeCompleteProject:
		if command.Outcome.Description == "" {
			return InvalidArgument("completion description is required")
		}
		if command.Task == RunTaskReason {
			if len(command.Outcome.From) == 0 {
				return InvalidArgument("completion sources are required")
			}
			if err := ValidateGoalNotInSources(command.Outcome.From); err != nil {
				return InvalidArgument(err.Error())
			}
		} else if command.Outcome.FactDescription == "" {
			return InvalidArgument("Bootstrap completion Fact is required")
		}
	case RunOutcomeConcludeIntent:
		if command.Outcome.FactDescription == "" {
			return InvalidArgument("conclusion Fact is required")
		}
	}
	return nil
}

func (s *serviceCore) runResultInputState(
	ctx context.Context,
	tx DBTX,
	command ApplyRunResultCommand,
) (ProjectMeta, bool, error) {
	meta, err := s.projects.GetMeta(ctx, tx, command.ProjectID)
	if err != nil {
		return ProjectMeta{}, false, err
	}
	if command.Outcome.Kind == RunOutcomeDecline {
		return meta, false, nil
	}
	if meta.Generation != command.Generation || meta.Status != ProjectStatusActive {
		return meta, true, nil
	}
	if (command.Task == RunTaskReason || command.Task == RunTaskBootstrap) && meta.Revision != command.InputRevision {
		return meta, true, nil
	}
	if command.Task == RunTaskReason {
		return meta, false, nil
	}

	target, err := s.intents.GetByID(ctx, tx, command.ProjectID, *command.TargetIntent)
	if err == sql.ErrNoRows {
		return meta, true, nil
	}
	if err != nil {
		return ProjectMeta{}, false, err
	}
	wantKind := IntentKindExplore
	if command.Task == RunTaskBootstrap {
		wantKind = IntentKindBootstrap
	}
	if target.Kind != wantKind {
		return meta, true, nil
	}
	if !target.IsOpen() {
		return meta, true, nil
	}
	if command.Task == RunTaskBootstrap {
		eligible, err := s.BootstrapTargetEligible(ctx, tx, command.ProjectID, target.ID)
		if err != nil {
			return ProjectMeta{}, false, err
		}
		if !eligible {
			return meta, true, nil
		}
	}
	for _, source := range target.From {
		usable, err := s.facts.IsUsableSource(ctx, tx, command.ProjectID, source)
		if err != nil {
			return ProjectMeta{}, false, err
		}
		if !usable {
			return meta, true, nil
		}
	}
	return meta, false, nil
}

// BootstrapTargetEligible reports whether an Intent is still the initial
// Bootstrap target. Run admission and result application share this invariant.
func (s *serviceCore) BootstrapTargetEligible(ctx context.Context, tx DBTX, projectID, intentID string) (bool, error) {
	var eligible int
	err := tx.QueryRowContext(ctx, `
		SELECT CASE WHEN
			p.status = 'active'
			AND p.bootstrap_enabled = 1
			AND p.generation = 1
			AND i.kind = ?
			AND i.description = ?
			AND i.creator = ?
			AND i.to_fact_id IS NULL
			AND i.cancelled_at IS NULL
			AND (SELECT COUNT(*) FROM intents all_i WHERE all_i.project_id = p.id) = 1
			AND (SELECT COUNT(*) FROM facts active_f WHERE active_f.project_id = p.id AND active_f.retracted_at IS NULL) = 2
			AND EXISTS (SELECT 1 FROM facts origin_f WHERE origin_f.project_id = p.id AND origin_f.id = 'origin' AND origin_f.retracted_at IS NULL)
			AND EXISTS (SELECT 1 FROM facts goal_f WHERE goal_f.project_id = p.id AND goal_f.id = 'goal' AND goal_f.retracted_at IS NULL)
			AND (SELECT COUNT(*) FROM intent_sources sources WHERE sources.project_id = p.id AND sources.intent_id = i.id) = 1
			AND EXISTS (SELECT 1 FROM intent_sources source WHERE source.project_id = p.id AND source.intent_id = i.id AND source.fact_id = 'origin')
		THEN 1 ELSE 0 END
		FROM projects p
		JOIN intents i ON i.project_id = p.id
		WHERE p.id = ? AND i.id = ?`,
		IntentKindBootstrap, BootstrapIntentDescription, BootstrapIntentCreator, projectID, intentID,
	).Scan(&eligible)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return eligible == 1, err
}

func (s *serviceCore) applyProposedRunIntents(
	ctx context.Context,
	tx DBTX,
	command ApplyRunResultCommand,
) (runResultRevision, error) {
	for _, proposed := range command.Outcome.Intents {
		if err := s.validateRunResultSources(ctx, tx, command.ProjectID, proposed.From); err != nil {
			return runResultRevision{}, err
		}
	}
	intentIDs := make([]string, 0, len(command.Outcome.Intents))
	for _, proposed := range command.Outcome.Intents {
		intentID, err := NextIntentID(ctx, tx, command.ProjectID)
		if err != nil {
			return runResultRevision{}, err
		}
		if err := s.intents.Create(
			ctx, tx, intentID, command.ProjectID, IntentKindExplore, proposed.Description,
			command.Worker, proposed.From, nil, command.AppliedAt,
		); err != nil {
			return runResultRevision{}, err
		}
		intentIDs = append(intentIDs, intentID)
	}
	after := projectRevisionEntities{Intents: make([]Intent, 0, len(intentIDs))}
	for _, intentID := range intentIDs {
		intent, err := s.intents.GetByID(ctx, tx, command.ProjectID, intentID)
		if err != nil {
			return runResultRevision{}, err
		}
		after.Intents = append(after.Intents, *intent)
	}
	return runResultRevision{
		ChangeKind: ChangeIntentsProposed, EntityType: "intent_batch",
		After:  after,
		Result: map[string]any{"intent_ids": intentIDs}, CreatedIntentIDs: intentIDs,
	}, nil
}

func (s *serviceCore) applyReasonRunCompletion(
	ctx context.Context,
	tx DBTX,
	command ApplyRunResultCommand,
	meta ProjectMeta,
) (runResultRevision, error) {
	if err := s.validateRunResultSources(ctx, tx, command.ProjectID, command.Outcome.From); err != nil {
		return runResultRevision{}, err
	}
	completionIntentID, err := s.createRunCompletionIntent(ctx, tx, command, meta, command.Outcome.From)
	if err != nil {
		return runResultRevision{}, err
	}
	intent, err := s.intents.GetByID(ctx, tx, command.ProjectID, completionIntentID)
	if err != nil {
		return runResultRevision{}, err
	}
	completion, err := s.completions.GetGeneration(ctx, tx, command.ProjectID, meta.Generation)
	if err != nil {
		return runResultRevision{}, err
	}
	after := projectRevisionEntities{Intents: []Intent{*intent}, CompletionRecords: []CompletionRecord{completion}}
	return runResultRevision{
		ChangeKind: ChangeProjectCompleted, EntityType: "project", After: after,
		Result:           map[string]any{"completion_intent_id": completionIntentID},
		CreatedIntentIDs: []string{completionIntentID}, CompletionIntentID: completionIntentID,
	}, nil
}

func (s *serviceCore) applyRunIntentConclusion(
	ctx context.Context,
	tx DBTX,
	command ApplyRunResultCommand,
) (runResultRevision, error) {
	if command.TargetIntent == nil || command.Outcome.FactDescription == "" {
		return runResultRevision{}, InvalidArgument("Intent conclusion is missing its target or Fact")
	}
	target, err := s.intents.GetByID(ctx, tx, command.ProjectID, *command.TargetIntent)
	if err != nil {
		return runResultRevision{}, err
	}
	updatedIntent, fact, err := s.concludeRunIntent(ctx, tx, command, *target, "target Intent")
	if err != nil {
		return runResultRevision{}, err
	}
	historyBefore := projectRevisionEntities{Intents: []Intent{*target}}
	historyAfter := projectRevisionEntities{Intents: []Intent{updatedIntent}, Facts: []Fact{fact}}
	return runResultRevision{
		ChangeKind: ChangeIntentConcluded, EntityType: "intent", EntityID: &target.ID,
		Before: historyBefore, After: historyAfter, Result: map[string]any{"fact_id": fact.ID},
		CreatedFactIDs: []string{fact.ID},
	}, nil
}

func (s *serviceCore) applyBootstrapRunCompletion(
	ctx context.Context,
	tx DBTX,
	command ApplyRunResultCommand,
	meta ProjectMeta,
) (runResultRevision, error) {
	if command.TargetIntent == nil || command.Outcome.FactDescription == "" {
		return runResultRevision{}, InvalidArgument("Bootstrap completion is missing its target or Fact")
	}
	target, err := s.intents.GetByID(ctx, tx, command.ProjectID, *command.TargetIntent)
	if err != nil {
		return runResultRevision{}, err
	}
	updatedIntent, fact, err := s.concludeRunIntent(ctx, tx, command, *target, "Bootstrap Intent")
	if err != nil {
		return runResultRevision{}, err
	}
	completionIntentID, err := s.createRunCompletionIntent(ctx, tx, command, meta, []string{fact.ID})
	if err != nil {
		return runResultRevision{}, err
	}
	completionIntent, err := s.intents.GetByID(ctx, tx, command.ProjectID, completionIntentID)
	if err != nil {
		return runResultRevision{}, err
	}
	completion, err := s.completions.GetGeneration(ctx, tx, command.ProjectID, meta.Generation)
	if err != nil {
		return runResultRevision{}, err
	}
	historyBefore := projectRevisionEntities{Intents: []Intent{*target}}
	historyAfter := projectRevisionEntities{Intents: []Intent{updatedIntent, *completionIntent}, Facts: []Fact{fact}, CompletionRecords: []CompletionRecord{completion}}
	return runResultRevision{
		ChangeKind: ChangeBootstrapCompleted, EntityType: "project",
		Before: historyBefore, After: historyAfter, Result: map[string]any{
			"fact_id": fact.ID, "target_intent_id": target.ID,
			"completion_intent_id": completionIntentID,
		},
		CreatedFactIDs: []string{fact.ID}, CreatedIntentIDs: []string{completionIntentID},
		CompletionIntentID: completionIntentID,
	}, nil
}

func (s *serviceCore) concludeRunIntent(
	ctx context.Context,
	tx DBTX,
	command ApplyRunResultCommand,
	target Intent,
	conflictSubject string,
) (Intent, Fact, error) {
	factID, err := NextFactID(ctx, tx, command.ProjectID)
	if err != nil {
		return Intent{}, Fact{}, err
	}
	if err := s.facts.Create(ctx, tx, factID, command.ProjectID, command.Outcome.FactDescription); err != nil {
		return Intent{}, Fact{}, err
	}
	rows, err := s.intents.ConcludeFromRun(ctx, tx, command.ProjectID, target.ID, factID, command.AppliedAt)
	if err != nil {
		return Intent{}, Fact{}, err
	}
	if rows != 1 {
		return Intent{}, Fact{}, Conflict(conflictSubject + " changed while applying Run result")
	}
	updatedIntent, err := s.intents.GetByID(ctx, tx, command.ProjectID, target.ID)
	if err != nil {
		return Intent{}, Fact{}, err
	}
	fact, err := s.facts.GetByID(ctx, tx, command.ProjectID, factID)
	if err != nil {
		return Intent{}, Fact{}, err
	}
	return *updatedIntent, fact, nil
}

func (s *serviceCore) createRunCompletionIntent(
	ctx context.Context,
	tx DBTX,
	command ApplyRunResultCommand,
	meta ProjectMeta,
	sources []string,
) (string, error) {
	intentID, err := NextIntentID(ctx, tx, command.ProjectID)
	if err != nil {
		return "", err
	}
	goal := "goal"
	if err := s.intents.Create(ctx, tx, intentID, command.ProjectID, IntentKindCompletion, command.Outcome.Description, command.Worker,
		sources, &goal, command.AppliedAt); err != nil {
		return "", err
	}
	if err := s.completions.Create(ctx, tx, command.ProjectID, meta.Generation, intentID,
		command.Outcome.Description, command.Worker, command.AppliedAt); err != nil {
		return "", err
	}
	rows, err := s.projects.TransitionStatus(ctx, tx, command.ProjectID, ProjectStatusActive, ProjectStatusCompleted)
	if err != nil {
		return "", err
	}
	if rows != 1 {
		return "", Conflict("Project changed while applying completion")
	}
	return intentID, nil
}

func (s *serviceCore) validateRunResultSources(ctx context.Context, tx DBTX, projectID string, sources []string) error {
	for _, source := range sources {
		usable, err := s.facts.IsUsableSource(ctx, tx, projectID, source)
		if err != nil {
			return err
		}
		if !usable {
			return InvalidArgument("Run result references an unavailable source Fact")
		}
	}
	return nil
}
