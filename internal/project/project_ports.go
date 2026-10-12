package project

import "context"

// ConfigRevisionProvider reads the active Config Revision through the Project
// command transaction. The provider is kept as a function to avoid coupling
// the Project module to configuration storage or Control Plane types.
type ConfigRevisionProvider func(context.Context, DBTX) (string, error)

// EventPublisher is the notification seam used after committed Project state
// changes. Delivery is intentionally outside the aggregate transaction.
type EventPublisher interface {
	PublishChange(change BlackboardChange)
}

// ProjectDeletionPublisher is an optional terminal notification capability.
// Project deletion has no surviving change log, so it cannot be represented as
// a BlackboardChange after the aggregate transaction commits.
type ProjectDeletionPublisher interface {
	PublishProjectDeleted(projectID string, revision int64)
}

// CancellationReason describes why active Execution Runs lose commit rights.
type CancellationReason string

const (
	CancellationProjectPaused     CancellationReason = "project_paused"
	CancellationProjectCompleted  CancellationReason = "project_completed"
	CancellationBlackboardChanged CancellationReason = "blackboard_changed"
	CancellationFactRetracted     CancellationReason = "fact_retracted"
	CancellationIntentCancelled   CancellationReason = "intent_cancelled"
	CancellationOperatorCompleted CancellationReason = "operator_completed"
	CancellationProjectDeleted    CancellationReason = "project_deleted"
)

// RunCancellationClause selects active Runs affected by a Blackboard command.
// Clauses are ORed; fields inside one clause are ANDed.
type RunCancellationClause struct {
	Tasks               []RunTask
	TargetIntentID      *string
	SourceFactID        *string
	InputRevisionAtMost *int64
}

type RunCancellationSelector struct {
	Clauses []RunCancellationClause
}

// ExecutionPort is the complete transaction participant used by Project
// commands. The caller's DBTX keeps Project and Run-ledger changes atomic.
type ExecutionPort interface {
	EnsureReasonSchedulingBaseline(
		ctx context.Context,
		tx DBTX,
		projectID string,
		generation int,
		inputRevision int64,
		createdAt string,
	) error
	PreviewRunCancellation(
		ctx context.Context,
		tx DBTX,
		projectID string,
		selector RunCancellationSelector,
	) (int, error)
	// An empty clause matches every active Run in the Project.
	CancelRuns(
		ctx context.Context,
		tx DBTX,
		projectID string,
		projectRevision int64,
		reason CancellationReason,
		selector RunCancellationSelector,
	) (int, error)
	QueueProjectCleanup(ctx context.Context, tx DBTX, projectID, createdAt string) (string, error)
}
