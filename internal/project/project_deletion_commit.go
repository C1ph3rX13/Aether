package project

import "context"

// commitProjectDeletion preserves cleanup evidence before the Project cascade.
// Deletion has no surviving Blackboard Revision or History entry; its terminal
// notification names the last committed Revision and is sent only after commit.
func commitProjectDeletion(
	ctx context.Context, service *Service,
	target projectRevisionTarget, command deleteProjectCommand,
) (DeleteProjectReceipt, error) {
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return DeleteProjectReceipt{}, err
	}
	defer tx.Rollback()
	projectMeta, err := loadRevisionProject(ctx, service.serviceCore, tx, target, command)
	if err != nil {
		return DeleteProjectReceipt{}, err
	}
	affectedRuns, err := service.execution.CancelRuns(
		ctx, tx, projectMeta.ID, projectMeta.Revision, CancellationProjectDeleted,
		RunCancellationSelector{Clauses: []RunCancellationClause{{}}},
	)
	if err != nil {
		return DeleteProjectReceipt{}, err
	}
	jobID, err := service.execution.QueueProjectCleanup(ctx, tx, projectMeta.ID, UTCNow())
	if err != nil {
		return DeleteProjectReceipt{}, err
	}
	if err := service.projects.Delete(ctx, tx, projectMeta.ID); err != nil {
		return DeleteProjectReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeleteProjectReceipt{}, err
	}
	if publisher, ok := service.publisher.(ProjectDeletionPublisher); ok {
		publisher.PublishProjectDeleted(projectMeta.ID, projectMeta.Revision)
	}
	return DeleteProjectReceipt{
		ProjectID: projectMeta.ID, ProjectRevision: projectMeta.Revision,
		AffectedRunCount: affectedRuns, CleanupJobID: jobID,
	}, nil
}
