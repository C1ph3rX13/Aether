package project

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/database"
)

type hintCancellationCall struct {
	projectID string
	revision  int64
	reason    CancellationReason
	selector  RunCancellationSelector
}

type hintBaselineCall struct {
	projectID  string
	generation int
	revision   int64
}

type hintPreviewCall struct {
	projectID string
	selector  RunCancellationSelector
}

type hintCleanupCall struct {
	projectID     string
	projectExists int
}

type transactionalHintExecutionPort struct {
	count            int
	parentID         string
	baselineParentID string
	cleanupParentID  string
	baselineErr      error
	previewErr       error
	err              error
	cleanupErr       error
	baselineCalls    []hintBaselineCall
	previewCalls     []hintPreviewCall
	calls            []hintCancellationCall
	cleanupCalls     []hintCleanupCall
	events           []string
}

var _ ExecutionPort = (*transactionalHintExecutionPort)(nil)

func (port *transactionalHintExecutionPort) EnsureReasonSchedulingBaseline(
	ctx context.Context, tx DBTX, projectID string, generation int, revision int64, _ string,
) error {
	port.baselineCalls = append(port.baselineCalls, hintBaselineCall{
		projectID: projectID, generation: generation, revision: revision,
	})
	if port.baselineErr != nil {
		return port.baselineErr
	}
	if port.baselineParentID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO test_project_command_participants (kind, project_id, revision, parent_id)
		VALUES ('baseline', ?, ?, ?)`, projectID, revision, port.baselineParentID)
	return err
}

func (port *transactionalHintExecutionPort) PreviewRunCancellation(
	_ context.Context, _ DBTX, projectID string, selector RunCancellationSelector,
) (int, error) {
	port.previewCalls = append(port.previewCalls, hintPreviewCall{projectID: projectID, selector: selector})
	if port.previewErr != nil {
		return 0, port.previewErr
	}
	return port.count, nil
}

func (port *transactionalHintExecutionPort) CancelRuns(
	ctx context.Context,
	tx DBTX,
	projectID string,
	revision int64,
	reason CancellationReason,
	selector RunCancellationSelector,
) (int, error) {
	port.calls = append(port.calls, hintCancellationCall{
		projectID: projectID, revision: revision, reason: reason, selector: selector,
	})
	port.events = append(port.events, "cancel")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO test_hint_run_cancellations (project_id, revision, parent_id)
		VALUES (?, ?, ?)`, projectID, revision, port.parentID); err != nil {
		return 0, err
	}
	if port.err != nil {
		return 0, port.err
	}
	return port.count, nil
}

func (port *transactionalHintExecutionPort) QueueProjectCleanup(
	ctx context.Context, tx DBTX, projectID, _ string,
) (string, error) {
	port.cleanupCalls = append(port.cleanupCalls, hintCleanupCall{projectID: projectID})
	if port.cleanupErr != nil {
		return "", port.cleanupErr
	}
	var projectExists int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM projects WHERE id = ?)`, projectID).Scan(&projectExists); err != nil {
		return "", err
	}
	port.cleanupCalls[len(port.cleanupCalls)-1].projectExists = projectExists
	port.events = append(port.events, "cleanup")
	parentID := port.cleanupParentID
	if parentID == "" {
		parentID = "valid-parent"
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO test_project_command_participants (kind, project_id, revision, parent_id)
		VALUES ('cleanup', ?, 0, ?)`, projectID, parentID)
	if err != nil {
		return "", err
	}
	return "cleanup-1", nil
}

type publishedProjectObservation struct {
	change      BlackboardChange
	revision    int64
	hintCount   int
	activeHints int
	err         error
}

type observingProjectPublisher struct {
	db interface {
		GetReadConn(context.Context) (*sql.Conn, error)
	}
	observations []publishedProjectObservation
	deletions    []publishedProjectDeletion
}

type publishedProjectDeletion struct {
	projectID string
	revision  int64
	exists    int
	cleanup   int
	err       error
}

func (publisher *observingProjectPublisher) PublishChange(change BlackboardChange) {
	observation := publishedProjectObservation{change: change}
	conn, err := publisher.db.GetReadConn(context.Background())
	if err != nil {
		observation.err = err
		publisher.observations = append(publisher.observations, observation)
		return
	}
	defer conn.Close()
	observation.err = conn.QueryRowContext(context.Background(), `
		SELECT p.blackboard_revision,
		       COUNT(h.id),
		       COALESCE(SUM(CASE WHEN h.withdrawn_at IS NULL THEN 1 ELSE 0 END), 0)
		FROM projects p
		LEFT JOIN hints h ON h.project_id = p.id
		WHERE p.id = ?
		GROUP BY p.id`, change.ProjectID).Scan(
		&observation.revision, &observation.hintCount, &observation.activeHints,
	)
	publisher.observations = append(publisher.observations, observation)
}

func (publisher *observingProjectPublisher) PublishProjectDeleted(projectID string, revision int64) {
	observation := publishedProjectDeletion{projectID: projectID, revision: revision}
	conn, err := publisher.db.GetReadConn(context.Background())
	if err != nil {
		observation.err = err
		publisher.deletions = append(publisher.deletions, observation)
		return
	}
	defer conn.Close()
	observation.err = conn.QueryRowContext(context.Background(), `
		SELECT EXISTS (SELECT 1 FROM projects WHERE id = ?),
		       COUNT(*)
		FROM test_project_command_participants
		WHERE kind = 'cleanup' AND project_id = ?`, projectID, projectID).
		Scan(&observation.exists, &observation.cleanup)
	publisher.deletions = append(publisher.deletions, observation)
}

func TestExecutionCoordinationPortRecordsPreviewAndFailureInjection(t *testing.T) {
	t.Run("preview", func(t *testing.T) {
		service, _, _, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		execution.count = 4

		preview, err := service.PreviewImpact(t.Context(), created.Project.ID, ImpactRequest{
			Action: ImpactHintChange, ExpectedRevision: created.Project.Revision,
		})
		if err != nil {
			t.Fatal(err)
		}
		if preview.ProjectRevision != created.Project.Revision || preview.AffectedRunCount != 4 {
			t.Fatalf("impact preview = %+v", preview)
		}
		if len(execution.previewCalls) != 1 || execution.previewCalls[0].projectID != created.Project.ID {
			t.Fatalf("preview calls = %+v", execution.previewCalls)
		}
	})

	t.Run("preview failure", func(t *testing.T) {
		service, _, _, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		execution.previewErr = errors.New("preview unavailable")

		_, err := service.PreviewImpact(t.Context(), created.Project.ID, ImpactRequest{
			Action: ImpactHintChange, ExpectedRevision: created.Project.Revision,
		})
		if err == nil || err.Error() != "preview unavailable" {
			t.Fatalf("preview failure = %v", err)
		}
		if len(execution.previewCalls) != 1 {
			t.Fatalf("preview failure calls = %+v", execution.previewCalls)
		}
	})

	t.Run("baseline failure", func(t *testing.T) {
		service, _, publisher, execution := newHintRevisionCommitTestService(t)
		execution.baselineErr = errors.New("baseline unavailable")

		_, err := service.Create(t.Context(), CreateProjectRequest{
			Title: "baseline failure", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
		})
		if err == nil || err.Error() != "baseline unavailable" {
			t.Fatalf("baseline failure = %v", err)
		}
		if len(execution.baselineCalls) != 1 {
			t.Fatalf("baseline calls = %+v", execution.baselineCalls)
		}
		assertNoProjectCommandState(t, service, publisher)
	})

	t.Run("cleanup failure", func(t *testing.T) {
		service, _, publisher, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		execution.cleanupErr = errors.New("cleanup unavailable")

		_, err := service.DeleteConfirmed(t.Context(), created.Project.ID, DeleteProjectRequest{
			ExpectedTitle: created.Project.Title, ExpectedRevision: created.Project.Revision,
		})
		if err == nil || err.Error() != "cleanup unavailable" {
			t.Fatalf("cleanup failure = %v", err)
		}
		if len(execution.cleanupCalls) != 1 || execution.cleanupCalls[0].projectID != created.Project.ID {
			t.Fatalf("cleanup calls = %+v", execution.cleanupCalls)
		}
		detail, getErr := service.Get(t.Context(), created.Project.ID)
		if getErr != nil || detail.Project.Revision != created.Project.Revision {
			t.Fatalf("cleanup failure changed project = %+v error=%v", detail.Project, getErr)
		}
		assertNoProjectCommandParticipants(t, service)
		if len(publisher.deletions) != 0 {
			t.Fatalf("cleanup failure published deletion = %+v", publisher.deletions)
		}
	})
}

func TestHintCommandsCommitBlackboardHistoryCancellationReceiptAndPublicationTogether(t *testing.T) {
	service, _, publisher, execution := newHintRevisionCommitTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Hint revision commit", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher.observations = nil
	execution.count = 2

	createdHint, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
		Content: "inspect the deployment evidence", ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if createdHint.ProjectRevision != 2 || createdHint.AffectedRunCount != 2 ||
		createdHint.Hint.ID != "h001" || !createdHint.Hint.IsActive() {
		t.Fatalf("Create Hint receipt = %+v", createdHint)
	}
	assertCommittedHintRevision(t, service, created.Project.ID, 2, ChangeHintCreated, 1, 1)
	assertHintCancellation(t, execution, 0, created.Project.ID, 2, 1)
	assertHintPublication(t, publisher, 0, 2, ChangeHintCreated, 1, 1)
	assertHistoryResult(t, service, created.Project.ID, 2, map[string]any{"affected_run_count": float64(2)})

	publisher.observations = nil
	execution.count = 1
	withdrawn, err := service.WithdrawHint(t.Context(), created.Project.ID, createdHint.Hint.ID, WithdrawHintRequest{
		Reason: "superseded by operator evidence", ExpectedRevision: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if withdrawn.ProjectRevision != 3 || withdrawn.AffectedRunCount != 1 ||
		withdrawn.ID != createdHint.Hint.ID || withdrawn.IsActive() {
		t.Fatalf("Withdraw Hint receipt = %+v", withdrawn)
	}
	assertCommittedHintRevision(t, service, created.Project.ID, 3, ChangeHintWithdrawn, 1, 0)
	assertHintCancellation(t, execution, 1, created.Project.ID, 3, 2)
	assertHintPublication(t, publisher, 0, 3, ChangeHintWithdrawn, 1, 0)
	assertHistoryResult(t, service, created.Project.ID, 3, map[string]any{
		"affected_run_count": float64(1), "reason": "superseded by operator evidence",
	})

	replay, err := service.ListChanges(t.Context(), created.Project.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Changes) != 2 || replay.Changes[0].ChangeHash == "" ||
		replay.Changes[1].PreviousHash != replay.Changes[0].ChangeHash ||
		replay.Changes[1].ChangeHash == "" {
		t.Fatalf("Hint Change hash chain = %+v", replay.Changes)
	}
}

func TestHintCommandsRollBackEveryParticipantAndSuppressPublication(t *testing.T) {
	t.Run("stale expected revision", func(t *testing.T) {
		service, _, publisher, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		publisher.observations = nil

		_, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
			Content: "stale", ExpectedRevision: 0,
		})
		assertDomainErrorCode(t, err, ErrorConflict)
		assertHintCommandRolledBack(t, service, created.Project.ID, publisher, execution)
	})

	t.Run("completed Project lifecycle conflict precedes stale revision", func(t *testing.T) {
		service, _, publisher, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		completed, err := service.CompleteProject(t.Context(), created.Project.ID, CompleteRequest{
			From: []string{"origin"}, Description: "complete", ExpectedRevision: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		publisher.observations = nil
		execution.calls = nil

		_, err = service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
			Content: "stale feedback", ExpectedRevision: 1,
		})
		assertDomainErrorCode(t, err, ErrorConflict)
		var domainErr *DomainError
		if !errors.As(err, &domainErr) || domainErr.CurrentRevision != nil ||
			!strings.Contains(domainErr.Detail, "only through Reopen") {
			t.Fatalf("completed Project Create Hint error = %+v", err)
		}
		assertHintCommandState(t, service, created.Project.ID, completed.Project.Revision, 0, 0, 2)
		if len(publisher.observations) != 0 || len(execution.calls) != 0 {
			t.Fatalf("completed Project failure leaked cancellation/publication: calls=%d publications=%d",
				len(execution.calls), len(publisher.observations))
		}
	})

	t.Run("illegal Hint state", func(t *testing.T) {
		service, _, publisher, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		createdHint, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
			Content: "temporary", ExpectedRevision: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.WithdrawHint(t.Context(), created.Project.ID, createdHint.Hint.ID, WithdrawHintRequest{
			Reason: "obsolete", ExpectedRevision: 2,
		}); err != nil {
			t.Fatal(err)
		}
		publisher.observations = nil
		execution.calls = nil

		_, err = service.WithdrawHint(t.Context(), created.Project.ID, createdHint.Hint.ID, WithdrawHintRequest{
			Reason: "again", ExpectedRevision: 3,
		})
		assertDomainErrorCode(t, err, ErrorConflict)
		assertHintCommandState(t, service, created.Project.ID, 3, 1, 0, 3)
		if len(publisher.observations) != 0 || len(execution.calls) != 0 {
			t.Fatalf("illegal state leaked cancellation/publication: calls=%d publications=%d",
				len(execution.calls), len(publisher.observations))
		}
	})

	t.Run("Run cancellation failure", func(t *testing.T) {
		service, _, publisher, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		publisher.observations = nil
		execution.err = errors.New("cancel active Runs")

		_, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
			Content: "must roll back", ExpectedRevision: 1,
		})
		if err == nil || err.Error() != "cancel active Runs" {
			t.Fatalf("cancellation error = %v", err)
		}
		assertHintCommandRolledBack(t, service, created.Project.ID, publisher, execution)
	})

	t.Run("SQLite commit failure", func(t *testing.T) {
		service, _, publisher, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		publisher.observations = nil
		execution.parentID = "missing-parent"

		_, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
			Content: "must not be published", ExpectedRevision: 1,
		})
		if err == nil {
			t.Fatal("expected deferred foreign-key failure at commit")
		}
		assertHintCommandRolledBack(t, service, created.Project.ID, publisher, execution)
	})
}

func TestHintRevisionCommitMaintainsPeriodicHistorySnapshots(t *testing.T) {
	service, db := newProjectTestService(t)
	created := mustCreateHintTestProject(t, service)
	for revision := int64(2); revision <= 100; revision++ {
		result, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
			Content: "periodic snapshot evidence", ExpectedRevision: revision - 1,
		})
		if err != nil {
			t.Fatalf("Create Hint revision %d: %v", revision, err)
		}
		if result.ProjectRevision != revision {
			t.Fatalf("Create Hint revision = %d, want %d", result.ProjectRevision, revision)
		}
	}

	conn, err := db.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var snapshotKind, stateHash string
	if err := conn.QueryRowContext(t.Context(), `
		SELECT snapshot_kind, state_hash
		FROM project_history_snapshots
		WHERE project_id = ? AND revision = 100`, created.Project.ID,
	).Scan(&snapshotKind, &stateHash); err != nil {
		t.Fatal(err)
	}
	if snapshotKind != "periodic" || stateHash == "" {
		t.Fatalf("Revision 100 snapshot = kind %q hash %q", snapshotKind, stateHash)
	}
}

func newHintRevisionCommitTestService(
	t *testing.T,
) (*Service, *database.DB, *observingProjectPublisher, *transactionalHintExecutionPort) {
	t.Helper()
	_, db := newProjectTestService(t)
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		CREATE TABLE test_hint_cancellation_parents (id TEXT PRIMARY KEY);
		CREATE TABLE test_hint_run_cancellations (
			project_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			parent_id TEXT NOT NULL,
			FOREIGN KEY (parent_id) REFERENCES test_hint_cancellation_parents(id)
				DEFERRABLE INITIALLY DEFERRED
		);
		CREATE TABLE test_project_command_participants (
			kind TEXT NOT NULL,
			project_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			parent_id TEXT NOT NULL,
			FOREIGN KEY (parent_id) REFERENCES test_hint_cancellation_parents(id)
				DEFERRABLE INITIALLY DEFERRED
		);
		INSERT INTO test_hint_cancellation_parents (id) VALUES ('valid-parent');
	`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	publisher := &observingProjectPublisher{db: db}
	execution := &transactionalHintExecutionPort{count: 1, parentID: "valid-parent"}
	service, err := NewService(db, publisher, execution, ServiceOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	return service, db, publisher, execution
}

func mustCreateHintTestProject(t *testing.T, service *Service) ProjectDetail {
	t.Helper()
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Hint rollback", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func assertCommittedHintRevision(
	t *testing.T, service *Service, projectID string, revision int64,
	kind ChangeKind, hintCount, activeHints int,
) {
	t.Helper()
	assertHintCommandState(t, service, projectID, revision, hintCount, activeHints, revision)
	history := readHistoryRevisionForTest(t, service, projectID, revision)
	transition, ok := history.Evidence.(HistoryTransitionEvidence)
	validHintTransition := false
	if ok && transition.Before != nil {
		switch kind {
		case ChangeHintCreated:
			validHintTransition = len(transition.Before.Hints) == 0 && len(transition.After.Hints) == 1 &&
				transition.After.Hints[0].IsActive()
		case ChangeHintWithdrawn:
			validHintTransition = len(transition.Before.Hints) == 1 && len(transition.After.Hints) == 1 &&
				transition.Before.Hints[0].ID == transition.After.Hints[0].ID &&
				transition.Before.Hints[0].IsActive() && !transition.After.Hints[0].IsActive()
		}
	}
	if history.Kind != kind || history.Source != (ChangeSource{Kind: "operator", Name: "test-operator"}) ||
		!validHintTransition {
		t.Fatalf("Hint history revision = %+v", history)
	}
}

func assertHistoryResult(
	t *testing.T, service *Service, projectID string, revision int64, want map[string]any,
) {
	t.Helper()
	replay, err := service.ListChanges(t.Context(), projectID, revision-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Changes) != 1 {
		t.Fatalf("revision %d replay = %+v", revision, replay)
	}
	delta, err := decodeHistoryDelta(replay.Changes[0].Delta)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(delta.Result, want) {
		t.Fatalf("revision %d result = %+v, want %+v", revision, delta.Result, want)
	}
}

func assertHintCancellation(
	t *testing.T, execution *transactionalHintExecutionPort, index int,
	projectID string, revision, inputRevision int64,
) {
	t.Helper()
	if len(execution.calls) <= index {
		t.Fatalf("missing cancellation call %d: %+v", index, execution.calls)
	}
	call := execution.calls[index]
	if call.projectID != projectID || call.revision != revision || call.reason != CancellationBlackboardChanged ||
		len(call.selector.Clauses) != 1 || len(call.selector.Clauses[0].Tasks) != 2 ||
		call.selector.Clauses[0].InputRevisionAtMost == nil ||
		*call.selector.Clauses[0].InputRevisionAtMost != inputRevision {
		t.Fatalf("cancellation call %d = %+v", index, call)
	}
}

func assertHintPublication(
	t *testing.T, publisher *observingProjectPublisher, index int,
	revision int64, kind ChangeKind, hintCount, activeHints int,
) {
	t.Helper()
	if len(publisher.observations) <= index {
		t.Fatalf("missing publication %d: %+v", index, publisher.observations)
	}
	observation := publisher.observations[index]
	if observation.err != nil || observation.change.Revision != revision ||
		observation.change.Kind != kind || observation.change.ChangeHash == "" ||
		observation.revision != revision || observation.hintCount != hintCount ||
		observation.activeHints != activeHints {
		t.Fatalf("publication observation %d = %+v", index, observation)
	}
}

func assertHintCommandRolledBack(
	t *testing.T, service *Service, projectID string,
	publisher *observingProjectPublisher, execution *transactionalHintExecutionPort,
) {
	t.Helper()
	assertHintCommandState(t, service, projectID, 1, 0, 0, 1)
	if len(publisher.observations) != 0 {
		t.Fatalf("failed command published %+v", publisher.observations)
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
		t.Fatalf("failed command retained %d Run cancellations; calls=%+v", cancellations, execution.calls)
	}
}

func assertHintCommandState(
	t *testing.T, service *Service, projectID string,
	revision int64, hintCount, activeHints int, historyCount int64,
) {
	t.Helper()
	detail, err := service.Get(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, hint := range detail.Hints {
		if hint.IsActive() {
			active++
		}
	}
	if detail.Project.Revision != revision || len(detail.Hints) != hintCount || active != activeHints {
		t.Fatalf("Project state = revision %d hints=%d active=%d", detail.Project.Revision, len(detail.Hints), active)
	}
	history := readHistoryPageForTest(t, service, projectID, HistoryPageQuery{Limit: 100})
	if int64(len(history.Revisions)) != historyCount {
		t.Fatalf("History count = %d, want %d: %+v", len(history.Revisions), historyCount, history)
	}
}
