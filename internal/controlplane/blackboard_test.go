package controlplane

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestBlackboardInvalidationCommandsCoordinateProductionActiveRuns(t *testing.T) {
	t.Run("create Hint", func(t *testing.T) {
		application, lease := newBlackboardCoordinationAssembly(t)
		defer application.Shutdown()

		created, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
			Title: "create Hint coordination", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		bootstrapID := created.Intents[0].ID
		reasonRun := startProductionLifecycleRun(t, application, lease, created.Project, rundomain.TaskReason, nil)
		bootstrapRun := startProductionLifecycleRun(t, application, lease, created.Project, rundomain.TaskBootstrap, &bootstrapID)

		result, err := application.ProjectService.CreateHint(t.Context(), created.Project.ID, project.CreateHintRequest{
			Content: "new external evidence", ExpectedRevision: created.Project.Revision,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.ProjectRevision != 2 || result.AffectedRunCount != 2 || !result.Hint.IsActive() {
			t.Fatalf("Create Hint result = %+v", result)
		}
		assertProductionCancelledRun(t, application, reasonRun.Run.ID, rundomain.ErrorBlackboardChanged)
		assertProductionCancelledRun(t, application, bootstrapRun.Run.ID, rundomain.ErrorBlackboardChanged)
	})

	t.Run("withdraw Hint", func(t *testing.T) {
		application, lease := newBlackboardCoordinationAssembly(t)
		defer application.Shutdown()

		created, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
			Title: "withdraw Hint coordination", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
			Hints: []project.CreateHintInline{{Content: "temporary guidance"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		bootstrapID := created.Intents[0].ID
		reasonRun := startProductionLifecycleRun(t, application, lease, created.Project, rundomain.TaskReason, nil)
		bootstrapRun := startProductionLifecycleRun(t, application, lease, created.Project, rundomain.TaskBootstrap, &bootstrapID)

		result, err := application.ProjectService.WithdrawHint(t.Context(), created.Project.ID, "h001", project.WithdrawHintRequest{
			Reason: "guidance is no longer valid", ExpectedRevision: created.Project.Revision,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.ProjectRevision != 2 || result.AffectedRunCount != 2 || result.IsActive() {
			t.Fatalf("Withdraw Hint result = %+v", result)
		}
		assertProductionCancelledRun(t, application, reasonRun.Run.ID, rundomain.ErrorBlackboardChanged)
		assertProductionCancelledRun(t, application, bootstrapRun.Run.ID, rundomain.ErrorBlackboardChanged)
	})

	for _, test := range []struct {
		name    string
		command func(*testing.T, *testAssembly, project.ProjectDetail) (int64, int, error)
		check   func(*testing.T, *testAssembly, *rundomain.AdmittedRun, *rundomain.AdmittedRun)
	}{
		{
			name: "retract Fact",
			command: func(t *testing.T, application *testAssembly, current project.ProjectDetail) (int64, int, error) {
				result, err := application.ProjectService.RetractFact(t.Context(), current.Project.ID, "origin", project.RetractFactRequest{
					Reason: "origin evidence was invalidated", ExpectedRevision: current.Project.Revision,
				})
				if err != nil {
					return 0, 0, err
				}
				return result.ProjectRevision, result.AffectedRunCount, nil
			},
			check: func(t *testing.T, application *testAssembly, reasonRun, exploreRun *rundomain.AdmittedRun) {
				assertProductionCancelledRun(t, application, reasonRun.Run.ID, rundomain.ErrorFactRetracted)
				assertProductionCancelledRun(t, application, exploreRun.Run.ID, rundomain.ErrorFactRetracted)
			},
		},
		{
			name: "supersede Fact",
			command: func(t *testing.T, application *testAssembly, current project.ProjectDetail) (int64, int, error) {
				result, err := application.ProjectService.SupersedeFact(t.Context(), current.Project.ID, "origin", project.SupersedeFactRequest{
					Description: "corrected origin evidence", Reason: "origin evidence was corrected",
					ExpectedRevision: current.Project.Revision,
				})
				if err != nil {
					return 0, 0, err
				}
				return result.ProjectRevision, result.AffectedRunCount, nil
			},
			check: func(t *testing.T, application *testAssembly, reasonRun, exploreRun *rundomain.AdmittedRun) {
				assertProductionCancelledRun(t, application, reasonRun.Run.ID, rundomain.ErrorFactRetracted)
				assertProductionCancelledRun(t, application, exploreRun.Run.ID, rundomain.ErrorFactRetracted)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, lease := newBlackboardCoordinationAssembly(t)
			defer application.Shutdown()

			created, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
				Title: test.name + " coordination", Origin: "origin", Goal: "goal",
			})
			if err != nil {
				t.Fatal(err)
			}
			current, intent := createProductionExploreIntent(t, application, created)
			reasonRun := startProductionLifecycleRun(t, application, lease, current.Project, rundomain.TaskReason, nil)
			exploreRun := startProductionLifecycleRun(t, application, lease, current.Project, rundomain.TaskExplore, &intent.ID)

			projectRevision, affectedRunCount, err := test.command(t, application, current)
			if err != nil {
				t.Fatal(err)
			}
			if projectRevision != current.Project.Revision+1 || affectedRunCount != 2 {
				t.Fatalf("%s result revision=%d affected=%d", test.name, projectRevision, affectedRunCount)
			}
			test.check(t, application, reasonRun, exploreRun)
		})
	}

	t.Run("cancel Intent leaves unrelated Reason active", func(t *testing.T) {
		application, lease := newBlackboardCoordinationAssembly(t)
		defer application.Shutdown()

		created, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
			Title: "cancel Intent coordination", Origin: "origin", Goal: "goal",
		})
		if err != nil {
			t.Fatal(err)
		}
		current, intent := createProductionExploreIntent(t, application, created)
		reasonRun := startProductionLifecycleRun(t, application, lease, current.Project, rundomain.TaskReason, nil)
		exploreRun := startProductionLifecycleRun(t, application, lease, current.Project, rundomain.TaskExplore, &intent.ID)

		result, err := application.ProjectService.CancelIntent(t.Context(), current.Project.ID, intent.ID, project.CancelIntentRequest{
			Reason: "direction was withdrawn", ExpectedRevision: current.Project.Revision,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.ProjectRevision != current.Project.Revision+1 || result.AffectedRunCount != 1 || result.Status != project.IntentStatusCancelled {
			t.Fatalf("Cancel Intent result = %+v", result)
		}
		assertProductionCancelledRun(t, application, exploreRun.Run.ID, rundomain.ErrorIntentCancelled)
		reason, err := application.RunService.GetRun(t.Context(), reasonRun.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if reason.Status != rundomain.StatusActive {
			t.Fatalf("unaffected Reason status = %q, want %q", reason.Status, rundomain.StatusActive)
		}
		assertProductionClaimCount(t, application, reasonRun.Run.ID, 1)
	})
}

func TestBlackboardInvalidationRollbackThroughProductionCoordination(t *testing.T) {
	application, lease := newBlackboardCoordinationAssembly(t)
	defer application.Shutdown()

	created, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "blackboard rollback", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := startProductionLifecycleRun(t, application, lease, created.Project, rundomain.TaskReason, nil)

	conn, err := application.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(t.Context(), `
		CREATE TRIGGER reject_blackboard_change_cancellation
		BEFORE UPDATE OF status ON execution_runs
		WHEN NEW.status = 'cancelled' AND NEW.error_code = 'blackboard_changed'
		BEGIN
			SELECT RAISE(ABORT, 'reject blackboard change cancellation');
		END`)
	closeErr := conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}

	if _, err := application.ProjectService.CreateHint(t.Context(), created.Project.ID, project.CreateHintRequest{
		Content: "must roll back", ExpectedRevision: created.Project.Revision,
	}); err == nil {
		t.Fatal("expected Create Hint coordination failure")
	}
	current, err := application.ProjectService.Get(t.Context(), created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Project != created.Project || len(current.Hints) != 0 {
		t.Fatalf("Project after failed Create Hint = %+v", current)
	}
	stored, err := application.RunService.GetRun(t.Context(), run.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != rundomain.StatusActive || stored.ErrorCode != "" {
		t.Fatalf("Run after failed Create Hint = %+v", stored)
	}
	assertProductionClaimCount(t, application, run.Run.ID, 1)
	changes, err := application.ProjectService.ListChanges(t.Context(), created.Project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Changes) != 1 || changes.CurrentRevision != created.Project.Revision {
		t.Fatalf("History after failed Create Hint = %+v", changes)
	}
}

func newBlackboardCoordinationAssembly(t *testing.T) (*testAssembly, rundomain.DispatcherLease) {
	t.Helper()
	application, err := newTestAssembly(
		filepath.Join(t.TempDir(), "aether.db"),
		ControlPlaneHostOptions{OperatorName: "test-operator"},
	)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-blackboard", TTL: time.Minute,
	})
	if err != nil {
		application.Shutdown()
		t.Fatal(err)
	}
	return application, lease
}
