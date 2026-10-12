package project

import (
	"testing"
)

func TestSchedulingReadModelReturnsBoundedProjectOwnedEvidence(t *testing.T) {
	service, db := newProjectTestService(t)

	first, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "first scheduling project", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposeTestIntent(t, service, db, first.Project.ID, "first intent", []string{"origin"})
	proposeTestIntent(t, service, db, first.Project.ID, "second intent", []string{"origin"})

	bootstrap, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "bootstrap scheduling project", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "paused scheduling project", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PauseProject(t.Context(), paused.Project.ID, ProjectLifecycleRequest{
		ExpectedRevision: paused.Project.Revision,
	}); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	model, err := service.ReadSchedulingModel(t.Context(), tx, SchedulingReadRequest{
		ProjectLimit:       2,
		BootstrapSupported: true, ReasonSupported: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(model.Projects) != 2 || model.NextProjectCursor != bootstrap.Project.ID {
		t.Fatalf("scheduling Project window: %+v", model)
	}
	if got := model.Projects[0]; got.ID != first.Project.ID || got.Decision.Kind != SchedulingDecisionReason {
		t.Fatalf("first Project scheduling evidence: %+v", got)
	}
	if got := model.Projects[1]; got.ID != bootstrap.Project.ID ||
		got.Decision.Kind != SchedulingDecisionBootstrap || got.Decision.TargetIntentID != "i001" {
		t.Fatalf("Bootstrap Project scheduling evidence: %+v", got)
	}
	intents, err := service.ReadSchedulingIntents(t.Context(), tx, SchedulingIntentReadRequest{
		ProjectIDs: []string{first.Project.ID}, Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 2 || intents[0].ID != "i001" || intents[1].ID != "i002" {
		t.Fatalf("Open Explore Intent scheduling evidence: %+v", intents)
	}
}

func TestSchedulingIntentReadModelDoesNotInterpretRunEligibility(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Project-owned Intent evidence", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposeTestIntent(t, service, db, created.Project.ID, "still open", []string{"origin"})

	writeTx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeTx.ExecContext(t.Context(), `
		INSERT INTO execution_work_keys
			(work_key, project_id, task_type, target_intent_id, created_at, retry_state)
		VALUES ('test-exhausted-intent', ?, 'explore', 'i001', ?, 'exhausted')`,
		created.Project.ID, UTCNow()); err != nil {
		_ = writeTx.Rollback()
		t.Fatal(err)
	}
	if err := writeTx.Commit(); err != nil {
		t.Fatal(err)
	}

	readTx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()
	intents, err := service.ReadSchedulingIntents(t.Context(), readTx, SchedulingIntentReadRequest{
		ProjectIDs: []string{created.Project.ID}, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].ID != "i001" {
		t.Fatalf("Project scheduling Intent evidence: %+v", intents)
	}
}

func TestRuntimeStatusesPreserveRequestedOrderAndMissingEvidence(t *testing.T) {
	service, db := newProjectTestService(t)
	active, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "active runtime project", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "paused runtime project", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PauseProject(t.Context(), paused.Project.ID, ProjectLifecycleRequest{
		ExpectedRevision: paused.Project.Revision,
	}); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statuses, err := service.ReadRuntimeStatuses(t.Context(), tx, []string{
		paused.Project.ID, "proj_missing", active.Project.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 3 ||
		statuses[0].ProjectID != paused.Project.ID || statuses[0].Status != ProjectStatusPaused || statuses[0].Missing ||
		statuses[1].ProjectID != "proj_missing" || statuses[1].Status != "" || !statuses[1].Missing ||
		statuses[2].ProjectID != active.Project.ID || statuses[2].Status != ProjectStatusActive || statuses[2].Missing {
		t.Fatalf("Project runtime statuses: %+v", statuses)
	}
}
