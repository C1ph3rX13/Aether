package project

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/database"
)

func TestProjectServiceOwnsLifecycleAndBlackboardTransactions(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	execution := &projectExecutionTestAdapter{}
	service, err := NewService(db, nil, execution)
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Project core", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Project.Generation != 1 || created.Project.Revision != 1 || len(created.Intents) != 1 {
		t.Fatalf("created project: %+v", created)
	}
	if len(execution.baselineCalls) != 1 || execution.baselineCalls[0].revision != 1 {
		t.Fatalf("reason baseline: %+v", execution.baselineCalls)
	}

	if _, err := service.PauseProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResumeProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	completed, err := service.CompleteProject(t.Context(), created.Project.ID, CompleteRequest{
		From: []string{"origin"}, Description: "goal reached", ExpectedRevision: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Project.Status != ProjectStatusCompleted || completed.Project.Revision != 4 {
		t.Fatalf("completed project: %+v", completed.Project)
	}
	if len(execution.cancellationCalls) != 2 ||
		execution.cancellationCalls[0].reason != CancellationProjectPaused ||
		execution.cancellationCalls[1].reason != CancellationOperatorCompleted {
		t.Fatalf("run cancellation causes: %+v", execution.cancellationCalls)
	}

	reopened, err := service.ReopenProject(t.Context(), created.Project.ID, ReopenRequest{
		Feedback: "external correction", ExpectedRevision: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Project.Generation != 2 || reopened.Project.Revision != 5 ||
		reopened.PreviousCompletion != completed.Completion {
		t.Fatalf("reopened project: %+v", reopened)
	}

	replay, err := service.ListChanges(t.Context(), created.Project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Changes) != 5 || replay.CurrentRevision != 5 {
		t.Fatalf("change replay: %+v", replay)
	}
}

func TestLegacyRevisionZeroProjectCanCommitItsFirstHistoryRevision(t *testing.T) {
	service, db := newProjectTestService(t)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.projects.Create(t.Context(), tx, "p000", "Legacy", false, UTCNow(), ""); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := service.facts.Create(t.Context(), tx, "origin", "p000", "origin"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := service.facts.Create(t.Context(), tx, "goal", "p000", "goal"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := InitializeHistory(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	receipt, err := service.UpdateTitle(t.Context(), "p000", UpdateProjectTitleRequest{
		Title: "Legacy renamed", ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ProjectRevision != 1 || receipt.Project.Revision != 1 {
		t.Fatalf("first legacy receipt: %+v", receipt)
	}
	history := readHistoryPageForTest(t, service, "p000", HistoryPageQuery{})
	if len(history.Revisions) != 1 || history.Revisions[0].Revision != 1 ||
		history.AvailableFromRevision != 1 {
		t.Fatalf("first legacy history: %+v", history)
	}
}

func TestProjectServiceReturnsTransportIndependentDomainErrors(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service, err := NewService(db, nil, &projectExecutionTestAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(t.Context(), CreateProjectRequest{Title: " ", Origin: "origin", Goal: "goal"})
	assertDomainErrorCode(t, err, ErrorInvalidArgument)

	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Errors", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.UpdateTitle(t.Context(), created.Project.ID, UpdateProjectTitleRequest{
		Title: "  ", ExpectedRevision: created.Project.Revision,
	})
	assertDomainErrorCode(t, err, ErrorInvalidArgument)
	_, err = service.ResumeProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{ExpectedRevision: 1})
	assertDomainErrorCode(t, err, ErrorConflict)

	_, err = service.CancelIntent(t.Context(), created.Project.ID, "i999", CancelIntentRequest{
		Reason: "invalid target", ExpectedRevision: 1,
	})
	assertDomainErrorCode(t, err, ErrorNotFound)

	intent := proposeTestIntent(t, service, db, created.Project.ID, "inspect", []string{"origin"})
	concludeTestIntent(t, service, db, created.Project.ID, intent.ID, "result")
	_, err = service.CancelIntent(t.Context(), created.Project.ID, intent.ID, CancelIntentRequest{
		Reason: "already done", ExpectedRevision: 3,
	})
	assertDomainErrorCode(t, err, ErrorConflict)
}

func TestProjectServiceOwnsStructuredRunResultApplication(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service, err := NewService(db, nil, &projectExecutionTestAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Run result", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := service.ApplyRunResult(t.Context(), tx, ApplyRunResultCommand{
		RunID:     "run-reason-1",
		ProjectID: created.Project.ID, Task: RunTaskReason,
		Generation: 1, InputRevision: 1, Worker: "reasoner",
		AppliedAt: "2026-07-22T12:00:00Z",
		Outcome: RunOutcome{
			Kind:    RunOutcomeProposeIntents,
			Intents: []ProposedRunIntent{{From: []string{"origin"}, Description: "  inspect evidence  "}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Stale || applied.ProjectRevision != 2 || applied.Change == nil ||
		applied.Change.Kind != ChangeIntentsProposed || len(applied.CreatedIntentIDs) != 1 {
		t.Fatalf("applied result: %+v", applied)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	detail, err := service.Get(t.Context(), created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Revision != 2 || len(detail.Intents) != 1 || detail.Intents[0].Description != "inspect evidence" {
		t.Fatalf("Blackboard after Run result: %+v", detail)
	}

	if _, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
		Content: "new input", ExpectedRevision: 2,
	}); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := service.ApplyRunResult(t.Context(), tx, ApplyRunResultCommand{
		RunID:     "run-reason-stale",
		ProjectID: created.Project.ID, Task: RunTaskReason,
		Generation: 1, InputRevision: 2, AppliedAt: "2026-07-22T12:01:00Z",
		Outcome: RunOutcome{Kind: RunOutcomeNoChange},
	})
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Stale || stale.ProjectRevision != 3 || stale.Change != nil {
		t.Fatalf("stale result: %+v", stale)
	}
}

func TestExecutionAdmissionUsesDeploymentRuntimePolicy(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service, err := NewService(db, nil, &projectExecutionTestAdapter{}, ServiceOptions{
		RuntimePolicy: RuntimePolicy{IntentLeaseSeconds: 31, ReasonLeaseSeconds: 47},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Deployment policy", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	admission, err := service.ExecutionAdmission(t.Context(), tx, created.Project.ID, RunTaskReason, nil)
	if err != nil {
		t.Fatal(err)
	}
	if admission.IntentLeaseSeconds != 31 || admission.ReasonLeaseSeconds != 47 {
		t.Fatalf("execution admission Lease policy = %+v", admission)
	}
}

func assertDomainErrorCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != code {
		t.Fatalf("expected domain error %s, got %v", code, err)
	}
}
