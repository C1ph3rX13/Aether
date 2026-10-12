package project

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/database"
)

func TestPreviewImpactRoutesSupportedActionsThroughExecutionCoordination(t *testing.T) {
	tests := []struct {
		name         string
		prepare      func(*testing.T, *Service, *database.DB, ProjectDetail) (ProjectDetail, string)
		action       ImpactAction
		wantSelector func(ProjectDetail, string) RunCancellationSelector
	}{
		{
			name:   "hint change",
			action: ImpactHintChange,
			wantSelector: func(project ProjectDetail, _ string) RunCancellationSelector {
				return hintChangeSelector(project.Project.Revision)
			},
		},
		{
			name:   "retract fact",
			action: ImpactRetractFact,
			prepare: func(_ *testing.T, _ *Service, _ *database.DB, project ProjectDetail) (ProjectDetail, string) {
				return project, "origin"
			},
			wantSelector: func(project ProjectDetail, entityID string) RunCancellationSelector {
				return factCorrectionSelector(entityID, project.Project.Revision)
			},
		},
		{
			name:   "supersede fact",
			action: ImpactSupersedeFact,
			prepare: func(_ *testing.T, _ *Service, _ *database.DB, project ProjectDetail) (ProjectDetail, string) {
				return project, "origin"
			},
			wantSelector: func(project ProjectDetail, entityID string) RunCancellationSelector {
				return factCorrectionSelector(entityID, project.Project.Revision)
			},
		},
		{
			name:   "cancel intent",
			action: ImpactCancelIntent,
			prepare: func(t *testing.T, service *Service, db *database.DB, project ProjectDetail) (ProjectDetail, string) {
				intent := proposeTestIntent(t, service, db, project.Project.ID, "investigate evidence", []string{"origin"})
				updated, err := service.Get(t.Context(), project.Project.ID)
				if err != nil {
					t.Fatal(err)
				}
				return updated, intent.ID
			},
			wantSelector: func(_ ProjectDetail, entityID string) RunCancellationSelector {
				return RunCancellationSelector{Clauses: []RunCancellationClause{{
					Tasks: []RunTask{RunTaskExplore, RunTaskBootstrap}, TargetIntentID: &entityID,
				}}}
			},
		},
		{
			name:   "complete project",
			action: ImpactCompleteProject,
			wantSelector: func(ProjectDetail, string) RunCancellationSelector {
				return RunCancellationSelector{Clauses: []RunCancellationClause{{}}}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, db, _, execution := newHintRevisionCommitTestService(t)
			created := mustCreateHintTestProject(t, service)
			current := created
			entityID := ""
			if test.prepare != nil {
				current, entityID = test.prepare(t, service, db, created)
			}
			execution.count = 7

			preview, err := service.PreviewImpact(t.Context(), current.Project.ID, ImpactRequest{
				Action: test.action, EntityID: entityID, ExpectedRevision: current.Project.Revision,
			})
			if err != nil {
				t.Fatal(err)
			}
			if preview != (ImpactPreview{ProjectRevision: current.Project.Revision, AffectedRunCount: 7}) {
				t.Fatalf("impact preview = %+v", preview)
			}
			if len(execution.previewCalls) != 1 {
				t.Fatalf("preview calls = %+v", execution.previewCalls)
			}
			call := execution.previewCalls[0]
			if call.projectID != current.Project.ID || !reflect.DeepEqual(call.selector, test.wantSelector(current, entityID)) {
				t.Fatalf("preview call = %+v, want project %q selector %+v", call, current.Project.ID, test.wantSelector(current, entityID))
			}
		})
	}
}

func TestPreviewImpactReturnsExplicitZeroFromRequiredCoordination(t *testing.T) {
	service, _, _, execution := newHintRevisionCommitTestService(t)
	created := mustCreateHintTestProject(t, service)
	execution.count = 0

	preview, err := service.PreviewImpact(t.Context(), created.Project.ID, ImpactRequest{
		Action: ImpactHintChange, ExpectedRevision: created.Project.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.AffectedRunCount != 0 || len(execution.previewCalls) != 1 {
		t.Fatalf("zero impact preview = %+v calls=%+v", preview, execution.previewCalls)
	}
}

func TestPreviewImpactRejectsInvalidInputBeforeExecutionCoordination(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *Service, *database.DB, ProjectDetail) (ProjectDetail, ImpactRequest)
	}{
		{
			name: "invalid action",
			setup: func(_ *testing.T, _ *Service, _ *database.DB, project ProjectDetail) (ProjectDetail, ImpactRequest) {
				return project, ImpactRequest{Action: ImpactAction("unsupported"), ExpectedRevision: project.Project.Revision}
			},
		},
		{
			name: "missing fact",
			setup: func(_ *testing.T, _ *Service, _ *database.DB, project ProjectDetail) (ProjectDetail, ImpactRequest) {
				return project, ImpactRequest{Action: ImpactRetractFact, EntityID: "missing", ExpectedRevision: project.Project.Revision}
			},
		},
		{
			name: "goal fact",
			setup: func(_ *testing.T, _ *Service, _ *database.DB, project ProjectDetail) (ProjectDetail, ImpactRequest) {
				return project, ImpactRequest{Action: ImpactRetractFact, EntityID: "goal", ExpectedRevision: project.Project.Revision}
			},
		},
		{
			name: "missing intent",
			setup: func(_ *testing.T, _ *Service, _ *database.DB, project ProjectDetail) (ProjectDetail, ImpactRequest) {
				return project, ImpactRequest{Action: ImpactCancelIntent, EntityID: "missing", ExpectedRevision: project.Project.Revision}
			},
		},
		{
			name: "stale revision",
			setup: func(_ *testing.T, _ *Service, _ *database.DB, project ProjectDetail) (ProjectDetail, ImpactRequest) {
				return project, ImpactRequest{Action: ImpactHintChange, ExpectedRevision: project.Project.Revision - 1}
			},
		},
		{
			name: "paused project",
			setup: func(t *testing.T, service *Service, _ *database.DB, project ProjectDetail) (ProjectDetail, ImpactRequest) {
				paused, err := service.PauseProject(t.Context(), project.Project.ID, ProjectLifecycleRequest{ExpectedRevision: project.Project.Revision})
				if err != nil {
					t.Fatal(err)
				}
				return ProjectDetail{Project: paused}, ImpactRequest{Action: ImpactCompleteProject, ExpectedRevision: paused.Revision}
			},
		},
		{
			name: "completed project",
			setup: func(t *testing.T, service *Service, _ *database.DB, project ProjectDetail) (ProjectDetail, ImpactRequest) {
				completed, err := service.CompleteProject(t.Context(), project.Project.ID, CompleteRequest{
					From: []string{"origin"}, Description: "done", ExpectedRevision: project.Project.Revision,
				})
				if err != nil {
					t.Fatal(err)
				}
				return ProjectDetail{Project: completed.Project}, ImpactRequest{Action: ImpactRetractFact, EntityID: "origin", ExpectedRevision: completed.Project.Revision}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, db, _, execution := newHintRevisionCommitTestService(t)
			created := mustCreateHintTestProject(t, service)
			current, request := test.setup(t, service, db, created)
			_, err := service.PreviewImpact(t.Context(), current.Project.ID, request)
			if err == nil {
				t.Fatal("invalid impact preview succeeded")
			}
			if len(execution.previewCalls) != 0 {
				t.Fatalf("invalid impact preview called execution coordination: %+v", execution.previewCalls)
			}
		})
	}
}

func TestNewServiceRejectsTypedNilExecutionCoordination(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(t.TempDir() + "/aether.db"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var execution *projectExecutionTestAdapter
	service, err := NewService(db, noopProjectEventPublisher{}, execution)
	if service != nil {
		t.Fatalf("typed-nil execution coordination service = %+v, want nil", service)
	}
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != ErrorInternal ||
		!strings.Contains(domainErr.Detail, "execution coordination") {
		t.Fatalf("typed-nil execution coordination error = %v", err)
	}
}

func TestNewServiceRejectsMissingExecutionCoordination(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(t.TempDir() + "/aether.db"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	service, err := NewService(db, noopProjectEventPublisher{}, nil)
	if service != nil {
		t.Fatalf("missing execution coordination service = %+v, want nil", service)
	}
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != ErrorInternal ||
		!strings.Contains(domainErr.Detail, "execution coordination") {
		t.Fatalf("missing execution coordination error = %v", err)
	}
}
