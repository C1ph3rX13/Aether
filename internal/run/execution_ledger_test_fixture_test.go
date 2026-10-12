package run_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/controlplane"
	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type (
	ProjectService            = project.Service
	CreateProjectRequest      = project.CreateProjectRequest
	ProjectLifecycleRequest   = project.ProjectLifecycleRequest
	CreateHintInline          = project.CreateHintInline
	CreateHintRequest         = project.CreateHintRequest
	CancelIntentRequest       = project.CancelIntentRequest
	WithdrawHintRequest       = project.WithdrawHintRequest
	RetractFactRequest        = project.RetractFactRequest
	UpdateProjectTitleRequest = project.UpdateProjectTitleRequest
	ProjectDetail             = project.ProjectDetail
	ProjectMeta               = project.ProjectMeta
	Intent                    = project.Intent
	Fact                      = project.Fact
	Hint                      = project.Hint
)

const (
	ProjectStatusActive    = project.ProjectStatusActive
	ProjectStatusCompleted = project.ProjectStatusCompleted
)

// runTestService keeps mutable test controls outside the production Service.
type runTestService struct {
	*rundomain.Service
	DB        *database.DB
	readiness rundomain.StartReadiness
	clock     func() time.Time
}

type RunService = runTestService

func proposeIntent(t *testing.T, projects *project.Service, runs *runTestService, projectID, description string, from []string) project.Intent {
	t.Helper()
	detail, err := projects.Get(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := runs.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	appliedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := projects.ApplyRunResult(t.Context(), tx, project.ApplyRunResultCommand{
		RunID: "fixture-reason-" + time.Now().UTC().Format(time.RFC3339Nano), ProjectID: projectID,
		Task: project.RunTaskReason, Generation: detail.Project.Generation, InputRevision: detail.Project.Revision,
		Worker: "fixture.reasoner", AppliedAt: appliedAt,
		Outcome: project.RunOutcome{Kind: project.RunOutcomeProposeIntents, Intents: []project.ProposedRunIntent{{From: from, Description: description}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.EnsureReasonSchedulingBaseline(t.Context(), tx, projectID, detail.Project.Generation, result.ProjectRevision, appliedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	updated, err := projects.Get(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, intent := range updated.Intents {
		if len(result.CreatedIntentIDs) == 1 && intent.ID == result.CreatedIntentIDs[0] {
			return intent
		}
	}
	t.Fatal("fixture Run result did not create an Intent")
	return project.Intent{}
}

func concludeIntent(t *testing.T, projects *project.Service, runs *runTestService, projectID, intentID, description string) (project.Intent, project.Fact) {
	t.Helper()
	detail, err := projects.Get(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := runs.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := projects.ApplyRunResult(t.Context(), tx, project.ApplyRunResultCommand{
		RunID: "fixture-explore-" + time.Now().UTC().Format(time.RFC3339Nano), ProjectID: projectID,
		TargetIntent: &intentID, Task: project.RunTaskExplore, Generation: detail.Project.Generation,
		InputRevision: detail.Project.Revision, Worker: "fixture.explorer", AppliedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Outcome: project.RunOutcome{Kind: project.RunOutcomeConcludeIntent, FactDescription: description},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	updated, err := projects.Get(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	var concluded project.Intent
	for _, intent := range updated.Intents {
		if intent.ID == intentID {
			concluded = intent
		}
	}
	for _, fact := range updated.Facts {
		if len(result.CreatedFactIDs) == 1 && fact.ID == result.CreatedFactIDs[0] {
			return concluded, fact
		}
	}
	t.Fatal("fixture Run result did not create a Fact")
	return project.Intent{}, project.Fact{}
}

func (service *runTestService) claimIsCurrent(
	ctx context.Context,
	tx database.DBTX,
	execution rundomain.ExecutionRun,
	nowUnixNano int64,
) (bool, error) {
	return service.ClaimIsCurrent(ctx, tx, execution, nowUnixNano)
}

func newRunTestServices(t *testing.T, serviceOptions ...project.ServiceOptions) (*project.Service, *runTestService, func()) {
	return newRunTestServicesWithPublisher(t, nil, serviceOptions...)
}

func newRunTestServicesWithPublisher(
	t *testing.T,
	publisher interface{ PublishProjectChange(rundomain.ProjectChange) },
	serviceOptions ...project.ServiceOptions,
) (*project.Service, *runTestService, func()) {
	t.Helper()
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}

	runService := &runTestService{
		DB: db,
		readiness: rundomain.StartReadinessFunc(
			func(context.Context, rundomain.StartRequest) error { return nil },
		),
		clock: func() time.Time { return time.Now().UTC() },
	}
	projectForRun := project.NewRunAccess(serviceOptions...)
	runProjectAccess, err := controlplane.NewRunProjectPort(projectForRun)
	if err != nil {
		t.Fatal(err)
	}
	runService.Service = rundomain.NewService(
		db,
		runProjectAccess,
		publisher,
		rundomain.ServiceOptions{
			OperatorName: "test-operator",
			Readiness: rundomain.StartReadinessFunc(func(ctx context.Context, request rundomain.StartRequest) error {
				if runService.readiness == nil {
					return &rundomain.DomainError{Code: rundomain.ErrorNotReady, Detail: "Run readiness is not configured"}
				}
				return runService.readiness.CheckStartReadiness(ctx, request)
			}),
			Clock: func() time.Time { return runService.clock() },
		},
	)
	projectExecution, err := controlplane.NewProjectExecutionPort(runService.Service)
	if err != nil {
		t.Fatal(err)
	}
	projectService, err := project.NewService(db, nil, projectExecution, serviceOptions...)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := conn.ExecContext(t.Context(), `
		INSERT INTO dispatcher_lease (
			lease_name, instance_id, fencing_token, acquired_at, renewed_at,
			lease_expires_at, lease_expires_unix_nano
		) VALUES ('global', 'dispatcher-a', 1, ?, ?, ?, ?)`,
		now, now, expiresAt.Format(time.RFC3339Nano), expiresAt.UnixNano(),
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()

	return projectService, runService, func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}
}

func schedulingIsBootstrapIntent(intent project.Intent) bool {
	return intent.Description == project.BootstrapIntentDescription &&
		intent.Creator == project.BootstrapIntentCreator &&
		len(intent.From) == 1 && intent.From[0] == "origin"
}
