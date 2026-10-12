package project

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCorrectionCommandsCommitTypedRevisionReceiptCancellationAndPublication(t *testing.T) {
	t.Run("fact supersede", func(t *testing.T) {
		service, _, publisher, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		publisher.observations = nil
		execution.calls = nil
		execution.count = 2

		result, err := service.SupersedeFact(t.Context(), created.Project.ID, "origin", SupersedeFactRequest{
			Description: "corrected origin", Reason: "source was stale", ExpectedRevision: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.ProjectRevision != 2 || result.AffectedRunCount != 2 ||
			result.Original.Status != FactStatusRetracted || result.Replacement.Description != "corrected origin" ||
			result.Original.ReplacementFactID == nil || *result.Original.ReplacementFactID != result.Replacement.ID {
			t.Fatalf("Supersede receipt = %+v", result)
		}
		assertCorrectionHistory(t, service, created.Project.ID, 2, ChangeFactSuperseded, "source was stale", "test-operator")
		assertCorrectionPublication(t, publisher, 0, created.Project.ID, 2, ChangeFactSuperseded, 2)
		assertFactCancellation(t, execution, 0, created.Project.ID, 2, CancellationFactRetracted, "origin", 1)
		assertHistoryResultValue(t, service, created.Project.ID, 2, map[string]any{
			"reason": "source was stale", "replacement_fact_id": result.Replacement.ID,
			"affected_run_count": float64(2),
		})
		assertCorrectionFacts(t, service, created.Project.ID, result.Replacement.ID)
	})

	t.Run("fact retract", func(t *testing.T) {
		service, _, publisher, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		publisher.observations = nil
		execution.calls = nil
		execution.count = 1

		result, err := service.RetractFact(t.Context(), created.Project.ID, "origin", RetractFactRequest{
			Reason: "evidence withdrawn", ExpectedRevision: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.ProjectRevision != 2 || result.AffectedRunCount != 1 || result.Status != FactStatusRetracted ||
			result.RetractionReason == nil || *result.RetractionReason != "evidence withdrawn" {
			t.Fatalf("Retract receipt = %+v", result)
		}
		assertCorrectionHistory(t, service, created.Project.ID, 2, ChangeFactRetracted, "evidence withdrawn", "test-operator")
		assertCorrectionPublication(t, publisher, 0, created.Project.ID, 2, ChangeFactRetracted, 1)
		assertFactCancellation(t, execution, 0, created.Project.ID, 2, CancellationFactRetracted, "origin", 1)
	})

	t.Run("intent cancel", func(t *testing.T) {
		service, db, publisher, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		intent := proposeTestIntent(t, service, db, created.Project.ID, "inspect evidence", []string{"origin"})
		publisher.observations = nil
		execution.calls = nil
		execution.count = 3

		result, err := service.CancelIntent(t.Context(), created.Project.ID, intent.ID, CancelIntentRequest{
			Reason: "no longer relevant", ExpectedRevision: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.ProjectRevision != 3 || result.AffectedRunCount != 3 || result.Status != IntentStatusCancelled ||
			result.CancelledBy == nil || *result.CancelledBy != "test-operator" {
			t.Fatalf("Cancel receipt = %+v", result)
		}
		assertCorrectionHistory(t, service, created.Project.ID, 3, ChangeIntentCancelled, "no longer relevant", "test-operator")
		assertCorrectionPublication(t, publisher, 0, created.Project.ID, 3, ChangeIntentCancelled, 3)
		if len(execution.calls) != 1 || execution.calls[0].selector.Clauses[0].TargetIntentID == nil ||
			*execution.calls[0].selector.Clauses[0].TargetIntentID != intent.ID {
			t.Fatalf("Cancel selector = %+v", execution.calls)
		}
	})
}

func TestCorrectionCommandsRejectAndRollBackWithoutPublishing(t *testing.T) {
	t.Run("stale revision", func(t *testing.T) {
		service, _, publisher, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		publisher.observations = nil
		execution.calls = nil
		_, err := service.RetractFact(t.Context(), created.Project.ID, "origin", RetractFactRequest{
			Reason: "stale", ExpectedRevision: 0,
		})
		assertRevisionConflict(t, err, 1)
		assertCorrectionRolledBack(t, service, created.Project.ID, publisher, execution)
	})

	t.Run("completed Project checks lifecycle before expected revision", func(t *testing.T) {
		service, _, publisher, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		completed, err := service.CompleteProject(t.Context(), created.Project.ID, CompleteRequest{
			From: []string{"origin"}, Description: "done", ExpectedRevision: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		publisher.observations = nil
		execution.calls = nil
		_, err = service.RetractFact(t.Context(), created.Project.ID, "origin", RetractFactRequest{
			Reason: "invalid lifecycle", ExpectedRevision: 1,
		})
		assertDomainErrorCode(t, err, ErrorForbidden)
		if !strings.Contains(err.Error(), "completed") || completed.Project.Revision != 2 {
			t.Fatalf("completed correction error = %v", err)
		}
		if len(publisher.observations) != 0 || len(execution.calls) != 0 {
			t.Fatalf("lifecycle failure leaked effects: publications=%d calls=%d", len(publisher.observations), len(execution.calls))
		}
	})

	t.Run("invalid target and terminal state", func(t *testing.T) {
		service, db, publisher, execution := newHintRevisionCommitTestService(t)
		created := mustCreateHintTestProject(t, service)
		publisher.observations = nil
		execution.calls = nil

		_, err := service.SupersedeFact(t.Context(), created.Project.ID, "goal", SupersedeFactRequest{
			Description: "replacement", Reason: "goal is immutable", ExpectedRevision: 1,
		})
		assertDomainErrorCode(t, err, ErrorForbidden)
		_, err = service.SupersedeFact(t.Context(), created.Project.ID, "missing", SupersedeFactRequest{
			Description: "replacement", Reason: "missing source", ExpectedRevision: 1,
		})
		assertDomainErrorCode(t, err, ErrorNotFound)
		intent := proposeTestIntent(t, service, db, created.Project.ID, "done", []string{"origin"})
		concludeTestIntent(t, service, db, created.Project.ID, intent.ID, "concluded")
		publisher.observations = nil
		execution.calls = nil
		_, err = service.CancelIntent(t.Context(), created.Project.ID, intent.ID, CancelIntentRequest{
			Reason: "already concluded", ExpectedRevision: 3,
		})
		assertDomainErrorCode(t, err, ErrorConflict)
		if len(publisher.observations) != 0 || len(execution.calls) != 0 {
			t.Fatalf("terminal correction leaked effects: publications=%d calls=%d", len(publisher.observations), len(execution.calls))
		}
	})

	t.Run("cancellation and commit failures", func(t *testing.T) {
		for _, test := range []struct {
			name      string
			configure func(*transactionalHintExecutionPort)
		}{
			{name: "cancellation", configure: func(port *transactionalHintExecutionPort) { port.err = errors.New("cancel active Runs") }},
			{name: "commit", configure: func(port *transactionalHintExecutionPort) { port.parentID = "missing-parent" }},
		} {
			t.Run(test.name, func(t *testing.T) {
				service, _, publisher, execution := newHintRevisionCommitTestService(t)
				created := mustCreateHintTestProject(t, service)
				publisher.observations = nil
				test.configure(execution)
				_, err := service.RetractFact(t.Context(), created.Project.ID, "origin", RetractFactRequest{
					Reason: "must roll back", ExpectedRevision: 1,
				})
				if err == nil {
					t.Fatal("expected Correction failure")
				}
				assertCorrectionRolledBack(t, service, created.Project.ID, publisher, execution)
			})
		}
	})
}

func assertCorrectionHistory(t *testing.T, service *Service, projectID string, revision int64, kind ChangeKind, reason, actor string) {
	t.Helper()
	history := readHistoryRevisionForTest(t, service, projectID, revision)
	if history.Kind != kind || history.Source != (ChangeSource{Kind: "operator", Name: actor}) {
		t.Fatalf("Correction history metadata = %+v", history)
	}
	delta, err := decodeHistoryDelta(mustSingleChange(t, service, projectID, revision).Delta)
	if err != nil {
		t.Fatal(err)
	}
	if delta.Kind != kind || delta.Result["reason"] != reason {
		t.Fatalf("Correction history delta = %+v", delta)
	}
	if kind == ChangeFactSuperseded && len(delta.Facts) != 2 {
		t.Fatalf("Supersede fact delta = %+v", delta.Facts)
	}
}

func assertCorrectionPublication(t *testing.T, publisher *observingProjectPublisher, index int, projectID string, revision int64, kind ChangeKind, affected int) {
	t.Helper()
	if len(publisher.observations) <= index {
		t.Fatalf("missing Correction publication: %+v", publisher.observations)
	}
	observation := publisher.observations[index]
	if observation.err != nil || observation.change.ProjectID != projectID || observation.change.Revision != revision ||
		observation.change.Kind != kind || observation.revision != revision {
		t.Fatalf("Correction publication = %+v", observation)
	}
	delta, err := decodeHistoryDelta(observation.change.Delta)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := delta.Result["affected_run_count"].(float64); !ok || int(got) != affected {
		t.Fatalf("published affected count = %+v", delta.Result)
	}
}

func assertFactCancellation(t *testing.T, execution *transactionalHintExecutionPort, index int, projectID string, revision int64, reason CancellationReason, factID string, inputRevision int64) {
	t.Helper()
	if len(execution.calls) <= index {
		t.Fatalf("missing Fact cancellation: %+v", execution.calls)
	}
	call := execution.calls[index]
	if call.projectID != projectID || call.revision != revision || call.reason != reason || len(call.selector.Clauses) != 2 ||
		call.selector.Clauses[0].SourceFactID == nil || *call.selector.Clauses[0].SourceFactID != factID ||
		call.selector.Clauses[1].InputRevisionAtMost == nil || *call.selector.Clauses[1].InputRevisionAtMost != inputRevision {
		t.Fatalf("Fact cancellation = %+v", call)
	}
}

func assertCorrectionRolledBack(t *testing.T, service *Service, projectID string, publisher *observingProjectPublisher, execution *transactionalHintExecutionPort) {
	t.Helper()
	detail, err := service.Get(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Revision != 1 || len(detail.Facts) != 2 || len(detail.Intents) != 0 {
		t.Fatalf("Correction rollback Project = %+v", detail)
	}
	history := readHistoryPageForTest(t, service, projectID, HistoryPageQuery{Limit: 100})
	if len(history.Revisions) != 1 || history.Revisions[0].Kind != ChangeProjectCreated {
		t.Fatalf("Correction rollback History = %+v", history)
	}
	if len(publisher.observations) != 0 {
		t.Fatalf("Correction rollback effects: publications=%+v calls=%+v", publisher.observations, execution.calls)
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
		t.Fatalf("Correction rollback retained %d Run cancellations; calls=%+v", cancellations, execution.calls)
	}
}

func assertCorrectionFacts(t *testing.T, service *Service, projectID, replacementID string) {
	t.Helper()
	detail, err := service.Get(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	var origin, replacement *Fact
	for index := range detail.Facts {
		fact := &detail.Facts[index]
		if fact.ID == "origin" {
			origin = fact
		}
		if fact.ID == replacementID {
			replacement = fact
		}
	}
	if origin == nil || replacement == nil || origin.Status != FactStatusRetracted || replacement.Supersedes == nil || *replacement.Supersedes != "origin" {
		t.Fatalf("persisted Fact correction = %+v", detail.Facts)
	}
}

func assertHistoryResultValue(t *testing.T, service *Service, projectID string, revision int64, want map[string]any) {
	t.Helper()
	delta, err := decodeHistoryDelta(mustSingleChange(t, service, projectID, revision).Delta)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(delta.Result, want) {
		t.Fatalf("Correction result = %+v, want %+v", delta.Result, want)
	}
}

func mustSingleChange(t *testing.T, service *Service, projectID string, revision int64) BlackboardChange {
	t.Helper()
	replay, err := service.ListChanges(t.Context(), projectID, revision-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Changes) != 1 {
		t.Fatalf("revision %d changes = %+v", revision, replay.Changes)
	}
	return replay.Changes[0]
}
