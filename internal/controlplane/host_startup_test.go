package controlplane

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/project"
)

func TestManagedHostRejectsProjectsWithoutDeploymentConfiguration(t *testing.T) {
	databasePath, projectID := prepareProjectWithoutHistorySnapshot(t, false)
	db := database.NewDB()
	if err := db.Configure(databasePath); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), "UPDATE projects SET config_revision = NULL"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	host, err := NewControlPlaneHost(databasePath, nil, ControlPlaneHostOptions{
		OperatorName: "test-operator", DeploymentID: "test-deployment",
	})
	if host != nil {
		host.Shutdown()
		t.Fatal("production host accepted a Project from before Dynamic Configuration")
	}
	if err == nil || !strings.Contains(err.Error(), "new database") {
		t.Fatalf("startup error = %v, want an explicit new database requirement", err)
	}
	if err := db.Configure(databasePath); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var states, projects int
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM configuration_state").Scan(&states); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM projects WHERE id = ? AND config_revision IS NULL", projectID).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if states != 1 || projects != 1 {
		t.Fatalf("rejected database changed: configuration states=%d, unbound Projects=%d", states, projects)
	}
}

func TestNewControlPlaneHostInitializesProjectHistoryForEachInstanceBeforeReturning(t *testing.T) {
	type applicationDatabase struct {
		path      string
		projectID string
	}
	databases := make([]applicationDatabase, 2)
	for index := range databases {
		databases[index].path, databases[index].projectID = prepareProjectWithoutHistorySnapshot(t, false)
	}

	for _, configured := range databases {
		application, err := NewControlPlaneHost(configured.path, nil, ControlPlaneHostOptions{
			OperatorName: "test-operator", DeploymentID: "test-deployment",
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if shutdownErr := application.Shutdown(); shutdownErr != nil {
				t.Errorf("Control Plane shutdown: %v", shutdownErr)
			}
		})
		assertProjectHistorySnapshot(t, configured.path, configured.projectID)
	}
}

func TestNewControlPlaneHostClosesDatabaseWhenProjectHistoryInitializationFails(t *testing.T) {
	databasePath, _ := prepareProjectWithoutHistorySnapshot(t, true)
	db := database.NewDB()

	application, err := newControlPlaneHostWithDatabase(
		db,
		databasePath,
		nil,
		ControlPlaneHostOptions{OperatorName: "test-operator", DeploymentID: "test-deployment"},
		true,
	)
	if err == nil {
		application.Shutdown()
		t.Fatal("NewControlPlaneHost() error = nil, want Project History initialization failure")
	}
	if application != nil {
		t.Fatalf("NewControlPlaneHost() application = %+v, want nil", application)
	}
	if !strings.Contains(err.Error(), "initialize Project History") ||
		!strings.Contains(err.Error(), "unsupported Project history snapshot schema") {
		t.Fatalf("NewControlPlaneHost() error = %q, want Project History schema context", err)
	}
	if pingErr := db.Ping(t.Context()); pingErr == nil || !strings.Contains(pingErr.Error(), "database not configured") {
		t.Fatalf("database remained usable after startup failure: %v", pingErr)
	}
}

func TestNewControlPlaneHostCommitsProjectHistoryBackfillInBoundedBatches(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "aether.db")
	application, err := newTestAssembly(databasePath, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}

	const projectCount = 33
	projectIDs := make([]string, 0, projectCount)
	for index := 0; index < projectCount; index++ {
		created, createErr := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
			Title: fmt.Sprintf("History batch %02d", index+1), Origin: "origin", Goal: "goal",
		})
		if createErr != nil {
			application.Shutdown()
			t.Fatal(createErr)
		}
		projectIDs = append(projectIDs, created.Project.ID)
	}
	conn, err := application.DB.GetConn(t.Context())
	if err != nil {
		application.Shutdown()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		INSERT INTO blackboard_history_retention_operations (operation_id, active, created_at)
		VALUES ('test-app-startup-batches', 1, ?)`, project.UTCNow()); err != nil {
		conn.Close()
		application.Shutdown()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "DELETE FROM project_history_snapshots"); err != nil {
		conn.Close()
		application.Shutdown()
		t.Fatal(err)
	}
	lastProjectID := projectIDs[len(projectIDs)-1]
	if _, err := conn.ExecContext(t.Context(),
		"UPDATE projects SET generation = 'invalid' WHERE id = ?", lastProjectID); err != nil {
		conn.Close()
		application.Shutdown()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		application.Shutdown()
		t.Fatal(err)
	}
	application.Shutdown()

	failedApplication, err := NewControlPlaneHost(databasePath, nil, ControlPlaneHostOptions{
		OperatorName: "test-operator", DeploymentID: "test-deployment",
	})
	if err == nil {
		failedApplication.Shutdown()
		t.Fatal("NewControlPlaneHost() error = nil, want second-batch Project History failure")
	}
	if failedApplication != nil || !strings.Contains(err.Error(), lastProjectID) {
		t.Fatalf("NewControlPlaneHost() application=%+v error=%v, want final Project context", failedApplication, err)
	}

	db := database.NewDB()
	if err := db.Configure(databasePath); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	readConn, err := db.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer readConn.Close()
	var snapshotCount int
	if err := readConn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM project_history_snapshots").Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != 32 {
		t.Fatalf("committed History snapshots = %d, want first 32-Project batch", snapshotCount)
	}
}

func prepareProjectWithoutHistorySnapshot(t *testing.T, insertInvalidSnapshot bool) (string, string) {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "aether.db")
	application, err := newTestAssembly(databasePath, ControlPlaneHostOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "Startup history", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		application.Shutdown()
		t.Fatal(err)
	}
	conn, err := application.DB.GetConn(t.Context())
	if err != nil {
		application.Shutdown()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		INSERT INTO blackboard_history_retention_operations
			(operation_id, project_id, active, created_at)
		VALUES ('test-app-startup-reset', ?, 1, ?)`, created.Project.ID, project.UTCNow()); err != nil {
		conn.Close()
		application.Shutdown()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(),
		"DELETE FROM project_history_snapshots WHERE project_id = ?", created.Project.ID); err != nil {
		conn.Close()
		application.Shutdown()
		t.Fatal(err)
	}
	if insertInvalidSnapshot {
		if _, err := conn.ExecContext(t.Context(), `
			INSERT INTO project_history_snapshots (
				project_id, revision, generation, snapshot_kind,
				schema, state_json, state_hash, created_at
			) VALUES (?, 1, 1, 'cutover', 'unsupported/v1', '{}', 'invalid', ?)`,
			created.Project.ID, project.UTCNow()); err != nil {
			conn.Close()
			application.Shutdown()
			t.Fatal(err)
		}
	}
	if err := conn.Close(); err != nil {
		application.Shutdown()
		t.Fatal(err)
	}
	application.Shutdown()
	return databasePath, created.Project.ID
}

func assertProjectHistorySnapshot(t *testing.T, databasePath, projectID string) {
	t.Helper()
	db := database.NewDB()
	if err := db.Configure(databasePath); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var revision int64
	var kind, stateHash string
	if err := conn.QueryRowContext(t.Context(), `
		SELECT revision, snapshot_kind, state_hash
		FROM project_history_snapshots WHERE project_id = ?`, projectID).
		Scan(&revision, &kind, &stateHash); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || kind != "cutover" || len(stateHash) != 64 {
		t.Fatalf("startup cutover = revision %d kind %q hash %q", revision, kind, stateHash)
	}
}
