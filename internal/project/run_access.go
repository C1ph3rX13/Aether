package project

import "context"

func (access *RunAccess) ExecutionAdmission(
	ctx context.Context,
	tx DBTX,
	projectID string,
	task RunTask,
	targetIntentID *string,
) (ExecutionAdmission, error) {
	return access.core.ExecutionAdmission(ctx, tx, projectID, task, targetIntentID)
}

func (access *RunAccess) ExecutionRevision(ctx context.Context, tx DBTX, projectID string) (int64, error) {
	return access.core.ExecutionRevision(ctx, tx, projectID)
}

// ReplayExecutionChangesBatch returns the typed Project history evidence used
// by Run scheduling while keeping the history implementation in Project.
func (access *RunAccess) ReplayExecutionChangesBatch(
	ctx context.Context,
	tx DBTX,
	queries []ExecutionChangeRangeQuery,
) ([]ExecutionChangeRange, error) {
	return access.core.ReplayExecutionChangesBatch(ctx, tx, queries)
}

// ApplyRunResult applies one already validated Run result in the caller-owned
// transaction. RunAccess does not expose any Project operator commands.
func (access *RunAccess) ApplyRunResult(
	ctx context.Context,
	tx DBTX,
	command ApplyRunResultCommand,
) (ApplyRunResult, error) {
	return access.core.ApplyRunResult(ctx, tx, command)
}
