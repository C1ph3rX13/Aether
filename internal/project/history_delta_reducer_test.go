package project

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/database"
)

func TestDeltaHistoryReplaysTypedProjectChanges(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Delta history", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
		Content: "bounded replay", ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}

	history := readHistoryRevisionForTest(t, service, created.Project.ID, 2)
	detail, ok := history.Evidence.(HistoryTransitionEvidence)
	if !ok || detail.Before == nil || len(detail.Before.Hints) != 0 || len(detail.After.Hints) != 1 {
		t.Fatalf("reconstructed history detail = %+v", history)
	}
	if detail.After.Hints[0].Content != "bounded replay" {
		t.Fatalf("reconstructed after image omitted the new Hint: %+v", detail.After)
	}

	replay, err := service.ListChanges(t.Context(), created.Project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Changes) != 2 || replay.Changes[1].PayloadSchema != DeltaHistorySchema {
		t.Fatalf("delta replay metadata = %+v", replay)
	}

	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(),
		"UPDATE blackboard_history_retention_operations SET active = 0 WHERE operation_id = 'test-history-mutation'"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		UPDATE blackboard_changes SET source_name = 'tampered'
		WHERE project_id = ? AND revision = 2`, created.Project.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("history metadata mutation was not rejected: %v", err)
	}
	if _, err := conn.ExecContext(t.Context(),
		"DELETE FROM blackboard_changes WHERE project_id = ? AND revision = 2", created.Project.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("history deletion was not rejected: %v", err)
	}
	if _, err := conn.ExecContext(t.Context(),
		"DELETE FROM project_history_snapshots WHERE project_id = ? AND revision = 1", created.Project.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("snapshot deletion was not rejected: %v", err)
	}
}

func TestGenerationSnapshotStoresFullProjectState(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Snapshot state", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.CompleteProject(t.Context(), created.Project.ID, CompleteRequest{
		From: []string{"origin"}, Description: "complete generation", ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReopenProject(t.Context(), created.Project.ID, ReopenRequest{
		Feedback: "new evidence", ExpectedRevision: completed.ProjectRevision,
	}); err != nil {
		t.Fatal(err)
	}

	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var stateJSON []byte
	if err := conn.QueryRowContext(t.Context(), `
		SELECT state_json FROM project_history_snapshots
		WHERE project_id = ? AND revision = 3`, created.Project.ID).Scan(&stateJSON); err != nil {
		t.Fatal(err)
	}
	var snapshot ProjectDetail
	if err := json.Unmarshal(stateJSON, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Facts) != 2 || len(snapshot.Intents) != 1 || len(snapshot.Hints) != 1 || len(snapshot.CompletionRecords) != 1 {
		t.Fatalf("generation snapshot is not a full Project state: %+v", snapshot)
	}
}

func TestDeltaHistoryDetectsHashChainMutation(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Hash chain", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
		Content: "evidence", ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		UPDATE blackboard_changes
		SET delta = json_set(delta, '$.project.after.revision', 99)
		WHERE project_id = ? AND revision = 2`, created.Project.ID); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := NewHistoryQueries().ReadRevisionSnapshot(t.Context(), tx, created.Project.ID, 2); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("history mutation was not detected by the hash chain: %v", err)
	}
}

func TestPeriodicSnapshotBoundsHistoryReconstruction(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Revision 1", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	for revision := int64(1); revision <= 100; revision++ {
		if _, err := service.UpdateTitle(t.Context(), created.Project.ID, UpdateProjectTitleRequest{
			Title: "Revision " + fmt.Sprint(revision+1), ExpectedRevision: revision,
		}); err != nil {
			t.Fatalf("update at revision %d: %v", revision, err)
		}
	}

	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var snapshotKind string
	if err := conn.QueryRowContext(t.Context(), `
		SELECT snapshot_kind FROM project_history_snapshots
		WHERE project_id = ? AND revision = 100`, created.Project.ID).Scan(&snapshotKind); err != nil {
		t.Fatal(err)
	}
	if snapshotKind != historySnapshotPeriodic {
		t.Fatalf("snapshot kind = %q, want %q", snapshotKind, historySnapshotPeriodic)
	}
	history := readHistoryRevisionForTest(t, service, created.Project.ID, 101)
	detail, ok := history.Evidence.(HistoryTransitionEvidence)
	if !ok || detail.Before == nil || detail.Before.Project.Title != "Revision 100" ||
		detail.After.Project.Title != "Revision 101" {
		t.Fatalf("periodic snapshot reconstruction = %+v", history)
	}
}

func TestInitializeHistoryBackfillsMissingCutoverSnapshot(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "aether.db")
	initial := database.NewDB()
	if err := initial.Configure(databasePath); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(initial, noopProjectEventPublisher{}, &projectExecutionTestAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Cutover", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		initial.Close()
		t.Fatal(err)
	}
	conn, err := initial.GetConn(t.Context())
	if err != nil {
		initial.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		INSERT INTO blackboard_history_retention_operations (operation_id, active, created_at)
		VALUES ('test-cutover-reset', 1, ?)`, UTCNow()); err != nil {
		conn.Close()
		initial.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(),
		"DELETE FROM project_history_snapshots WHERE project_id = ?", created.Project.ID); err != nil {
		conn.Close()
		initial.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		initial.Close()
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := database.NewDB()
	if err := reopened.Configure(databasePath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened database: %v", err)
		}
	})
	conn, err = reopened.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var snapshotCount int
	if err := conn.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM project_history_snapshots WHERE project_id = ?`, created.Project.ID).
		Scan(&snapshotCount); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != 0 {
		t.Fatalf("database Configure initialized %d Project History snapshots, want 0", snapshotCount)
	}
	uninitializedService, err := NewService(reopened, noopProjectEventPublisher{}, &projectExecutionTestAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uninitializedService.UpdateTitle(t.Context(), created.Project.ID, UpdateProjectTitleRequest{
		Title: "must not repair history", ExpectedRevision: created.Project.Revision,
	}); err == nil || !strings.Contains(err.Error(), "Project History is not initialized") {
		t.Fatalf("write without Project History initialization error = %v", err)
	}
	if err := InitializeHistory(t.Context(), reopened); err != nil {
		t.Fatal(err)
	}
	conn, err = reopened.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var revision int64
	var kind, stateHash string
	if err := conn.QueryRowContext(t.Context(), `
		SELECT revision, snapshot_kind, state_hash
		FROM project_history_snapshots WHERE project_id = ?`, created.Project.ID).
		Scan(&revision, &kind, &stateHash); err != nil {
		t.Fatal(err)
	}
	if revision != created.Project.Revision || kind != "cutover" || len(stateHash) != 64 {
		t.Fatalf("startup cutover = revision %d kind %q hash %q", revision, kind, stateHash)
	}
}
