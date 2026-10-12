package project

import "context"

// projectQuery composes repository calls to build full project detail.
// This is a query-side composition (read-only), not a service with business logic.
type projectQuery struct {
	projects    *projectStore
	facts       *FactStore
	intents     *IntentStore
	hints       *HintStore
	completions *CompletionStore
}

func newProjectQuery() *projectQuery {
	return &projectQuery{
		projects:    &projectStore{},
		facts:       &FactStore{},
		intents:     &IntentStore{},
		hints:       &HintStore{},
		completions: &CompletionStore{},
	}
}

// GetDetail builds a full ProjectDetail from the database.
func (q *projectQuery) GetDetail(ctx context.Context, conn DBTX, projectID string) (ProjectDetail, error) {
	input, err := q.GetExecutionInput(ctx, conn, projectID)
	if err != nil {
		return ProjectDetail{}, err
	}
	completions, err := q.completions.ListByProject(ctx, conn, projectID)
	if err != nil {
		return ProjectDetail{}, err
	}

	return ProjectDetail{
		Project:           input.Project,
		Facts:             input.Facts,
		Intents:           input.Intents,
		Hints:             input.Hints,
		CompletionRecords: completions,
	}, nil
}

// GetExecutionInput loads only the authoritative records that can contribute
// to a task input. Completion history is audit data and is excluded from Run
// Admission's write transaction.
func (q *projectQuery) GetExecutionInput(ctx context.Context, conn DBTX, projectID string) (ExecutionInputProjection, error) {
	meta, err := q.projects.GetMeta(ctx, conn, projectID)
	if err != nil {
		return ExecutionInputProjection{}, err
	}
	facts, err := q.facts.ListByProject(ctx, conn, projectID)
	if err != nil {
		return ExecutionInputProjection{}, err
	}
	intents, err := q.intents.ListByProject(ctx, conn, projectID)
	if err != nil {
		return ExecutionInputProjection{}, err
	}
	hints, err := q.hints.ListByProject(ctx, conn, projectID)
	if err != nil {
		return ExecutionInputProjection{}, err
	}
	return ExecutionInputProjection{Project: meta, Facts: facts, Intents: intents, Hints: hints}, nil
}
