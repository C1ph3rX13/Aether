package project

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestProjectLifecyclePreservesGenerationsAndAdvancesRevision(t *testing.T) {
	service, db := newProjectTestService(t)
	ctx := t.Context()

	detail, err := service.Create(ctx, CreateProjectRequest{
		Title: "Lifecycle", Origin: "origin", Goal: "goal", BootstrapEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Generation != 1 || detail.Project.Revision != 1 {
		t.Fatalf("created project version: generation=%d revision=%d", detail.Project.Generation, detail.Project.Revision)
	}

	paused, err := service.PauseProject(ctx, detail.Project.ID, ProjectLifecycleRequest{ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != ProjectStatusPaused || paused.Revision != 2 {
		t.Fatalf("paused project: status=%s revision=%d", paused.Status, paused.Revision)
	}
	resumed, err := service.ResumeProject(ctx, detail.Project.ID, ProjectLifecycleRequest{ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != ProjectStatusActive || resumed.Revision != 3 {
		t.Fatalf("resumed project: status=%s revision=%d", resumed.Status, resumed.Revision)
	}

	createdIntent := proposeTestIntent(t, service, db, detail.Project.ID, "investigate", []string{"origin"})
	_, concludedFact := concludeTestIntent(t, service, db, detail.Project.ID, createdIntent.ID, "finding")

	first, err := service.CompleteProject(ctx, detail.Project.ID, CompleteRequest{
		From: []string{concludedFact.ID}, Description: "first result", ExpectedRevision: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Project.Generation != 1 || first.Project.Revision != 6 || first.Completion.IntentID != first.Intent.ID {
		t.Fatalf("first completion: project=%+v completion=%+v intent=%s", first.Project, first.Completion, first.Intent.ID)
	}

	reopened, err := service.ReopenProject(ctx, detail.Project.ID, ReopenRequest{
		Feedback: "new evidence", ExpectedRevision: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Project.Generation != 2 || reopened.Project.Revision != 7 || reopened.Project.Status != ProjectStatusActive {
		t.Fatalf("reopened project: %+v", reopened.Project)
	}
	if reopened.Hint.Content != "new evidence" || reopened.Hint.Generation != 2 || reopened.PreviousCompletion != first.Completion {
		t.Fatalf("reopen result: %+v", reopened)
	}

	second, err := service.CompleteProject(ctx, detail.Project.ID, CompleteRequest{
		From: []string{concludedFact.ID}, Description: "second result", ExpectedRevision: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Project.Generation != 2 || second.Project.Revision != 8 {
		t.Fatalf("second completion: %+v", second.Project)
	}

	got, err := service.Get(ctx, detail.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CompletionRecords) != 2 || got.CompletionRecords[0] != first.Completion || got.CompletionRecords[1] != second.Completion {
		t.Fatalf("completion history: %+v", got.CompletionRecords)
	}
	if len(got.Intents) != 3 || len(got.Hints) != 1 || len(got.Facts) != 3 {
		t.Fatalf("blackboard history changed: facts=%d intents=%d hints=%d", len(got.Facts), len(got.Intents), len(got.Hints))
	}
	replay, err := service.ListChanges(ctx, detail.Project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []ChangeKind{
		ChangeProjectCreated,
		ChangeProjectPaused,
		ChangeProjectResumed,
		ChangeIntentsProposed,
		ChangeIntentConcluded,
		ChangeProjectCompleted,
		ChangeProjectReopened,
		ChangeProjectCompleted,
	}
	if len(replay.Changes) != len(wantKinds) {
		t.Fatalf("lifecycle change log: %+v", replay)
	}
	for index, wantKind := range wantKinds {
		if replay.Changes[index].Revision != int64(index+1) || replay.Changes[index].Kind != wantKind {
			t.Fatalf("lifecycle change %d: %+v, want %s", index, replay.Changes[index], wantKind)
		}
	}
	for _, index := range []int{1, 2} {
		var payload struct {
			SchemaVersion int          `json:"schema_version"`
			Source        ChangeSource `json:"source"`
			After         struct {
				Project ProjectMeta `json:"project"`
			} `json:"after"`
		}
		if err := json.Unmarshal(replay.Changes[index].Data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.SchemaVersion != ChangeSchemaVersion || payload.Source.Kind != "operator" ||
			payload.Source.Name != "Operator" || payload.After.Project.Revision != int64(index+1) {
			t.Fatalf("lifecycle payload %d: %+v", index, payload)
		}
	}
}

func TestProjectLifecycleRejectsIllegalTransitionsWithoutMutation(t *testing.T) {
	service, _ := newProjectTestService(t)
	ctx := t.Context()

	detail, err := service.Create(ctx, CreateProjectRequest{Title: "Invalid", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	projectID := detail.Project.ID

	assertLifecycleConflict(t, func() error {
		_, err := service.ResumeProject(ctx, projectID, ProjectLifecycleRequest{ExpectedRevision: 1})
		return err
	})
	if _, err := service.PauseProject(ctx, projectID, ProjectLifecycleRequest{ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	assertLifecycleConflict(t, func() error {
		_, err := service.PauseProject(ctx, projectID, ProjectLifecycleRequest{ExpectedRevision: 2})
		return err
	})
	assertLifecycleConflict(t, func() error {
		_, err := service.CompleteProject(ctx, projectID, CompleteRequest{From: []string{"origin"}, Description: "done", ExpectedRevision: 2})
		return err
	})

	got, err := service.Get(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project.Status != ProjectStatusPaused || got.Project.Revision != 2 || len(got.CompletionRecords) != 0 {
		t.Fatalf("illegal transition mutated project: %+v completions=%+v", got.Project, got.CompletionRecords)
	}
}

func TestFailedBlackboardTransactionDoesNotAdvanceRevision(t *testing.T) {
	service, _ := newProjectTestService(t)
	ctx := t.Context()

	detail, err := service.Create(ctx, CreateProjectRequest{Title: "Rollback", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CompleteProject(ctx, detail.Project.ID, CompleteRequest{
		From: []string{"missing"}, Description: "invalid", ExpectedRevision: 1,
	})
	if err == nil {
		t.Fatal("complete with a missing source fact succeeded")
	}

	got, err := service.Get(ctx, detail.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project.Status != ProjectStatusActive || got.Project.Revision != 1 || len(got.Intents) != 0 || len(got.CompletionRecords) != 0 {
		t.Fatalf("failed transaction mutated project: %+v", got)
	}
	replay, err := service.ListChanges(ctx, detail.Project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Changes) != 1 || replay.Changes[0].Kind != ChangeProjectCreated {
		t.Fatalf("failed transaction leaked change log: %+v", replay)
	}
}

func assertLifecycleConflict(t *testing.T, command func() error) {
	t.Helper()
	err := command()
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != ErrorConflict {
		t.Fatalf("expected lifecycle conflict, got %v", err)
	}
}
