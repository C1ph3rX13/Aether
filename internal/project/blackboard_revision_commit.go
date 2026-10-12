package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// projectRevisionCommand keeps command-specific validation and mutation in the
// Project module while the commit operation owns the shared consistency
// protocol around it.
type projectRevisionCommand[Mutation projectRevisionMutation] interface {
	projectRevisionValidator
	applyRevision(context.Context, *serviceCore, ExecutionPort, DBTX, projectRevisionContext) (Mutation, error)
}

type projectRevisionValidator interface {
	validateRevisionProject(ProjectMeta) error
	validateRevisionBeforeExpected() bool
}

type projectRevisionTarget struct {
	ProjectID        string
	ExpectedRevision *int64
}

func existingProjectRevisionTarget(projectID string, expectedRevision int64) projectRevisionTarget {
	return projectRevisionTarget{ProjectID: projectID, ExpectedRevision: &expectedRevision}
}

type projectRevisionContext struct {
	Project    ProjectMeta
	OccurredAt string
}

// projectRevisionMutation is the typed transition extension point for Project
// commands. Each mutation owns its Run impact and History evidence while the
// commit operation owns their ordering and atomicity.
type projectRevisionMutation interface {
	revisionCancellation(ProjectMeta) projectRevisionCancellation
	revisionEvidence(affectedRuns int) projectRevisionEvidence
}

type projectRevisionCommitReceipt[Mutation projectRevisionMutation] struct {
	Project          ProjectMeta
	AffectedRunCount int
	Change           BlackboardChange
	Mutation         Mutation
}

type projectRevisionEvidence struct {
	Kind       ChangeKind
	EntityType string
	EntityID   *string
	Before     projectRevisionEntities
	After      projectRevisionEntities
	Result     map[string]any
}

// projectRevisionEntities contains only the records touched by a command.
// Project metadata and Revision allocation belong to the commit operation.
type projectRevisionEntities struct {
	Facts             []Fact
	Intents           []Intent
	Hints             []Hint
	CompletionRecords []CompletionRecord
}

func (entities projectRevisionEntities) withProject(meta ProjectMeta) ProjectDetail {
	return ProjectDetail{
		Project: meta, Facts: entities.Facts, Intents: entities.Intents,
		Hints: entities.Hints, CompletionRecords: entities.CompletionRecords,
	}
}

type projectRevisionCancellation struct {
	Reason   CancellationReason
	Selector RunCancellationSelector
}

type projectRevisionCommitContext struct {
	OccurredAt string
	Source     ChangeSource
}

// commitProjectRevision is the Project-owned write protocol for one committed
// Blackboard revision. Nothing is published until every transaction
// participant and the SQLite commit have succeeded.
func commitProjectRevision[Mutation projectRevisionMutation](
	ctx context.Context,
	service *Service,
	target projectRevisionTarget,
	command projectRevisionCommand[Mutation],
) (projectRevisionCommitReceipt[Mutation], error) {
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return projectRevisionCommitReceipt[Mutation]{}, err
	}
	defer tx.Rollback()
	receipt, err := applyProjectRevisionInTransaction(
		ctx, service.serviceCore, service.execution, tx, target, command,
		projectRevisionCommitContext{
			OccurredAt: UTCNow(),
			Source:     ChangeSource{Kind: "operator", Name: service.operatorName},
		},
	)
	if err != nil {
		return projectRevisionCommitReceipt[Mutation]{}, err
	}
	if err := tx.Commit(); err != nil {
		return projectRevisionCommitReceipt[Mutation]{}, err
	}

	if service.publisher != nil {
		service.publisher.PublishChange(receipt.Change)
	}
	return receipt, nil
}

// applyProjectRevisionInTransaction applies the Project-owned revision
// protocol without taking ownership of the caller's transaction or publishing
// its change. Finish Run uses this path so the Run terminal state, Claim
// release, Blackboard mutation, and History evidence commit together.
func applyProjectRevisionInTransaction[Mutation projectRevisionMutation](
	ctx context.Context,
	service *serviceCore,
	execution ExecutionPort,
	tx DBTX,
	target projectRevisionTarget,
	command projectRevisionCommand[Mutation],
	commitContext projectRevisionCommitContext,
) (projectRevisionCommitReceipt[Mutation], error) {

	projectMeta, err := loadRevisionProject(ctx, service, tx, target, command)
	if err != nil {
		return projectRevisionCommitReceipt[Mutation]{}, err
	}
	revisionContext := projectRevisionContext{Project: projectMeta, OccurredAt: commitContext.OccurredAt}
	mutation, err := command.applyRevision(ctx, service, execution, tx, revisionContext)
	if err != nil {
		return projectRevisionCommitReceipt[Mutation]{}, err
	}
	projectID := target.ProjectID
	if created, ok := any(mutation).(projectCreatedRevision); ok {
		projectID = created.Detail.Project.ID
	}
	revision, err := service.projects.AdvanceRevision(ctx, tx, projectID)
	if err != nil {
		return projectRevisionCommitReceipt[Mutation]{}, err
	}
	if revision != projectMeta.Revision+1 {
		return projectRevisionCommitReceipt[Mutation]{}, fmt.Errorf(
			"Project revision advanced from %d to %d, want %d",
			projectMeta.Revision, revision, projectMeta.Revision+1,
		)
	}
	afterProject, err := service.projects.GetMeta(ctx, tx, projectID)
	if err != nil {
		return projectRevisionCommitReceipt[Mutation]{}, err
	}

	affectedRuns := 0
	cancellation := mutation.revisionCancellation(projectMeta)
	if cancellation.Reason != "" {
		affectedRuns, err = execution.CancelRuns(
			ctx, tx, projectID, revision, cancellation.Reason, cancellation.Selector,
		)
		if err != nil {
			return projectRevisionCommitReceipt[Mutation]{}, err
		}
	}
	evidence := mutation.revisionEvidence(affectedRuns)
	// Commands describe touched entities. Project state and the allocated
	// Revision always come from the authoritative transaction, including
	// creation and Generation changes.
	if evidence.EntityType == "project" {
		evidence.EntityID = &projectID
	}
	change, err := service.appendDeltaChange(
		ctx, tx, projectID, afterProject.Generation, revision,
		evidence.Kind, evidence.EntityType, evidence.EntityID,
		evidence.Before.withProject(projectMeta), evidence.After.withProject(afterProject),
		commitContext.Source, evidence.Result, revisionContext.OccurredAt,
	)
	if err != nil {
		return projectRevisionCommitReceipt[Mutation]{}, err
	}
	return projectRevisionCommitReceipt[Mutation]{
		Project: afterProject, AffectedRunCount: affectedRuns,
		Change: change, Mutation: mutation,
	}, nil
}

func loadRevisionProject(
	ctx context.Context, service *serviceCore, tx DBTX,
	target projectRevisionTarget, command projectRevisionValidator,
) (ProjectMeta, error) {
	var projectMeta ProjectMeta
	var err error
	if target.ProjectID != "" {
		projectMeta, err = service.projects.GetMeta(ctx, tx, target.ProjectID)
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectMeta{}, ErrProjectNotFound
		}
		if err != nil {
			return ProjectMeta{}, err
		}
	}
	if !command.validateRevisionBeforeExpected() {
		if err := command.validateRevisionProject(projectMeta); err != nil {
			return ProjectMeta{}, err
		}
	}
	if target.ExpectedRevision != nil && projectMeta.Revision != *target.ExpectedRevision {
		return ProjectMeta{}, RevisionConflict("project revision changed", projectMeta.Revision)
	}
	if command.validateRevisionBeforeExpected() {
		if err := command.validateRevisionProject(projectMeta); err != nil {
			return ProjectMeta{}, err
		}
	}

	return projectMeta, nil
}
