package project

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/database"
)

type noopProjectEventPublisher struct{}

func (noopProjectEventPublisher) PublishChange(BlackboardChange) {}

type projectExecutionBaselineCall struct {
	projectID  string
	generation int
	revision   int64
}

type projectExecutionPreviewCall struct {
	projectID string
	selector  RunCancellationSelector
}

type projectExecutionCancellationCall struct {
	projectID string
	revision  int64
	reason    CancellationReason
	selector  RunCancellationSelector
}

type projectExecutionCleanupCall struct {
	projectID string
}

// projectExecutionTestAdapter is the complete transaction participant used by
// Project tests that do not need a real Run ledger. It records every operation
// and exposes independent failures so fixture setup cannot hide a missing
// execution capability behind a no-op fallback.
type projectExecutionTestAdapter struct {
	baselineCalls     []projectExecutionBaselineCall
	previewCalls      []projectExecutionPreviewCall
	cancellationCalls []projectExecutionCancellationCall
	cleanupCalls      []projectExecutionCleanupCall

	previewCount    int
	cancelCount     int
	cleanupJobID    string
	baselineErr     error
	previewErr      error
	cancellationErr error
	cleanupErr      error
}

var _ ExecutionPort = (*projectExecutionTestAdapter)(nil)

func (adapter *projectExecutionTestAdapter) EnsureReasonSchedulingBaseline(
	_ context.Context, _ DBTX, projectID string, generation int, revision int64, _ string,
) error {
	adapter.baselineCalls = append(adapter.baselineCalls, projectExecutionBaselineCall{
		projectID: projectID, generation: generation, revision: revision,
	})
	return adapter.baselineErr
}

func (adapter *projectExecutionTestAdapter) PreviewRunCancellation(
	_ context.Context, _ DBTX, projectID string, selector RunCancellationSelector,
) (int, error) {
	adapter.previewCalls = append(adapter.previewCalls, projectExecutionPreviewCall{
		projectID: projectID, selector: selector,
	})
	if adapter.previewErr != nil {
		return 0, adapter.previewErr
	}
	return adapter.previewCount, nil
}

func (adapter *projectExecutionTestAdapter) CancelRuns(
	_ context.Context, _ DBTX, projectID string, revision int64, reason CancellationReason,
	selector RunCancellationSelector,
) (int, error) {
	adapter.cancellationCalls = append(adapter.cancellationCalls, projectExecutionCancellationCall{
		projectID: projectID, revision: revision, reason: reason, selector: selector,
	})
	if adapter.cancellationErr != nil {
		return 0, adapter.cancellationErr
	}
	return adapter.cancelCount, nil
}

func (adapter *projectExecutionTestAdapter) QueueProjectCleanup(
	_ context.Context, _ DBTX, projectID, _ string,
) (string, error) {
	adapter.cleanupCalls = append(adapter.cleanupCalls, projectExecutionCleanupCall{projectID: projectID})
	if adapter.cleanupErr != nil {
		return "", adapter.cleanupErr
	}
	if adapter.cleanupJobID == "" {
		return "test-cleanup", nil
	}
	return adapter.cleanupJobID, nil
}

func newProjectTestService(t *testing.T) (*Service, *database.DB) {
	t.Helper()
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		INSERT INTO blackboard_history_retention_operations (operation_id, active, created_at)
		VALUES ('test-history-mutation', 1, ?)`, UTCNow()); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	service, err := NewService(db, noopProjectEventPublisher{}, &projectExecutionTestAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	return service, db
}

func readHistoryPageForTest(
	t *testing.T,
	service *Service,
	projectID string,
	query HistoryPageQuery,
) HistoryPage {
	t.Helper()
	tx, err := service.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	page, err := service.history.ReadPage(t.Context(), tx, projectID, query)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return page
}

func readHistoryRevisionForTest(
	t *testing.T,
	service *Service,
	projectID string,
	revision int64,
) HistoryRevision {
	t.Helper()
	page := readHistoryPageForTest(t, service, projectID, HistoryPageQuery{
		BeforeRevision: revision + 1,
		Limit:          1,
	})
	if len(page.Revisions) != 1 || page.Revisions[0].Revision != revision {
		t.Fatalf("History revision %d = %+v", revision, page.Revisions)
	}
	return page.Revisions[0]
}

func applyTestRunResult(t *testing.T, service *Service, db *database.DB, command ApplyRunResultCommand) (ApplyRunResult, error) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := service.ApplyRunResult(t.Context(), tx, command)
	if err != nil {
		return ApplyRunResult{}, err
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result, nil
}

func proposeTestIntent(t *testing.T, service *Service, db *database.DB, projectID, description string, from []string) Intent {
	t.Helper()
	detail, err := service.Get(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyTestRunResult(t, service, db, ApplyRunResultCommand{
		RunID: fmt.Sprintf("test-reason-%d", detail.Project.Revision), ProjectID: projectID,
		Task: RunTaskReason, Generation: detail.Project.Generation, InputRevision: detail.Project.Revision,
		Worker: "test.reasoner", AppliedAt: UTCNow(),
		Outcome: RunOutcome{Kind: RunOutcomeProposeIntents, Intents: []ProposedRunIntent{{From: from, Description: description}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Get(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, intent := range updated.Intents {
		if len(result.CreatedIntentIDs) == 1 && intent.ID == result.CreatedIntentIDs[0] {
			return intent
		}
	}
	t.Fatal("Run result did not create its Intent")
	return Intent{}
}

func concludeTestIntent(t *testing.T, service *Service, db *database.DB, projectID, intentID, description string) (Intent, Fact) {
	t.Helper()
	detail, err := service.Get(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyTestRunResult(t, service, db, ApplyRunResultCommand{
		RunID: fmt.Sprintf("test-explore-%s-%d", intentID, detail.Project.Revision), ProjectID: projectID,
		TargetIntent: &intentID, Task: RunTaskExplore, Generation: detail.Project.Generation,
		InputRevision: detail.Project.Revision, Worker: "test.explorer", AppliedAt: UTCNow(),
		Outcome: RunOutcome{Kind: RunOutcomeConcludeIntent, FactDescription: description},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Get(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	var concluded Intent
	for _, intent := range updated.Intents {
		if intent.ID == intentID {
			concluded = intent
			break
		}
	}
	for _, fact := range updated.Facts {
		if len(result.CreatedFactIDs) == 1 && fact.ID == result.CreatedFactIDs[0] {
			return concluded, fact
		}
	}
	t.Fatal("Run result did not create its Fact")
	return Intent{}, Fact{}
}
