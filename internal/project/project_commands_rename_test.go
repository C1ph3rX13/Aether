package project

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/database"
)

func TestRenameProjectIsRevisionSafeAndRecordsOperatorAfterImage(t *testing.T) {
	service, db := newRenameTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Before", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := service.UpdateTitle(t.Context(), created.Project.ID, UpdateProjectTitleRequest{
		Title: "  After  ", ExpectedRevision: created.Project.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ProjectRevision != 2 || receipt.Project.Revision != 2 || receipt.Project.Title != "After" {
		t.Fatalf("rename receipt = %+v", receipt)
	}

	replay, err := service.ListChanges(t.Context(), created.Project.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Changes) != 1 || replay.Changes[0].Kind != ChangeProjectRenamed || replay.Changes[0].Revision != 2 {
		t.Fatalf("rename change = %+v", replay)
	}
	var payload struct {
		SchemaVersion int          `json:"schema_version"`
		Source        ChangeSource `json:"source"`
		Before        struct {
			Project ProjectMeta `json:"project"`
		} `json:"before"`
		After struct {
			Project ProjectMeta `json:"project"`
		} `json:"after"`
	}
	if err := json.Unmarshal(replay.Changes[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SchemaVersion != ChangeSchemaVersion || payload.Source != (ChangeSource{Kind: "operator", Name: "deployment-operator"}) ||
		payload.Before.Project.Title != "Before" || payload.Before.Project.Revision != 1 ||
		payload.After.Project.Title != "After" || payload.After.Project.Revision != 2 {
		t.Fatalf("rename payload = %+v", payload)
	}

	_, err = service.UpdateTitle(t.Context(), created.Project.ID, UpdateProjectTitleRequest{
		Title: "Stale write", ExpectedRevision: 1,
	})
	var conflict *DomainError
	if !errors.As(err, &conflict) || conflict.Code != ErrorConflict || conflict.CurrentRevision == nil || *conflict.CurrentRevision != 2 {
		t.Fatalf("stale rename error = %#v", err)
	}
	got, err := service.Get(t.Context(), created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project.Title != "After" || got.Project.Revision != 2 {
		t.Fatalf("stale rename mutated Project = %+v", got.Project)
	}

	var changeCount int
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM blackboard_changes WHERE project_id = ?", created.Project.ID,
	).Scan(&changeCount); err != nil {
		t.Fatal(err)
	}
	if changeCount != 2 {
		t.Fatalf("change count after stale rename = %d", changeCount)
	}
}

func TestRenameProjectRollsBackTitleAndRevisionWhenChangeAppendFails(t *testing.T) {
	service, db := newRenameTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Stable", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(t.Context(), `
		CREATE TRIGGER reject_project_rename_change
		BEFORE INSERT ON blackboard_changes
		WHEN NEW.kind = 'project_renamed'
		BEGIN
			SELECT RAISE(ABORT, 'reject rename change');
		END`)
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.UpdateTitle(t.Context(), created.Project.ID, UpdateProjectTitleRequest{
		Title: "Must rollback", ExpectedRevision: created.Project.Revision,
	}); err == nil {
		t.Fatal("rename succeeded despite rejected Change append")
	}
	got, err := service.Get(t.Context(), created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project.Title != "Stable" || got.Project.Revision != 1 {
		t.Fatalf("failed rename leaked state = %+v", got.Project)
	}
}

func newRenameTestService(t *testing.T) (*Service, *database.DB) {
	t.Helper()
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service, err := NewService(db, noopProjectEventPublisher{}, &projectExecutionTestAdapter{}, ServiceOptions{
		OperatorName: "deployment-operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, db
}
