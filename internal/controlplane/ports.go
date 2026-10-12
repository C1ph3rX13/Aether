package controlplane

import (
	"context"
	"errors"

	"github.com/C1ph3rX13/Aether/internal/controlplane/input"
	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type projectExecutionPort struct{ runs *rundomain.Service }

var _ project.ExecutionPort = projectExecutionPort{}

// NewProjectExecutionPort returns the complete adapter used by Project
// commands to coordinate the Run ledger in the caller-owned transaction.
func NewProjectExecutionPort(runs *rundomain.Service) (project.ExecutionPort, error) {
	if runs == nil {
		return nil, errors.New("Run service is required")
	}
	return projectExecutionPort{runs: runs}, nil
}

type runProjectPort struct{ projects *project.RunAccess }

var _ rundomain.ProjectAccess = runProjectPort{}

// NewRunProjectPort returns the narrow Project adapter consumed by Run.
func NewRunProjectPort(projects *project.RunAccess) (rundomain.ProjectAccess, error) {
	if projects == nil {
		return nil, errors.New("Project Run access is required")
	}
	return runProjectPort{projects: projects}, nil
}

func (port projectExecutionPort) EnsureReasonSchedulingBaseline(
	ctx context.Context, tx database.DBTX, projectID string, generation int, inputRevision int64, createdAt string,
) error {
	return port.runs.EnsureReasonSchedulingBaseline(ctx, tx, projectID, generation, inputRevision, createdAt)
}

func (port projectExecutionPort) PreviewRunCancellation(
	ctx context.Context, tx database.DBTX, projectID string, selector project.RunCancellationSelector,
) (int, error) {
	return port.runs.PreviewRunCancellation(ctx, tx, projectID, toRunCancellationSelector(selector))
}

func (port projectExecutionPort) CancelRuns(
	ctx context.Context, tx database.DBTX, projectID string, revision int64,
	reason project.CancellationReason, selector project.RunCancellationSelector,
) (int, error) {
	code := mapCancellationReason(reason)
	count, err := port.runs.CancelRuns(ctx, tx, projectID, revision, code, toRunCancellationSelector(selector))
	var domainErr *rundomain.DomainError
	if errors.As(err, &domainErr) && domainErr.Code == rundomain.ErrorConflict {
		return 0, project.Conflict(domainErr.Detail)
	}
	return count, err
}

func toRunCancellationSelector(selector project.RunCancellationSelector) rundomain.RunCancellationSelector {
	converted := rundomain.RunCancellationSelector{Clauses: make([]rundomain.RunCancellationClause, len(selector.Clauses))}
	for index, clause := range selector.Clauses {
		tasks := make([]rundomain.TaskType, len(clause.Tasks))
		for taskIndex, task := range clause.Tasks {
			tasks[taskIndex] = rundomain.TaskType(task)
		}
		converted.Clauses[index] = rundomain.RunCancellationClause{
			Tasks: tasks, TargetIntentID: clause.TargetIntentID,
			SourceFactID: clause.SourceFactID, InputRevisionAtMost: clause.InputRevisionAtMost,
		}
	}
	return converted
}

func mapCancellationReason(reason project.CancellationReason) rundomain.ErrorCode {
	switch reason {
	case project.CancellationProjectPaused:
		return rundomain.ErrorProjectPaused
	case project.CancellationProjectCompleted:
		return rundomain.ErrorProjectCompleted
	case project.CancellationBlackboardChanged:
		return rundomain.ErrorBlackboardChanged
	case project.CancellationFactRetracted:
		return rundomain.ErrorFactRetracted
	case project.CancellationIntentCancelled:
		return rundomain.ErrorIntentCancelled
	case project.CancellationOperatorCompleted:
		return rundomain.ErrorOperatorCompleted
	case project.CancellationProjectDeleted:
		return rundomain.ErrorProjectDeleted
	default:
		return rundomain.ErrorInvalidArgument
	}
}

func (port projectExecutionPort) QueueProjectCleanup(
	ctx context.Context, tx database.DBTX, projectID, createdAt string,
) (string, error) {
	return port.runs.QueueProjectCleanup(ctx, tx, projectID, createdAt)
}

func (port runProjectPort) Admission(
	ctx context.Context, tx database.DBTX, projectID string, task rundomain.TaskType, targetIntentID *string,
) (rundomain.ProjectAdmission, error) {
	admission, err := port.projects.ExecutionAdmission(ctx, tx, projectID, project.RunTask(task), targetIntentID)
	if err != nil {
		return rundomain.ProjectAdmission{}, err
	}
	result := rundomain.ProjectAdmission{
		Active:     admission.Status == project.ProjectStatusActive,
		Generation: admission.Generation, Revision: admission.Revision, ConfigRevision: admission.ConfigRevision,
		TargetAvailable: admission.TargetAvailable, IntentLeaseSeconds: admission.IntentLeaseSeconds,
		ReasonLeaseSeconds: admission.ReasonLeaseSeconds,
	}
	if !result.Active || !result.TargetAvailable {
		return result, nil
	}
	switch task {
	case rundomain.TaskReason, rundomain.TaskExplore:
		blackboardInput, inputErr := input.BlackboardInputForRun(admission.Input, targetIntentID)
		if inputErr != nil {
			return rundomain.ProjectAdmission{}, inputErr
		}
		result.BlackboardInput = &blackboardInput
	case rundomain.TaskBootstrap:
		bootstrapInput, inputErr := input.BootstrapInputForRun(admission.Input, *targetIntentID)
		if inputErr != nil {
			return rundomain.ProjectAdmission{}, inputErr
		}
		result.BootstrapInput = &bootstrapInput
	}
	return result, nil
}

func (port runProjectPort) Revision(ctx context.Context, tx database.DBTX, projectID string) (int64, error) {
	return port.projects.ExecutionRevision(ctx, tx, projectID)
}

func (port runProjectPort) ReplayChangesBatch(
	ctx context.Context, tx database.DBTX, queries []rundomain.ProjectChangeRangeQuery,
) ([]rundomain.ProjectChangeRange, error) {
	projectQueries := make([]project.ExecutionChangeRangeQuery, len(queries))
	for index, query := range queries {
		projectQueries[index] = project.ExecutionChangeRangeQuery{
			ProjectID: query.ProjectID, AfterRevision: query.AfterRevision, ThroughRevision: query.ThroughRevision,
		}
	}
	replays, err := port.projects.ReplayExecutionChangesBatch(ctx, tx, projectQueries)
	if err != nil {
		return nil, err
	}
	result := make([]rundomain.ProjectChangeRange, len(replays))
	for index, replay := range replays {
		result[index].ReplayRequired = replay.ReplayRequired
		result[index].Changes = make([]rundomain.ProjectChange, len(replay.Changes))
		for changeIndex, change := range replay.Changes {
			result[index].Changes[changeIndex] = rundomain.ProjectChange{
				ProjectID: queries[index].ProjectID, Revision: change.Revision,
				Generation: change.Generation, Kind: rundomain.ProjectChangeKind(change.Kind),
			}
		}
	}
	return result, nil
}

func (port runProjectPort) ApplyRunResult(
	ctx context.Context, tx database.DBTX, command rundomain.ApplyProjectResultCommand,
) (rundomain.ApplyProjectResult, error) {
	result, err := port.projects.ApplyRunResult(ctx, tx, project.ApplyRunResultCommand{
		RunID: command.RunID, ProjectID: command.ProjectID, TargetIntent: command.TargetIntent,
		Task: project.RunTask(command.Task), Generation: command.Generation,
		InputRevision: command.InputRevision, Worker: command.Worker,
		Outcome: toProjectRunOutcome(command.Outcome), AppliedAt: command.AppliedAt,
	})
	if err != nil {
		return rundomain.ApplyProjectResult{}, mapRunProjectError(err)
	}
	var change *rundomain.ProjectChange
	if result.Change != nil {
		converted := toRunProjectChange(*result.Change)
		change = &converted
	}
	return rundomain.ApplyProjectResult{
		ProjectRevision: result.ProjectRevision, Stale: result.Stale, Change: change,
		CreatedFactIDs: result.CreatedFactIDs, CreatedIntentIDs: result.CreatedIntentIDs,
		CompletionIntentID: result.CompletionIntentID,
	}, nil
}

func toProjectRunOutcome(outcome rundomain.Outcome) project.RunOutcome {
	converted := project.RunOutcome{
		Kind: project.RunOutcomeKind(outcome.Kind), Description: outcome.Description,
		From: append([]string(nil), outcome.From...),
	}
	if outcome.Fact != nil {
		converted.FactDescription = outcome.Fact.Description
	}
	converted.Intents = make([]project.ProposedRunIntent, len(outcome.Intents))
	for index, intent := range outcome.Intents {
		converted.Intents[index] = project.ProposedRunIntent{
			From: append([]string(nil), intent.From...), Description: intent.Description,
		}
	}
	return converted
}

func toRunProjectChange(change project.BlackboardChange) rundomain.ProjectChange {
	return rundomain.ProjectChange{
		ProjectID: change.ProjectID, Revision: change.Revision, Generation: change.Generation,
		Kind: rundomain.ProjectChangeKind(change.Kind), EntityType: change.EntityType,
		EntityID: change.EntityID, Data: append([]byte(nil), change.Data...), CreatedAt: change.CreatedAt,
	}
}

func mapRunProjectError(err error) error {
	var domainErr *project.DomainError
	if !errors.As(err, &domainErr) {
		return err
	}
	code := rundomain.ErrorInternal
	switch domainErr.Code {
	case project.ErrorInvalidArgument:
		code = rundomain.ErrorInvalidResult
	case project.ErrorNotFound, project.ErrorConflict:
		code = rundomain.ErrorStaleInput
	}
	return &rundomain.DomainError{Code: code, Detail: domainErr.Detail}
}
