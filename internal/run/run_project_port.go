package run

import (
	"context"
	"encoding/json"

	"github.com/C1ph3rX13/Aether/internal/database"
)

// ProjectAdmission contains the Project state needed to create one Execution Run.
// Blackboard records and persistence details stay behind the ProjectAccess bridge
// used by the application composition root.
type ProjectAdmission struct {
	Active             bool
	Generation         int
	Revision           int64
	ConfigRevision     string
	TargetAvailable    bool
	BlackboardInput    *BlackboardInput
	BootstrapInput     *BootstrapInput
	IntentLeaseSeconds int
	ReasonLeaseSeconds int
}

type ProjectChangeKind string

const (
	ProjectChangeProjectBaseline    ProjectChangeKind = "project_baseline"
	ProjectChangeProjectCreated     ProjectChangeKind = "project_created"
	ProjectChangeProjectRenamed     ProjectChangeKind = "project_renamed"
	ProjectChangeProjectPaused      ProjectChangeKind = "project_paused"
	ProjectChangeProjectResumed     ProjectChangeKind = "project_resumed"
	ProjectChangeIntentCreated      ProjectChangeKind = "intent_created"
	ProjectChangeHintCreated        ProjectChangeKind = "hint_created"
	ProjectChangeHintWithdrawn      ProjectChangeKind = "hint_withdrawn"
	ProjectChangeIntentConcluded    ProjectChangeKind = "intent_concluded"
	ProjectChangeIntentCancelled    ProjectChangeKind = "intent_cancelled"
	ProjectChangeFactSuperseded     ProjectChangeKind = "fact_superseded"
	ProjectChangeFactRetracted      ProjectChangeKind = "fact_retracted"
	ProjectChangeProjectCompleted   ProjectChangeKind = "project_completed"
	ProjectChangeProjectReopened    ProjectChangeKind = "project_reopened"
	ProjectChangeIntentsProposed    ProjectChangeKind = "intents_proposed"
	ProjectChangeBootstrapCompleted ProjectChangeKind = "bootstrap_completed"
)

type ProjectChange struct {
	ProjectID  string
	Revision   int64
	Generation int
	Kind       ProjectChangeKind
	EntityType string
	EntityID   *string
	Data       json.RawMessage
	CreatedAt  string
}

type ProjectChangeRangeQuery struct {
	ProjectID       string
	AfterRevision   int64
	ThroughRevision int64
}

type ProjectChangeRange struct {
	ReplayRequired bool
	Changes        []ProjectChange
}

type ApplyProjectResultCommand struct {
	RunID         string
	ProjectID     string
	TargetIntent  *string
	Task          TaskType
	Generation    int
	InputRevision int64
	Worker        string
	Outcome       Outcome
	AppliedAt     string
}

type ApplyProjectResult struct {
	ProjectRevision    int64
	Stale              bool
	Change             *ProjectChange
	CreatedFactIDs     []string
	CreatedIntentIDs   []string
	CompletionIntentID string
}

// ProjectAccess is the Run-owned view of Project. The database handle is passed
// only so Run and Project can commit one business command atomically; callers
// outside this package do not depend on this persistence seam.
type ProjectAccess interface {
	Admission(
		context.Context,
		database.DBTX,
		string,
		TaskType,
		*string,
	) (ProjectAdmission, error)
	Revision(context.Context, database.DBTX, string) (int64, error)
	ReplayChangesBatch(context.Context, database.DBTX, []ProjectChangeRangeQuery) ([]ProjectChangeRange, error)
	ApplyRunResult(context.Context, database.DBTX, ApplyProjectResultCommand) (ApplyProjectResult, error)
}

// projectChangePublisher emits committed Project change notifications only
// after the surrounding Run/Project transaction commits.
type projectChangePublisher interface {
	PublishProjectChange(ProjectChange)
}

type activityChangePublisher interface {
	PublishActivityChanged(projectID string)
}
