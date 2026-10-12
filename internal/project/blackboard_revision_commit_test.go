package project

import (
	"errors"
	"reflect"
	"testing"
)

func TestProjectCommandReceiptsMatchCurrentStateAndReplayAcrossGenerations(t *testing.T) {
	service, _, publisher, _ := newHintRevisionCommitTestService(t)
	before := mustCreateHintTestProject(t, service)
	steps := []struct {
		name       string
		revision   int64
		generation int
		status     ProjectStatus
		command    func(ProjectDetail) (ProjectMeta, error)
	}{
		{"rename", 2, 1, ProjectStatusActive, func(detail ProjectDetail) (ProjectMeta, error) {
			receipt, err := service.UpdateTitle(t.Context(), detail.Project.ID, UpdateProjectTitleRequest{
				Title: "renamed Project", ExpectedRevision: detail.Project.Revision,
			})
			return receipt.Project, err
		}},
		{"pause", 3, 1, ProjectStatusPaused, func(detail ProjectDetail) (ProjectMeta, error) {
			return service.PauseProject(t.Context(), detail.Project.ID, ProjectLifecycleRequest{
				ExpectedRevision: detail.Project.Revision,
			})
		}},
		{"resume", 4, 1, ProjectStatusActive, func(detail ProjectDetail) (ProjectMeta, error) {
			return service.ResumeProject(t.Context(), detail.Project.ID, ProjectLifecycleRequest{
				ExpectedRevision: detail.Project.Revision,
			})
		}},
		{"complete", 5, 1, ProjectStatusCompleted, func(detail ProjectDetail) (ProjectMeta, error) {
			receipt, err := service.CompleteProject(t.Context(), detail.Project.ID, CompleteRequest{
				From: []string{"origin"}, Description: "Goal reached", ExpectedRevision: detail.Project.Revision,
			})
			if err != nil {
				return ProjectMeta{}, err
			}
			return receipt.Project, nil
		}},
		{"reopen", 6, 2, ProjectStatusActive, func(detail ProjectDetail) (ProjectMeta, error) {
			receipt, err := service.ReopenProject(t.Context(), detail.Project.ID, ReopenRequest{
				Feedback: "check the next Generation", ExpectedRevision: detail.Project.Revision,
			})
			if err != nil {
				return ProjectMeta{}, err
			}
			return receipt.Project, nil
		}},
	}
	for _, step := range steps {
		// These commands form one lifecycle; stop if a step fails so later
		// assertions always describe a reachable Project state.
		meta, err := step.command(before)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if meta.Revision != step.revision || meta.Generation != step.generation ||
			meta.Status != step.status || meta.Title != "renamed Project" {
			t.Fatalf("%s receipt = %+v", step.name, meta)
		}
		after, err := service.Get(t.Context(), meta.ID)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := service.GetBlackboardRevision(t.Context(), meta.ID, step.revision)
		if err != nil {
			t.Fatal(err)
		}
		if meta != after.Project || !reflect.DeepEqual(replay, after) {
			t.Fatalf("%s state disagrees: receipt=%+v current=%+v replay=%+v", step.name, meta, after, replay)
		}
		history := readHistoryRevisionForTest(t, service, meta.ID, step.revision)
		transition, ok := history.Evidence.(HistoryTransitionEvidence)
		if !ok || transition.Before == nil || transition.Before.Project != before.Project ||
			transition.After.Project != after.Project {
			t.Fatalf("%s History disagrees with the committed transition: %+v", step.name, history)
		}
		before = after
	}

	receipt, err := service.DeleteConfirmed(t.Context(), before.Project.ID, DeleteProjectRequest{
		ExpectedTitle: before.Project.Title, ExpectedRevision: before.Project.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ProjectRevision != 6 || len(publisher.deletions) != 1 ||
		publisher.deletions[0].revision != 6 || len(publisher.observations) != 6 {
		t.Fatalf("Delete must publish the last Revision without a Change: receipt=%+v publisher=%+v", receipt, publisher)
	}
}

func TestCreateProjectPublishesOnlyCommittedRevision(t *testing.T) {
	service, _, publisher, execution := newHintRevisionCommitTestService(t)
	execution.baselineParentID = "valid-parent"

	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Committed create", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Project.Revision != 1 || len(created.Intents) != 1 {
		t.Fatalf("Create Project result = %+v", created)
	}
	assertProjectRevisionPublication(t, publisher, 0, 1, ChangeProjectCreated, ProjectStatusActive)
}

func TestCreateProjectCommitFailureRollsBackStateHistoryAndPublication(t *testing.T) {
	service, _, publisher, execution := newHintRevisionCommitTestService(t)
	execution.baselineParentID = "missing-parent"

	_, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Rolled back create", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err == nil {
		t.Fatal("expected deferred foreign-key failure at commit")
	}
	assertNoProjectCommandState(t, service, publisher)
}

func TestDeleteProjectCommitsCancellationCleanupAndTerminalPublicationTogether(t *testing.T) {
	service, _, publisher, execution := newHintRevisionCommitTestService(t)
	created := mustCreateHintTestProject(t, service)
	publisher.observations = nil
	execution.count = 3

	receipt, err := service.DeleteConfirmed(t.Context(), created.Project.ID, DeleteProjectRequest{
		ExpectedTitle: created.Project.Title, ExpectedRevision: created.Project.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ProjectID != created.Project.ID || receipt.ProjectRevision != 1 ||
		receipt.AffectedRunCount != 3 || receipt.CleanupJobID != "cleanup-1" {
		t.Fatalf("Delete Project receipt = %+v", receipt)
	}
	if _, err := service.Get(t.Context(), created.Project.ID); !IsProjectNotFound(err) {
		t.Fatalf("deleted Project lookup error = %v", err)
	}
	if len(execution.calls) != 1 || execution.calls[0].reason != CancellationProjectDeleted ||
		!isProjectWideRunCancellation(execution.calls[0].selector) {
		t.Fatalf("Delete Project cancellation = %+v", execution.calls)
	}
	if len(execution.cleanupCalls) != 1 || execution.cleanupCalls[0].projectID != created.Project.ID ||
		execution.cleanupCalls[0].projectExists != 1 {
		t.Fatalf("Delete Project cleanup = %+v", execution.cleanupCalls)
	}
	if !reflect.DeepEqual(execution.events, []string{"cancel", "cleanup"}) {
		t.Fatalf("Delete Project coordination order = %v", execution.events)
	}
	if len(publisher.deletions) != 1 {
		t.Fatalf("Delete Project publications = %+v", publisher.deletions)
	}
	deletion := publisher.deletions[0]
	if deletion.err != nil || deletion.projectID != created.Project.ID || deletion.revision != 1 ||
		deletion.exists != 0 || deletion.cleanup != 1 {
		t.Fatalf("Delete Project publication observation = %+v", deletion)
	}
}

func TestDeleteProjectCancellationFailureRollsBackAndSuppressesCleanup(t *testing.T) {
	service, _, publisher, execution := newHintRevisionCommitTestService(t)
	created := mustCreateHintTestProject(t, service)
	publisher.observations = nil
	execution.err = errors.New("cancel active Runs")

	_, err := service.DeleteConfirmed(t.Context(), created.Project.ID, DeleteProjectRequest{
		ExpectedTitle: created.Project.Title, ExpectedRevision: created.Project.Revision,
	})
	if err == nil || err.Error() != "cancel active Runs" {
		t.Fatalf("Delete Project cancellation error = %v", err)
	}
	if len(execution.calls) != 1 || len(execution.cleanupCalls) != 0 {
		t.Fatalf("Delete Project failed coordination = calls=%+v cleanup=%+v", execution.calls, execution.cleanupCalls)
	}
	detail, getErr := service.Get(t.Context(), created.Project.ID)
	if getErr != nil || detail.Project != created.Project {
		t.Fatalf("failed Delete Project state = %+v error=%v", detail.Project, getErr)
	}
	if len(publisher.deletions) != 0 {
		t.Fatalf("failed Delete Project published = %+v", publisher.deletions)
	}
	assertNoProjectCommandParticipants(t, service)
}

func TestDeleteProjectCommitFailureRollsBackEveryParticipantAndSuppressesPublication(t *testing.T) {
	service, _, publisher, execution := newHintRevisionCommitTestService(t)
	created := mustCreateHintTestProject(t, service)
	publisher.observations = nil
	execution.cleanupParentID = "missing-parent"

	_, err := service.DeleteConfirmed(t.Context(), created.Project.ID, DeleteProjectRequest{
		ExpectedTitle: created.Project.Title, ExpectedRevision: created.Project.Revision,
	})
	if err == nil {
		t.Fatal("expected deferred foreign-key failure at commit")
	}
	detail, getErr := service.Get(t.Context(), created.Project.ID)
	if getErr != nil || detail.Project.Revision != 1 || detail.Project.Title != created.Project.Title {
		t.Fatalf("failed Delete Project state = %+v error=%v", detail, getErr)
	}
	history := readHistoryPageForTest(t, service, created.Project.ID, HistoryPageQuery{Limit: 100})
	if len(history.Revisions) != 1 || history.Revisions[0].Kind != ChangeProjectCreated {
		t.Fatalf("failed Delete Project History = %+v", history)
	}
	if len(publisher.deletions) != 0 {
		t.Fatalf("failed Delete Project published %+v", publisher.deletions)
	}
	assertNoProjectCommandParticipants(t, service)
}

func isProjectWideRunCancellation(selector RunCancellationSelector) bool {
	return len(selector.Clauses) == 1 && len(selector.Clauses[0].Tasks) == 0 &&
		selector.Clauses[0].TargetIntentID == nil && selector.Clauses[0].SourceFactID == nil &&
		selector.Clauses[0].InputRevisionAtMost == nil
}

func TestProjectIdentityAndLifecycleCommandsRejectStaleRevisionWithoutPublication(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *Service, ProjectDetail) ProjectDetail
		command func(*testing.T, *Service, ProjectDetail) error
	}{
		{
			name: "rename",
			command: func(t *testing.T, service *Service, created ProjectDetail) error {
				_, err := service.UpdateTitle(t.Context(), created.Project.ID, UpdateProjectTitleRequest{
					Title: "stale rename", ExpectedRevision: created.Project.Revision - 1,
				})
				return err
			},
		},
		{
			name: "pause",
			command: func(t *testing.T, service *Service, created ProjectDetail) error {
				_, err := service.PauseProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{
					ExpectedRevision: created.Project.Revision - 1,
				})
				return err
			},
		},
		{
			name: "resume",
			prepare: func(t *testing.T, service *Service, created ProjectDetail) ProjectDetail {
				if _, err := service.PauseProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{
					ExpectedRevision: created.Project.Revision,
				}); err != nil {
					t.Fatal(err)
				}
				updated, err := service.Get(t.Context(), created.Project.ID)
				if err != nil {
					t.Fatal(err)
				}
				return updated
			},
			command: func(t *testing.T, service *Service, created ProjectDetail) error {
				_, err := service.ResumeProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{
					ExpectedRevision: created.Project.Revision - 1,
				})
				return err
			},
		},
		{
			name: "complete",
			command: func(t *testing.T, service *Service, created ProjectDetail) error {
				_, err := service.CompleteProject(t.Context(), created.Project.ID, CompleteRequest{
					From: []string{"origin"}, Description: "stale completion",
					ExpectedRevision: created.Project.Revision - 1,
				})
				return err
			},
		},
		{
			name: "reopen",
			prepare: func(t *testing.T, service *Service, created ProjectDetail) ProjectDetail {
				if _, err := service.CompleteProject(t.Context(), created.Project.ID, CompleteRequest{
					From: []string{"origin"}, Description: "completed",
					ExpectedRevision: created.Project.Revision,
				}); err != nil {
					t.Fatal(err)
				}
				updated, err := service.Get(t.Context(), created.Project.ID)
				if err != nil {
					t.Fatal(err)
				}
				return updated
			},
			command: func(t *testing.T, service *Service, created ProjectDetail) error {
				_, err := service.ReopenProject(t.Context(), created.Project.ID, ReopenRequest{
					Feedback: "stale reopen", ExpectedRevision: created.Project.Revision - 1,
				})
				return err
			},
		},
		{
			name: "delete",
			command: func(t *testing.T, service *Service, created ProjectDetail) error {
				_, err := service.DeleteConfirmed(t.Context(), created.Project.ID, DeleteProjectRequest{
					ExpectedTitle: created.Project.Title, ExpectedRevision: created.Project.Revision - 1,
				})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _, publisher, execution := newHintRevisionCommitTestService(t)
			created := mustCreateHintTestProject(t, service)
			if test.prepare != nil {
				created = test.prepare(t, service, created)
			}
			publisher.observations = nil
			publisher.deletions = nil
			execution.calls = nil

			err := test.command(t, service, created)
			assertRevisionConflict(t, err, created.Project.Revision)
			after, getErr := service.Get(t.Context(), created.Project.ID)
			if getErr != nil || after.Project != created.Project {
				t.Fatalf("stale %s changed Project: before=%+v after=%+v error=%v", test.name, created.Project, after.Project, getErr)
			}
			if len(publisher.observations) != 0 || len(publisher.deletions) != 0 || len(execution.calls) != 0 {
				t.Fatalf("stale %s leaked effects: changes=%+v deletions=%+v cancellations=%+v",
					test.name, publisher.observations, publisher.deletions, execution.calls)
			}
		})
	}
}

func TestPauseProjectCommitsLifecycleCancellationHistoryAndPublicationTogether(t *testing.T) {
	service, _, publisher, execution := newHintRevisionCommitTestService(t)
	created := mustCreateHintTestProject(t, service)
	publisher.observations = nil
	execution.count = 2

	paused, err := service.PauseProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{
		ExpectedRevision: created.Project.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != ProjectStatusPaused || paused.Revision != 2 {
		t.Fatalf("Pause Project receipt = %+v", paused)
	}
	if len(execution.calls) != 1 {
		t.Fatalf("Pause Project cancellation calls = %+v", execution.calls)
	}
	call := execution.calls[0]
	if call.projectID != created.Project.ID || call.revision != 2 ||
		call.reason != CancellationProjectPaused || !isProjectWideRunCancellation(call.selector) {
		t.Fatalf("Pause Project cancellation = %+v", call)
	}
	assertProjectRevisionPublication(t, publisher, 0, 2, ChangeProjectPaused, ProjectStatusPaused)
	assertHistoryResult(t, service, created.Project.ID, 2, nil)
}

func TestCompleteProjectCommitsLifecycleCancellationHistoryAndPublicationTogether(t *testing.T) {
	service, _, publisher, execution := newHintRevisionCommitTestService(t)
	created := mustCreateHintTestProject(t, service)
	publisher.observations = nil
	execution.count = 2

	completed, err := service.CompleteProject(t.Context(), created.Project.ID, CompleteRequest{
		From: []string{"origin"}, Description: "operator confirmed completion",
		ExpectedRevision: created.Project.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Project.Status != ProjectStatusCompleted || completed.Project.Revision != 2 ||
		completed.ProjectRevision != 2 || completed.AffectedRunCount != 2 {
		t.Fatalf("Complete Project receipt = %+v", completed)
	}
	if len(execution.calls) != 1 {
		t.Fatalf("Complete Project cancellation calls = %+v", execution.calls)
	}
	call := execution.calls[0]
	if call.projectID != created.Project.ID || call.revision != 2 ||
		call.reason != CancellationOperatorCompleted || !isProjectWideRunCancellation(call.selector) {
		t.Fatalf("Complete Project cancellation = %+v", call)
	}
	assertProjectRevisionPublication(t, publisher, 0, 2, ChangeProjectCompleted, ProjectStatusCompleted)
	assertHistoryResult(t, service, created.Project.ID, 2, map[string]any{
		"completion_intent_id": completed.Intent.ID, "affected_run_count": float64(2),
	})
}

func TestCompleteProjectRollsBackEveryParticipantAndSuppressesPublication(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*transactionalHintExecutionPort)
	}{
		{name: "cancellation", configure: func(port *transactionalHintExecutionPort) {
			port.err = errors.New("cancel active Runs")
		}},
		{name: "commit", configure: func(port *transactionalHintExecutionPort) {
			port.parentID = "missing-parent"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, publisher, execution := newHintRevisionCommitTestService(t)
			created := mustCreateHintTestProject(t, service)
			publisher.observations = nil
			test.configure(execution)

			_, err := service.CompleteProject(t.Context(), created.Project.ID, CompleteRequest{
				From: []string{"origin"}, Description: "must roll back",
				ExpectedRevision: created.Project.Revision,
			})
			if err == nil {
				t.Fatal("expected Complete Project failure")
			}
			assertProjectRevisionRolledBack(t, service, created.Project.ID, publisher, execution)
		})
	}
}

func TestPauseProjectRollsBackEveryParticipantAndSuppressesPublication(t *testing.T) {
	service, _, publisher, execution := newHintRevisionCommitTestService(t)
	created := mustCreateHintTestProject(t, service)
	publisher.observations = nil
	execution.parentID = "missing-parent"

	_, err := service.PauseProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{
		ExpectedRevision: created.Project.Revision,
	})
	if err == nil {
		t.Fatal("expected deferred foreign-key failure at commit")
	}
	assertProjectRevisionRolledBack(t, service, created.Project.ID, publisher, execution)
}

func TestPauseProjectCancellationFailureRollsBackAndSuppressesPublication(t *testing.T) {
	service, _, publisher, execution := newHintRevisionCommitTestService(t)
	created := mustCreateHintTestProject(t, service)
	publisher.observations = nil
	execution.err = errors.New("cancel active Runs")

	_, err := service.PauseProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{
		ExpectedRevision: created.Project.Revision,
	})
	if err == nil || err.Error() != "cancel active Runs" {
		t.Fatalf("Pause Project cancellation error = %v", err)
	}
	assertProjectRevisionRolledBack(t, service, created.Project.ID, publisher, execution)
}

func assertProjectRevisionPublication(
	t *testing.T,
	publisher *observingProjectPublisher,
	index int,
	revision int64,
	kind ChangeKind,
	status ProjectStatus,
) {
	t.Helper()
	if len(publisher.observations) <= index {
		t.Fatalf("missing publication %d: %+v", index, publisher.observations)
	}
	observation := publisher.observations[index]
	if observation.err != nil || observation.change.Revision != revision ||
		observation.change.Kind != kind || observation.change.ChangeHash == "" ||
		observation.revision != revision {
		t.Fatalf("publication observation %d = %+v", index, observation)
	}
	history, err := decodeHistoryDelta(observation.change.Delta)
	if err != nil {
		t.Fatal(err)
	}
	if history.Project == nil || history.Project.AfterFields["status"] != string(status) {
		t.Fatalf("publication Project transition = %+v", history.Project)
	}
}

func assertProjectRevisionRolledBack(
	t *testing.T,
	service *Service,
	projectID string,
	publisher *observingProjectPublisher,
	execution *transactionalHintExecutionPort,
) {
	t.Helper()
	detail, err := service.Get(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Status != ProjectStatusActive || detail.Project.Revision != 1 {
		t.Fatalf("failed lifecycle command leaked Project state = %+v", detail.Project)
	}
	history := readHistoryPageForTest(t, service, projectID, HistoryPageQuery{Limit: 100})
	if len(history.Revisions) != 1 || history.Revisions[0].Kind != ChangeProjectCreated {
		t.Fatalf("failed lifecycle command leaked History = %+v", history)
	}
	if len(publisher.observations) != 0 {
		t.Fatalf("failed lifecycle command published %+v", publisher.observations)
	}
	conn, err := service.db.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var cancellations int
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM test_hint_run_cancellations").Scan(&cancellations); err != nil {
		t.Fatal(err)
	}
	if cancellations != 0 {
		t.Fatalf("failed lifecycle command retained %d Run cancellations; calls=%+v", cancellations, execution.calls)
	}
}

func assertNoProjectCommandState(
	t *testing.T, service *Service, publisher *observingProjectPublisher,
) {
	t.Helper()
	conn, err := service.db.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var projects, history, participants int
	if err := conn.QueryRowContext(t.Context(), `
		SELECT (SELECT COUNT(*) FROM projects),
		       (SELECT COUNT(*) FROM blackboard_changes),
		       (SELECT COUNT(*) FROM test_project_command_participants)`).
		Scan(&projects, &history, &participants); err != nil {
		t.Fatal(err)
	}
	if projects != 0 || history != 0 || participants != 0 {
		t.Fatalf("failed command state: projects=%d history=%d participants=%d", projects, history, participants)
	}
	if len(publisher.observations) != 0 || len(publisher.deletions) != 0 {
		t.Fatalf("failed command publications: changes=%+v deletions=%+v", publisher.observations, publisher.deletions)
	}
}

func assertNoProjectCommandParticipants(t *testing.T, service *Service) {
	t.Helper()
	conn, err := service.db.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var participants, cancellations int
	if err := conn.QueryRowContext(t.Context(), `
		SELECT (SELECT COUNT(*) FROM test_project_command_participants),
		       (SELECT COUNT(*) FROM test_hint_run_cancellations)`).
		Scan(&participants, &cancellations); err != nil {
		t.Fatal(err)
	}
	if participants != 0 || cancellations != 0 {
		t.Fatalf("failed command participants: cleanup=%d cancellations=%d", participants, cancellations)
	}
}

func assertRevisionConflict(t *testing.T, err error, revision int64) {
	t.Helper()
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != ErrorConflict ||
		domainErr.CurrentRevision == nil || *domainErr.CurrentRevision != revision {
		t.Fatalf("Revision conflict = %+v, want current Revision %d", err, revision)
	}
}
