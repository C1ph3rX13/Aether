package project

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBlackboardCorrectionsPreserveContentAndAppendOneChange(t *testing.T) {
	service, db := newProjectTestService(t)
	ctx := context.Background()

	detail, err := service.Create(ctx, CreateProjectRequest{
		Title:  "immutable blackboard",
		Origin: "original evidence",
		Goal:   "goal",
		Hints: []CreateHintInline{{
			Content: "initial hint",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	projectID := detail.Project.ID

	superseded, err := service.SupersedeFact(ctx, projectID, "origin", SupersedeFactRequest{
		Description:      "corrected evidence",
		Reason:           "source correction",
		ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if superseded.Original.Description != "original evidence" || superseded.Original.Status != FactStatusRetracted {
		t.Fatalf("original fact was not preserved as retracted: %+v", superseded.Original)
	}
	if superseded.Original.ReplacementFactID == nil || *superseded.Original.ReplacementFactID != superseded.Replacement.ID {
		t.Fatalf("replacement link missing: %+v", superseded)
	}
	if superseded.Replacement.Description != "corrected evidence" || superseded.Replacement.Supersedes == nil || *superseded.Replacement.Supersedes != "origin" {
		t.Fatalf("superseding fact: %+v", superseded.Replacement)
	}

	created := proposeTestIntent(t, service, db, projectID, "investigate", []string{superseded.Replacement.ID})
	cancelled, err := service.CancelIntent(ctx, projectID, created.ID, CancelIntentRequest{
		Reason: "no longer relevant", ExpectedRevision: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Description != "investigate" || cancelled.CancelledAt == nil || cancelled.CancelledBy == nil || *cancelled.CancelledBy != "Operator" {
		t.Fatalf("cancelled intent did not preserve content: %+v", cancelled)
	}
	stale, err := applyTestRunResult(t, service, db, ApplyRunResultCommand{
		RunID: "cancelled-intent-result", ProjectID: projectID, TargetIntent: &created.ID,
		Task: RunTaskExplore, Generation: 1, InputRevision: 3, Worker: "test.explorer", AppliedAt: UTCNow(),
		Outcome: RunOutcome{Kind: RunOutcomeConcludeIntent, FactDescription: "late result"},
	})
	if err != nil || !stale.Stale {
		t.Fatalf("cancelled Intent result should be stale: result=%+v err=%v", stale, err)
	}

	withdrawn, err := service.WithdrawHint(ctx, projectID, "h001", WithdrawHintRequest{
		Reason: "superseded guidance", ExpectedRevision: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if withdrawn.Content != "initial hint" || withdrawn.WithdrawnAt == nil {
		t.Fatalf("withdrawn hint did not preserve content: %+v", withdrawn)
	}

	retracted, err := service.RetractFact(ctx, projectID, superseded.Replacement.ID, RetractFactRequest{
		Reason: "evidence invalidated", ExpectedRevision: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retracted.Description != "corrected evidence" || retracted.Status != FactStatusRetracted {
		t.Fatalf("retracted replacement: %+v", retracted)
	}
	if _, err := service.RetractFact(ctx, projectID, superseded.Replacement.ID, RetractFactRequest{Reason: "again", ExpectedRevision: 6}); err == nil {
		t.Fatal("fact retracted twice")
	}
	if _, err := service.CancelIntent(ctx, projectID, created.ID, CancelIntentRequest{Reason: "again", ExpectedRevision: 6}); err == nil {
		t.Fatal("intent cancelled twice")
	}
	if _, err := service.WithdrawHint(ctx, projectID, "h001", WithdrawHintRequest{Reason: "again", ExpectedRevision: 6}); err == nil {
		t.Fatal("hint withdrawn twice")
	}

	replay, err := service.ListChanges(ctx, projectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []ChangeKind{
		ChangeProjectCreated,
		ChangeFactSuperseded,
		ChangeIntentsProposed,
		ChangeIntentCancelled,
		ChangeHintWithdrawn,
		ChangeFactRetracted,
	}
	if replay.ReplayRequired || replay.CurrentRevision != int64(len(wantKinds)) || len(replay.Changes) != len(wantKinds) {
		t.Fatalf("change replay: %+v", replay)
	}
	for index, wantKind := range wantKinds {
		change := replay.Changes[index]
		if change.Revision != int64(index+1) || change.Kind != wantKind {
			t.Fatalf("change %d: revision=%d kind=%s, want revision=%d kind=%s", index, change.Revision, change.Kind, index+1, wantKind)
		}
	}

	conn, err := db.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "UPDATE facts SET description = 'overwritten' WHERE project_id = ? AND id = 'origin'", projectID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("expected immutable fact trigger, got %v", err)
	}
	if _, err := conn.ExecContext(ctx, "UPDATE intents SET description = 'overwritten' WHERE project_id = ? AND id = ?", projectID, created.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("expected immutable intent trigger, got %v", err)
	}
	if _, err := conn.ExecContext(ctx, "UPDATE hints SET content = 'overwritten' WHERE project_id = ? AND id = 'h001'", projectID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("expected immutable hint trigger, got %v", err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO intent_sources (intent_id, project_id, fact_id) VALUES (?, ?, 'goal')", created.ID, projectID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("expected immutable intent source insert trigger, got %v", err)
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM intent_sources WHERE intent_id = ? AND project_id = ?", created.ID, projectID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("expected immutable intent source delete trigger, got %v", err)
	}
	for _, deletion := range []struct {
		entity    string
		statement string
		id        string
	}{
		{entity: "fact", statement: "DELETE FROM facts WHERE project_id = ? AND id = ?", id: "origin"},
		{entity: "intent", statement: "DELETE FROM intents WHERE project_id = ? AND id = ?", id: created.ID},
		{entity: "hint", statement: "DELETE FROM hints WHERE project_id = ? AND id = ?", id: "h001"},
	} {
		if _, err := conn.ExecContext(ctx, deletion.statement, projectID, deletion.id); err == nil || !strings.Contains(err.Error(), "only be deleted with their project") {
			t.Fatalf("expected %s hard-delete guard, got %v", deletion.entity, err)
		}
	}
	conn.Close()
	got, err := service.Get(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project.Revision != int64(len(wantKinds)) {
		t.Fatalf("failed commands changed revision: %d", got.Project.Revision)
	}
}

func TestChangeLogFailureRollsBackBlackboardAndRevision(t *testing.T) {
	service, db := newProjectTestService(t)
	ctx := context.Background()
	detail, err := service.Create(ctx, CreateProjectRequest{Title: "rollback", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := db.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
		CREATE TRIGGER reject_hint_change
		BEFORE INSERT ON blackboard_changes
		WHEN NEW.kind = 'hint_created'
		BEGIN
			SELECT RAISE(ABORT, 'reject hint change');
		END`); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	if _, err := service.CreateHint(ctx, detail.Project.ID, CreateHintRequest{Content: "rolled back", ExpectedRevision: 1}); err == nil {
		t.Fatal("expected change-log insertion failure")
	}
	got, err := service.Get(ctx, detail.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project.Revision != 1 || len(got.Hints) != 0 {
		t.Fatalf("failed transaction leaked business state: revision=%d hints=%+v", got.Project.Revision, got.Hints)
	}
	replay, err := service.ListChanges(ctx, detail.Project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Changes) != 1 || replay.Changes[0].Kind != ChangeProjectCreated {
		t.Fatalf("failed transaction leaked change: %+v", replay)
	}
}

func TestChangeReplayDetectsPrunedWindow(t *testing.T) {
	service, db := newProjectTestService(t)
	ctx := context.Background()
	detail, err := service.Create(ctx, CreateProjectRequest{Title: "replay", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateHint(ctx, detail.Project.ID, CreateHintRequest{Content: "one", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateHint(ctx, detail.Project.ID, CreateHintRequest{Content: "two", ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}

	conn, err := db.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		"DELETE FROM blackboard_changes WHERE project_id = ? AND revision = 1", detail.Project.ID,
	); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	stale, err := service.ListChanges(ctx, detail.Project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !stale.ReplayRequired || stale.EarliestRevision == nil || *stale.EarliestRevision != 2 || len(stale.Changes) != 0 {
		t.Fatalf("stale replay cursor: %+v", stale)
	}
	available, err := service.ListChanges(ctx, detail.Project.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if available.ReplayRequired || len(available.Changes) != 2 || available.Changes[0].Revision != 2 || available.Changes[1].Revision != 3 {
		t.Fatalf("available replay cursor: %+v", available)
	}
}

func TestChangeReplayRequiresRefreshWhenWindowIsTooLarge(t *testing.T) {
	service, db := newProjectTestService(t)
	ctx := context.Background()
	detail, err := service.Create(ctx, CreateProjectRequest{Title: "bounded replay", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
		WITH RECURSIVE revisions(value) AS (
			SELECT 2
			UNION ALL
			SELECT value + 1 FROM revisions WHERE value <= ?
		)
		INSERT INTO blackboard_changes (
			project_id, revision, generation, kind, entity_type, entity_id, data, created_at
		)
		SELECT ?, value, 1, 'hint_created', 'hint', NULL, '{}', '2026-01-01T00:00:00Z'
		FROM revisions`, MaximumReplayChanges+1, detail.Project.ID,
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE projects SET blackboard_revision = ? WHERE id = ?",
		MaximumReplayChanges+2, detail.Project.ID,
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()

	replay, err := service.ListChanges(ctx, detail.Project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.ReplayRequired || replay.CurrentRevision != MaximumReplayChanges+2 || len(replay.Changes) != 0 {
		t.Fatalf("bounded replay: %+v", replay)
	}
}

func TestChangeReplayRequiresRefreshBeforeLoadingTooManyBytes(t *testing.T) {
	service, db := newProjectTestService(t)
	ctx := context.Background()
	detail, err := service.Create(ctx, CreateProjectRequest{Title: "bounded replay bytes", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"content": strings.Repeat("x", int(MaximumReplayBytes))})
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	change := BlackboardChange{
		ProjectID: detail.Project.ID, Revision: 2, Generation: 1,
		Kind: ChangeHintCreated, EntityType: "hint", Data: payload, CreatedAt: "2026-01-01T00:00:00Z",
	}
	if err := (&historyChangeStore{}).Append(ctx, conn, change); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE projects SET blackboard_revision = 2 WHERE id = ?", detail.Project.ID,
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()

	replay, err := service.ListChanges(ctx, detail.Project.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.ReplayRequired || replay.CurrentRevision != 2 || len(replay.Changes) != 0 {
		t.Fatalf("byte-bounded replay: %+v", replay)
	}
}
