package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
)

type projectSchedulingProjection struct {
	db               *database.DB
	projects         *project.Service
	runs             *rundomain.Service
	dispatcherPolicy DispatcherPolicyProvider
}

func newProjectSchedulingProjection(
	db *database.DB,
	projects *project.Service,
	runs *rundomain.Service,
	dispatcherPolicy DispatcherPolicyProvider,
) *projectSchedulingProjection {
	return &projectSchedulingProjection{
		db: db, projects: projects, runs: runs, dispatcherPolicy: dispatcherPolicy,
	}
}

func (projection *projectSchedulingProjection) Snapshot(
	ctx context.Context,
	request rundomain.SchedulingSnapshotRequest,
) (scheduler.ControlSchedulingSnapshot, error) {
	if err := request.Validate(); err != nil {
		return scheduler.ControlSchedulingSnapshot{}, err
	}
	tx, err := projection.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return scheduler.ControlSchedulingSnapshot{}, err
	}
	defer tx.Rollback()
	observedAt := projection.runs.SchedulingObservedAt()
	if err := projection.runs.AuthorizeDispatcherAt(
		ctx, tx, request.DispatcherID, request.DispatcherFencingToken, observedAt,
	); err != nil {
		return scheduler.ControlSchedulingSnapshot{}, err
	}

	supported := make(map[rundomain.TaskType]bool, len(request.SupportedTasks))
	for _, task := range request.SupportedTasks {
		supported[task] = true
	}
	result := scheduler.ControlSchedulingSnapshot{
		ObservedAt: observedAt, RevokedRunIDs: []rundomain.RunID{}, Work: []scheduler.ControlSchedulingWork{},
	}
	result.DispatcherPolicy, err = projection.dispatcherPolicy(ctx, tx)
	if err != nil {
		return scheduler.ControlSchedulingSnapshot{}, fmt.Errorf("read active Dispatcher policy: %w", err)
	}
	if err := result.DispatcherPolicy.Validate(); err != nil {
		return scheduler.ControlSchedulingSnapshot{}, err
	}
	result.RevokedRunIDs, err = projection.runs.InspectSchedulingRevocationsInTransaction(
		ctx, tx, request, observedAt,
	)
	if err != nil {
		return scheduler.ControlSchedulingSnapshot{}, fmt.Errorf("inspect scheduling Run revocations: %w", err)
	}
	result.Work, result.NextProjectCursor, err = projection.boundedSnapshotWork(
		ctx, tx, request.ProjectCursor, supported, observedAt, request.CandidateLimit,
	)
	if err != nil {
		return scheduler.ControlSchedulingSnapshot{}, err
	}
	return result, nil
}

func (projection *projectSchedulingProjection) boundedSnapshotWork(
	ctx context.Context,
	tx database.DBTX,
	cursor string,
	supported map[rundomain.TaskType]bool,
	observedAt time.Time,
	limit int,
) ([]scheduler.ControlSchedulingWork, string, error) {
	result := make([]scheduler.ControlSchedulingWork, 0, limit)
	examined := make(map[string]struct{}, min(limit, rundomain.MaximumSchedulingCandidates))
	nextCursor := cursor
	workBudget := rundomain.MaximumSchedulingInspectedWork
	for len(result) < limit && len(examined) < rundomain.MaximumSchedulingCandidates && workBudget > 0 {
		pageLimit := min(limit-len(result), rundomain.MaximumSchedulingCandidates-len(examined), workBudget)
		model, err := projection.projects.ReadSchedulingModel(ctx, tx, project.SchedulingReadRequest{
			ProjectCursor: nextCursor, ProjectLimit: pageLimit,
			BootstrapSupported: supported[rundomain.TaskBootstrap],
			ReasonSupported:    supported[rundomain.TaskReason],
		})
		if err != nil {
			return nil, "", err
		}
		fresh := make([]project.SchedulingProjectReadModel, 0, len(model.Projects))
		for _, item := range model.Projects {
			if _, duplicate := examined[item.ID]; duplicate {
				break
			}
			examined[item.ID] = struct{}{}
			fresh = append(fresh, item)
		}
		if len(fresh) == 0 {
			break
		}
		if len(fresh) == len(model.Projects) {
			nextCursor = model.NextProjectCursor
		} else {
			nextCursor = fresh[len(fresh)-1].ID
		}
		work, err := projection.boundedWork(
			ctx, tx, fresh, supported, observedAt, limit-len(result), &workBudget,
		)
		if err != nil {
			return nil, "", err
		}
		result = append(result, work...)
		if len(fresh) < len(model.Projects) || len(model.Projects) < pageLimit {
			break
		}
	}
	if len(examined) == 0 {
		nextCursor = ""
	}
	return result, nextCursor, nil
}

func (projection *projectSchedulingProjection) boundedWork(
	ctx context.Context,
	tx database.DBTX,
	projects []project.SchedulingProjectReadModel,
	supported map[rundomain.TaskType]bool,
	observedAt time.Time,
	limit int,
	workBudget *int,
) ([]scheduler.ControlSchedulingWork, error) {
	byProject := make(map[string][]scheduler.ControlSchedulingWork, len(projects))
	decisionBlocksExplore := make(map[string]bool, len(projects))
	projectByID := make(map[string]project.SchedulingProjectReadModel, len(projects))
	decisions := make([]scheduler.ControlSchedulingWork, 0, len(projects))
	for _, item := range projects {
		projectByID[item.ID] = item
		candidate, ok, err := schedulingDecisionCandidate(item)
		if err != nil {
			return nil, err
		}
		if ok {
			decisions = append(decisions, candidate)
		}
	}
	*workBudget -= len(decisions)
	if err := projection.attachSchedulingStates(ctx, tx, decisions, projectByID, observedAt); err != nil {
		return nil, err
	}
	for _, candidate := range decisions {
		if candidate.State.Claimed {
			decisionBlocksExplore[candidate.ProjectID] = true
			continue
		}
		if schedulingStateEligibleAt(candidate.State, observedAt) {
			byProject[candidate.ProjectID] = []scheduler.ControlSchedulingWork{candidate}
			decisionBlocksExplore[candidate.ProjectID] = true
		}
	}

	exploreProjectIDs := make([]string, 0, len(projects))
	if supported[rundomain.TaskExplore] {
		for _, item := range projects {
			if !decisionBlocksExplore[item.ID] && item.HasOpenIntents {
				exploreProjectIDs = append(exploreProjectIDs, item.ID)
			}
		}
	}
	exploreLimit := min(limit, *workBudget)
	intentReadLimit := min(*workBudget+exploreLimit, project.MaximumSchedulingReadIntents)
	intents := []project.SchedulingIntentReadModel{}
	if len(exploreProjectIDs) > 0 && exploreLimit > 0 {
		var err error
		intents, err = projection.projects.ReadSchedulingIntents(ctx, tx, project.SchedulingIntentReadRequest{
			ProjectIDs: exploreProjectIDs, Limit: intentReadLimit,
		})
		if err != nil {
			return nil, err
		}
	}
	explores := make([]scheduler.ControlSchedulingWork, 0, len(intents))
	for _, intent := range intents {
		item, exists := projectByID[intent.ProjectID]
		if !exists {
			return nil, fmt.Errorf("Project scheduling model returned Intent %s for an unknown Project", intent.ID)
		}
		candidate, err := schedulingCandidate(item, project.RunTaskExplore, intent.ID)
		if err != nil {
			return nil, err
		}
		explores = append(explores, candidate)
	}
	*workBudget -= min(len(explores), *workBudget)
	if err := projection.attachSchedulingStates(ctx, tx, explores, projectByID, observedAt); err != nil {
		return nil, err
	}
	for _, candidate := range explores {
		if schedulingStateEligibleAt(candidate.State, observedAt) {
			byProject[candidate.ProjectID] = append(byProject[candidate.ProjectID], candidate)
		}
	}

	result := make([]scheduler.ControlSchedulingWork, 0, limit)
	for round := 0; len(result) < limit; round++ {
		added := false
		for _, item := range projects {
			work := byProject[item.ID]
			if round >= len(work) {
				continue
			}
			result = append(result, work[round])
			added = true
			if len(result) == limit {
				break
			}
		}
		if !added {
			break
		}
	}
	return result, nil
}

func (projection *projectSchedulingProjection) attachSchedulingStates(
	ctx context.Context,
	tx database.DBTX,
	work []scheduler.ControlSchedulingWork,
	projects map[string]project.SchedulingProjectReadModel,
	observedAt time.Time,
) error {
	stateByKey := make(map[rundomain.WorkKey]rundomain.SchedulingWorkState, len(work))
	for offset := 0; offset < len(work); offset += rundomain.MaximumSchedulingWorkKeys {
		end := min(offset+rundomain.MaximumSchedulingWorkKeys, len(work))
		queries := make([]rundomain.SchedulingWorkQuery, 0, end-offset)
		for index := offset; index < end; index++ {
			item := work[index]
			queries = append(queries, rundomain.SchedulingWorkQuery{
				WorkKey: item.WorkKey, ProjectID: item.ProjectID, Task: item.Task,
				Generation: item.Generation, Revision: item.Revision,
				HasOpenIntents: projects[item.ProjectID].HasOpenIntents,
			})
		}
		states, err := projection.runs.InspectSchedulingWorkInTransaction(ctx, tx, queries, observedAt)
		if err != nil {
			return fmt.Errorf("inspect persisted scheduling Work: %w", err)
		}
		for _, state := range states {
			if _, duplicate := stateByKey[state.WorkKey]; duplicate {
				return fmt.Errorf("control plane duplicated scheduling Work %s", state.WorkKey)
			}
			stateByKey[state.WorkKey] = state
		}
	}
	for index := range work {
		state, ok := stateByKey[work[index].WorkKey]
		if !ok {
			return fmt.Errorf("control plane omitted scheduling Work %s", work[index].WorkKey)
		}
		work[index].State = state
	}
	return nil
}

func schedulingDecisionCandidate(
	projectModel project.SchedulingProjectReadModel,
) (scheduler.ControlSchedulingWork, bool, error) {
	switch projectModel.Decision.Kind {
	case project.SchedulingDecisionNone:
		return scheduler.ControlSchedulingWork{}, false, nil
	case project.SchedulingDecisionMissingBootstrap:
		return scheduler.ControlSchedulingWork{}, false, &rundomain.DomainError{
			Code: rundomain.ErrorNotReady, Detail: fmt.Sprintf("Project %s is missing its Bootstrap intent", projectModel.ID),
		}
	case project.SchedulingDecisionBootstrap:
		candidate, err := schedulingCandidate(
			projectModel,
			project.RunTaskBootstrap,
			projectModel.Decision.TargetIntentID,
		)
		return candidate, true, err
	case project.SchedulingDecisionReason:
		candidate, err := schedulingCandidate(projectModel, project.RunTaskReason, "")
		return candidate, true, err
	default:
		return scheduler.ControlSchedulingWork{}, false, fmt.Errorf(
			"Project %s returned an invalid scheduling decision",
			projectModel.ID,
		)
	}
}

func schedulingCandidate(
	projectModel project.SchedulingProjectReadModel,
	task project.RunTask,
	targetID string,
) (scheduler.ControlSchedulingWork, error) {
	runTask := rundomain.TaskType(task)
	candidate := scheduler.ControlSchedulingWork{
		ProjectID: projectModel.ID, ConfigRevision: projectModel.ConfigRevision,
		Generation: projectModel.Generation, Revision: projectModel.Revision,
		Task: runTask,
	}
	input := rundomain.InputSnapshot{Generation: projectModel.Generation, Revision: projectModel.Revision}
	if targetID != "" {
		input.TargetIntentID = &targetID
		candidate.Target = &scheduler.SchedulingTarget{ID: targetID}
	}
	workKey, err := rundomain.WorkKeyFor(projectModel.ID, runTask, input)
	if err != nil {
		return scheduler.ControlSchedulingWork{}, err
	}
	candidate.WorkKey = workKey
	return candidate, nil
}

func schedulingStateEligibleAt(state rundomain.SchedulingWorkState, observedAt time.Time) bool {
	if state.Claimed {
		return false
	}
	if state.RetryState != rundomain.RetryEligible && state.RetryState != rundomain.RetryBackoff {
		return false
	}
	return state.NextEligibleAt == nil || !state.NextEligibleAt.After(observedAt)
}
