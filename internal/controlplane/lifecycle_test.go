package controlplane

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestProjectLifecycleCommandsCancelEveryActiveProjectRunThroughProductionCoordination(t *testing.T) {
	for _, test := range []struct {
		name          string
		cancel        func(*testing.T, *testAssembly, project.ProjectDetail) error
		wantProject   project.ProjectStatus
		wantErrorCode rundomain.ErrorCode
	}{
		{
			name: "pause",
			cancel: func(t *testing.T, application *testAssembly, target project.ProjectDetail) error {
				_, err := application.ProjectService.PauseProject(t.Context(), target.Project.ID, project.ProjectLifecycleRequest{
					ExpectedRevision: target.Project.Revision,
				})
				return err
			},
			wantProject: project.ProjectStatusPaused, wantErrorCode: rundomain.ErrorProjectPaused,
		},
		{
			name: "operator completion",
			cancel: func(t *testing.T, application *testAssembly, target project.ProjectDetail) error {
				_, err := application.ProjectService.CompleteProject(t.Context(), target.Project.ID, project.CompleteRequest{
					From: []string{"origin"}, Description: "operator confirmed completion",
					ExpectedRevision: target.Project.Revision,
				})
				return err
			},
			wantProject: project.ProjectStatusCompleted, wantErrorCode: rundomain.ErrorOperatorCompleted,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, err := newTestAssembly(
				filepath.Join(t.TempDir(), "aether.db"),
				ControlPlaneHostOptions{OperatorName: "test-operator"},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer application.Shutdown()

			lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
				InstanceID: "dispatcher-lifecycle", TTL: time.Minute,
			})
			if err != nil {
				t.Fatal(err)
			}
			target, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
				Title: "lifecycle target", Origin: "origin", Goal: "goal",
			})
			if err != nil {
				t.Fatal(err)
			}
			target, intent := createProductionExploreIntent(t, application, target)
			unrelated, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
				Title: "lifecycle unrelated", Origin: "origin", Goal: "goal",
			})
			if err != nil {
				t.Fatal(err)
			}

			targetReason := startProductionLifecycleRun(t, application, lease, target.Project, rundomain.TaskReason, nil)
			targetExplore := startProductionLifecycleRun(t, application, lease, target.Project, rundomain.TaskExplore, &intent.ID)
			unrelatedReason := startProductionLifecycleRun(t, application, lease, unrelated.Project, rundomain.TaskReason, nil)

			if err := test.cancel(t, application, target); err != nil {
				t.Fatal(err)
			}
			updated, err := application.ProjectService.Get(t.Context(), target.Project.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Project.Status != test.wantProject || updated.Project.Revision != target.Project.Revision+1 {
				t.Fatalf("Project after %s = %+v", test.name, updated.Project)
			}

			assertProductionCancelledRun(t, application, targetReason.Run.ID, test.wantErrorCode)
			assertProductionCancelledRun(t, application, targetExplore.Run.ID, test.wantErrorCode)
			unrelatedStored, err := application.RunService.GetRun(t.Context(), unrelatedReason.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if unrelatedStored.Status != rundomain.StatusActive {
				t.Fatalf("unrelated Run status = %q, want %q", unrelatedStored.Status, rundomain.StatusActive)
			}
			assertProductionClaimCount(t, application, unrelatedReason.Run.ID, 1)
		})
	}
}

func TestProjectPauseRollsBackProductionRunCoordinationFailure(t *testing.T) {
	application, err := newTestAssembly(
		filepath.Join(t.TempDir(), "aether.db"),
		ControlPlaneHostOptions{OperatorName: "test-operator"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()

	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-lifecycle-rollback", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "lifecycle rollback", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	activeRun := startProductionLifecycleRun(t, application, lease, target.Project, rundomain.TaskReason, nil)

	conn, err := application.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(t.Context(), `
		CREATE TRIGGER reject_project_pause_cancellation
		BEFORE UPDATE OF status ON execution_runs
		WHEN NEW.status = 'cancelled' AND NEW.error_code = 'project_paused'
		BEGIN
			SELECT RAISE(ABORT, 'reject project pause cancellation');
		END`)
	if closeErr := conn.Close(); err != nil {
		t.Fatal(err)
	} else if closeErr != nil {
		t.Fatal(closeErr)
	}

	if _, err := application.ProjectService.PauseProject(t.Context(), target.Project.ID, project.ProjectLifecycleRequest{
		ExpectedRevision: target.Project.Revision,
	}); err == nil {
		t.Fatal("expected Project pause failure")
	}
	updated, err := application.ProjectService.Get(t.Context(), target.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Project != target.Project {
		t.Fatalf("Project after failed pause = %+v, want %+v", updated.Project, target.Project)
	}
	stored, err := application.RunService.GetRun(t.Context(), activeRun.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != rundomain.StatusActive || stored.ErrorCode != "" {
		t.Fatalf("Run after failed pause = %+v", stored)
	}
	assertProductionClaimCount(t, application, activeRun.Run.ID, 1)
	changes, err := application.ProjectService.ListChanges(t.Context(), target.Project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Changes) != 1 {
		t.Fatalf("History after failed pause = %+v", changes)
	}
}

func createProductionExploreIntent(
	t *testing.T, application *testAssembly, created project.ProjectDetail,
) (project.ProjectDetail, project.Intent) {
	t.Helper()
	tx, err := application.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := application.ProjectService.ApplyRunResult(t.Context(), tx, project.ApplyRunResultCommand{
		RunID: "lifecycle-proposal-" + created.Project.ID, ProjectID: created.Project.ID,
		Task: project.RunTaskReason, Generation: created.Project.Generation, InputRevision: created.Project.Revision,
		Worker: "lifecycle-fixture", AppliedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Outcome: project.RunOutcome{Kind: project.RunOutcomeProposeIntents, Intents: []project.ProposedRunIntent{{
			From: []string{"origin"}, Description: "inspect evidence",
		}}},
	})
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	updated, err := application.ProjectService.Get(t.Context(), created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, intent := range updated.Intents {
		if len(result.CreatedIntentIDs) == 1 && intent.ID == result.CreatedIntentIDs[0] {
			return updated, intent
		}
	}
	t.Fatalf("created Intent %q not found in %+v", result.CreatedIntentIDs, updated.Intents)
	return project.ProjectDetail{}, project.Intent{}
}

func startProductionLifecycleRun(
	t *testing.T,
	application *testAssembly,
	lease rundomain.DispatcherLease,
	meta project.ProjectMeta,
	task rundomain.TaskType,
	targetIntentID *string,
) *rundomain.AdmittedRun {
	t.Helper()
	workKey, err := rundomain.WorkKeyFor(meta.ID, task, rundomain.InputSnapshot{
		Generation: meta.Generation, Revision: meta.Revision, TargetIntentID: targetIntentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	timeouts := rundomain.TimeoutSnapshot{RunSeconds: 60, PhaseSeconds: 10, GraceSeconds: 1}
	maxProposedIntents := 3
	if task != rundomain.TaskReason {
		timeouts.FallbackPhaseSeconds = 10
		maxProposedIntents = 0
	}
	started, err := application.RunService.StartRun(t.Context(), rundomain.StartRequest{
		ProjectID: meta.ID, Task: task, CandidateGeneration: meta.Generation, CandidateRevision: meta.Revision,
		TargetIntentID: targetIntentID, WorkKey: workKey, DispatcherID: lease.InstanceID,
		DispatcherFencingToken: lease.FencingToken,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: "lifecycle-v1", PromptTemplateHash: strings.Repeat("a", 64),
			Worker: rundomain.WorkerSnapshot{Name: fmt.Sprintf("lifecycle-%s", task), Adapter: "mock"},
			Image:  rundomain.ImageSnapshot{RequestedRef: "worker:v1"}, Timeouts: timeouts,
			ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: maxProposedIntents,
		},
		RetryPolicy: rundomain.DefaultRetryPolicy(),
	})
	if err != nil {
		t.Fatalf("start %s Run: %v", task, err)
	}
	return started
}

func assertProductionCancelledRun(
	t *testing.T, application *testAssembly, runID rundomain.RunID, wantCode rundomain.ErrorCode,
) {
	t.Helper()
	stored, err := application.RunService.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != rundomain.StatusCancelled || stored.ErrorCode != wantCode {
		t.Fatalf("cancelled Run = %+v, want status %q code %q", stored, rundomain.StatusCancelled, wantCode)
	}
	assertProductionClaimCount(t, application, runID, 0)
}

func assertProductionClaimCount(t *testing.T, application *testAssembly, runID rundomain.RunID, want int) {
	t.Helper()
	conn, err := application.DB.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var claims int
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM execution_claims WHERE run_id = ?", runID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != want {
		t.Fatalf("Run %s claims = %d, want %d", runID, claims, want)
	}
}
