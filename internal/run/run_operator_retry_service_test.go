package run_test

import (
	"fmt"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestDispatcherRecoveryKeepsTechnicalBudgetAcrossRepeatedRestarts(t *testing.T) {
	projects, runs, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projects.Create(t.Context(), CreateProjectRequest{Title: "restarts", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	runs.clock = func() time.Time { return now }
	request := reasonStartRequest(created.Project)
	request.RetryPolicy.MaxAttempts = 2
	first, err := runs.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: first.Run.ID, FencingToken: first.Run.FencingToken,
		Termination: &rundomain.RunTermination{Status: rundomain.StatusFailed, Code: rundomain.ExecutionProcessFailed},
	}); err != nil {
		t.Fatal(err)
	}
	previous := first.Run.ID
	for attempt := int64(2); attempt <= 6; attempt++ {
		now = now.Add(time.Minute)
		started, err := runs.StartRun(t.Context(), request)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if started.Run.Attempt != attempt || started.Run.PreviousRunID == nil || *started.Run.PreviousRunID != previous {
			t.Fatalf("retry lineage: %+v", started.Run)
		}
		recovered, err := runs.RecoverRuns(t.Context(), rundomain.RecoveryOptions{
			DispatcherID: request.DispatcherID, DispatcherFencingToken: request.DispatcherFencingToken, Reason: "dispatcher startup",
		})
		if err != nil || len(recovered) == 0 {
			t.Fatalf("recover: %v %v", recovered, err)
		}
		stored, err := runs.GetRun(t.Context(), started.Run.ID)
		if err != nil || stored.Status != rundomain.StatusInterrupted || stored.NextEligibleAt == nil ||
			!stored.NextEligibleAt.Equal(now.Add(rundomain.DefaultRetryPolicy().BackoffFor(request.WorkKey, 1))) {
			t.Fatalf("interruption changed retry delay: %+v %v", stored, err)
		}
		previous = started.Run.ID
	}
	now = now.Add(time.Minute)
	last, err := runs.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: last.Run.ID, FencingToken: last.Run.FencingToken,
		Termination: &rundomain.RunTermination{Status: rundomain.StatusFailed, Code: rundomain.ExecutionDeadline},
	}); err != nil {
		t.Fatal(err)
	}
	assertRunErrorCode(t, rundomain.ErrorRetryExhausted, func() error { _, err := runs.StartRun(t.Context(), request); return err })
}

func TestOperatorRetryRenewsBudgetPreservesEvidenceAndReplaysWithoutResettingLaterFailure(t *testing.T) {
	projects, runs, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projects.Create(t.Context(), CreateProjectRequest{Title: "operator retry", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(created.Project)
	request.RetryPolicy.MaxAttempts = 1
	failed := failRetryRun(t, runs, request)
	command := rundomain.RetryExhaustedRequest{RequestID: "retry-command-1", ExpectedRevision: created.Project.Revision}
	receipt, err := runs.RetryExhaustedWork(t.Context(), created.Project.ID, command)
	if err != nil || len(receipt.RunIDs) != 1 || receipt.RunIDs[0] != failed.ID || receipt.Replayed {
		t.Fatalf("retry receipt: %+v %v", receipt, err)
	}
	next := failRetryRun(t, runs, request)
	if next.Attempt != 2 || next.PreviousRunID == nil || *next.PreviousRunID != failed.ID || next.Spec.ConfigRevision != failed.Spec.ConfigRevision {
		t.Fatalf("retry lost execution history: %+v", next)
	}
	replayed, err := runs.RetryExhaustedWork(t.Context(), created.Project.ID, command)
	if err != nil || !replayed.Replayed || len(replayed.RunIDs) != 1 || replayed.RunIDs[0] != failed.ID {
		t.Fatalf("replay: %+v %v", replayed, err)
	}
	assertRunErrorCode(t, rundomain.ErrorRetryExhausted, func() error { _, err := runs.StartRun(t.Context(), request); return err })
	detail, err := runs.GetProjectActivityRunDetail(t.Context(), created.Project.ID, failed.ID)
	if err != nil || detail.Status != rundomain.StatusFailed || len(detail.OperatorRetries) != 1 ||
		detail.OperatorRetries[0].OperatorName != "test-operator" || detail.OperatorRetries[0].PreviousFailureCount != 1 {
		t.Fatalf("operator evidence: %+v %v", detail, err)
	}
	command.RequestID = "retry-command-2"
	if receipt, err := runs.RetryExhaustedWork(t.Context(), created.Project.ID, command); err != nil || len(receipt.RunIDs) != 1 || receipt.RunIDs[0] != next.ID {
		t.Fatalf("new retry: %+v %v", receipt, err)
	}
	current, err := projects.Get(t.Context(), created.Project.ID)
	if err != nil || current.Project.Revision != created.Project.Revision || len(current.Facts) != 2 {
		t.Fatalf("retry changed Blackboard: %+v %v", current, err)
	}
}

func TestOperatorRetryRejectsInactiveAndStaleProjectsAndSkipsObsoleteReason(t *testing.T) {
	projects, runs, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projects.Create(t.Context(), CreateProjectRequest{Title: "eligibility", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	request := reasonStartRequest(created.Project)
	request.RetryPolicy.MaxAttempts = 1
	failRetryRun(t, runs, request)
	paused, err := projects.PauseProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{ExpectedRevision: created.Project.Revision})
	if err != nil {
		t.Fatal(err)
	}
	command := rundomain.RetryExhaustedRequest{RequestID: "inactive", ExpectedRevision: paused.Revision}
	assertRunErrorCode(t, rundomain.ErrorProjectInactive, func() error { _, err := runs.RetryExhaustedWork(t.Context(), created.Project.ID, command); return err })
	resumed, err := projects.ResumeProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{ExpectedRevision: paused.Revision})
	if err != nil {
		t.Fatal(err)
	}
	command.RequestID = "stale"
	assertRunErrorCode(t, rundomain.ErrorConflict, func() error { _, err := runs.RetryExhaustedWork(t.Context(), created.Project.ID, command); return err })
	command.RequestID, command.ExpectedRevision = "current", resumed.Revision
	receipt, err := runs.RetryExhaustedWork(t.Context(), created.Project.ID, command)
	if err != nil || len(receipt.RunIDs) != 0 {
		t.Fatalf("obsolete Reason was revived: %+v %v", receipt, err)
	}
}

func TestBootstrapHintChangesReplaceInputWithoutClosingWork(t *testing.T) {
	for _, withdraw := range []bool{false, true} {
		t.Run(fmt.Sprintf("withdraw=%t", withdraw), func(t *testing.T) {
			projects, runs, cleanup := newRunTestServices(t)
			defer cleanup()
			created, err := projects.Create(t.Context(), CreateProjectRequest{Title: "bootstrap hint", Origin: "origin", Goal: "goal", BootstrapEnabled: true, Hints: []CreateHintInline{{Content: "old guidance"}}})
			if err != nil {
				t.Fatal(err)
			}
			request := bootstrapStartRequest(created.Project, created.Intents[0])
			first, err := runs.StartRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if withdraw {
				_, err = projects.WithdrawHint(t.Context(), created.Project.ID, created.Hints[0].ID, WithdrawHintRequest{ExpectedRevision: created.Project.Revision, Reason: "obsolete"})
			} else {
				_, err = projects.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{ExpectedRevision: created.Project.Revision, Content: "new guidance"})
			}
			if err != nil {
				t.Fatal(err)
			}
			updated, err := projects.Get(t.Context(), created.Project.ID)
			if err != nil {
				t.Fatal(err)
			}
			request = bootstrapStartRequest(updated.Project, updated.Intents[0])
			second, err := runs.StartRun(t.Context(), request)
			if err != nil {
				t.Fatalf("Bootstrap cannot continue after Hint change: %v", err)
			}
			if second.Run.WorkKey != first.Run.WorkKey || second.Run.Attempt != 2 || second.Run.Spec.Input.Revision != updated.Project.Revision || second.Run.Spec.Input.Identity == first.Run.Spec.Input.Identity {
				t.Fatalf("Bootstrap didn't re-admit latest input: %+v", second.Run)
			}
		})
	}
}

func bootstrapStartRequest(project ProjectMeta, intent Intent) rundomain.StartRequest {
	request := reasonStartRequest(project)
	setStartRequestTarget(&request, rundomain.TaskBootstrap, intent.ID)
	request.Spec.MaxProposedIntents = 0
	request.Spec.Timeouts.FallbackPhaseSeconds = request.Spec.Timeouts.PhaseSeconds
	request.Spec.Timeouts.RunSeconds = 3 * (request.Spec.Timeouts.PhaseSeconds + request.Spec.Timeouts.GraceSeconds)
	return request
}

func TestOperatorRetryIntentWorkAdmitsLatestHints(t *testing.T) {
	for _, task := range []rundomain.TaskType{rundomain.TaskBootstrap, rundomain.TaskExplore} {
		t.Run(string(task), func(t *testing.T) {
			projects, runs, created, intent, first, cleanup := startIntentRun(t, task)
			defer cleanup()
			now := time.Now().UTC()
			runs.clock = func() time.Time { return now }
			last := first
			for attempt := 1; attempt <= 3; attempt++ {
				if _, err := runs.FinishRun(t.Context(), rundomain.FinishRequest{RunID: last.Run.ID, FencingToken: last.Run.FencingToken, Termination: &rundomain.RunTermination{Status: rundomain.StatusFailed, Code: rundomain.ExecutionProcessFailed}}); err != nil {
					t.Fatal(err)
				}
				if attempt < 3 {
					now = now.Add(10 * time.Minute)
					request := reasonStartRequest(created.Project)
					setStartRequestTarget(&request, task, intent.ID)
					request.Spec.MaxProposedIntents = 0
					request.Spec.Timeouts = first.Run.Spec.Timeouts
					var err error
					last, err = runs.StartRun(t.Context(), request)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := projects.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{ExpectedRevision: created.Project.Revision, Content: "guidance after exhaustion"}); err != nil {
				t.Fatal(err)
			}
			updated, err := projects.Get(t.Context(), created.Project.ID)
			if err != nil {
				t.Fatal(err)
			}
			command := rundomain.RetryExhaustedRequest{RequestID: "retry-intent", ExpectedRevision: updated.Project.Revision}
			receipt, err := runs.RetryExhaustedWork(t.Context(), created.Project.ID, command)
			if err != nil || len(receipt.RunIDs) != 1 || receipt.RunIDs[0] != last.Run.ID {
				t.Fatalf("valid Intent work was skipped: %+v %v", receipt, err)
			}
			request := reasonStartRequest(updated.Project)
			setStartRequestTarget(&request, task, intent.ID)
			request.Spec.MaxProposedIntents = 0
			request.Spec.Timeouts = first.Run.Spec.Timeouts
			next, err := runs.StartRun(t.Context(), request)
			if err != nil || next.Run.Spec.Input.Revision != updated.Project.Revision || next.Run.Spec.Input.Identity == first.Run.Spec.Input.Identity {
				t.Fatalf("new input not admitted: %+v %v", next, err)
			}
		})
	}
}

func TestRecoveryResumesCleanupOfCancelledRunBeforeReplacingBootstrap(t *testing.T) {
	projects, runs, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projects.Create(t.Context(), CreateProjectRequest{Title: "cleanup after Hint", Origin: "origin", Goal: "goal", BootstrapEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	request := bootstrapStartRequest(created.Project, created.Intents[0])
	first, err := runs.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	handle := rundomain.ExecutionHandle{RunID: first.Run.ID, ProjectID: created.Project.ID, Phase: rundomain.PhaseBootstrap, ContainerID: "container", ExecID: "exec", StartedAt: time.Now().UTC()}
	if err := runs.RecordExecutionHandle(t.Context(), rundomain.ExecutionHandleRecordRequest{Handle: handle, RunFencingToken: first.Run.FencingToken, DispatcherID: request.DispatcherID, DispatcherFencingToken: request.DispatcherFencingToken}); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{ExpectedRevision: created.Project.Revision, Content: "latest guidance"}); err != nil {
		t.Fatal(err)
	}
	updated, err := projects.Get(t.Context(), created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	request = bootstrapStartRequest(updated.Project, updated.Intents[0])
	assertRunErrorCode(t, rundomain.ErrorRetryDeferred, func() error { _, err := runs.StartRun(t.Context(), request); return err })
	recovered, err := runs.RecoverRuns(t.Context(), rundomain.RecoveryOptions{DispatcherID: request.DispatcherID, DispatcherFencingToken: request.DispatcherFencingToken, Reason: "restart while cancelling"})
	if err != nil || len(recovered) != 1 || recovered[0] != first.Run.ID {
		t.Fatalf("cancelled Exec cleanup lost: %+v %v", recovered, err)
	}
	record := rundomain.RecoveryRecord{RunID: first.Run.ID, DispatcherID: request.DispatcherID, DispatcherFencingToken: request.DispatcherFencingToken, Status: rundomain.RecoveryDisposed, Reason: "cleanup"}
	assertRunErrorCode(t, rundomain.ErrorConflict, func() error { return runs.RecordRecoveryEvent(t.Context(), record) })
	if err := runs.MarkExecutionHandleStopped(t.Context(), rundomain.ExecutionHandleStopRequest{Handle: handle, DispatcherID: request.DispatcherID, DispatcherFencingToken: request.DispatcherFencingToken}); err != nil {
		t.Fatal(err)
	}
	if err := runs.RecordRecoveryEvent(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.StartRun(t.Context(), request); err != nil {
		t.Fatalf("replacement after cleanup: %v", err)
	}
	stored, err := runs.GetRun(t.Context(), first.Run.ID)
	if err != nil || stored.Status != rundomain.StatusCancelled || stored.ErrorCode != rundomain.ErrorBlackboardChanged {
		t.Fatalf("recovery rewrote cancellation evidence: %+v %v", stored, err)
	}
}

func failRetryRun(t *testing.T, runs *runTestService, request rundomain.StartRequest) rundomain.ExecutionRun {
	t.Helper()
	started, err := runs.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.FinishRun(t.Context(), rundomain.FinishRequest{RunID: started.Run.ID, FencingToken: started.Run.FencingToken, Termination: &rundomain.RunTermination{Status: rundomain.StatusFailed, Code: rundomain.ExecutionProcessFailed}}); err != nil {
		t.Fatal(err)
	}
	return started.Run
}
