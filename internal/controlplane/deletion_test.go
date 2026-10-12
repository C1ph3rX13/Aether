package controlplane

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestProjectDeleteUsesProductionCoordinationAndPreservesUnrelatedRun(t *testing.T) {
	application, err := newTestAssembly(
		filepath.Join(t.TempDir(), "aether.db"),
		ControlPlaneHostOptions{OperatorName: "test-operator"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()

	deleted, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "delete through coordination", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "unrelated coordination", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}

	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-delete", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := application.ControlPlane.SchedulerSnapshot(t.Context(), rundomain.SchedulingSnapshotRequest{
		DispatcherID: lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 2,
	})
	if err != nil || len(snapshot.Work) != 2 {
		t.Fatalf("deletion scheduling snapshot: work=%+v error=%v", snapshot.Work, err)
	}

	admitted := make(map[string]*rundomain.AdmittedRun, 2)
	for _, candidate := range snapshot.Work {
		start := rundomain.StartRequest{
			ProjectID: candidate.ProjectID, Task: candidate.Task,
			CandidateGeneration: candidate.Generation, CandidateRevision: candidate.Revision,
			WorkKey: candidate.WorkKey, DispatcherID: lease.InstanceID,
			DispatcherFencingToken: lease.FencingToken,
			Spec: rundomain.AdmissionSpecSnapshot{
				ConfigRevision: "delete-v1", PromptTemplateHash: strings.Repeat("a", 64),
				Worker:       rundomain.WorkerSnapshot{Name: "reasoner", Adapter: "mock"},
				Image:        rundomain.ImageSnapshot{RequestedRef: "worker:v1"},
				Timeouts:     rundomain.TimeoutSnapshot{RunSeconds: 12, PhaseSeconds: 5, GraceSeconds: 1},
				ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: 3,
			},
			RetryPolicy: rundomain.DefaultRetryPolicy(),
		}
		run, startErr := application.ControlPlane.StartRun(t.Context(), start)
		if startErr != nil {
			t.Fatalf("start Run for Project %s: %v", candidate.ProjectID, startErr)
		}
		admitted[candidate.ProjectID] = run
	}
	deletedRun := admitted[deleted.Project.ID]
	unrelatedRun := admitted[unrelated.Project.ID]
	if deletedRun == nil || unrelatedRun == nil {
		t.Fatalf("started Runs: %+v", admitted)
	}

	containerID := "container-delete"
	if err := application.ControlPlane.RecordProjectContainer(t.Context(), rundomain.ProjectContainerRecordRequest{
		Handle: rundomain.ProjectContainerHandle{
			RunID: deletedRun.Run.ID, ProjectID: deleted.Project.ID, ContainerID: containerID,
			RecordedAt: time.Now().UTC(),
		},
		RunFencingToken: deletedRun.Run.FencingToken,
		DispatcherID:    lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
	}); err != nil {
		t.Fatal(err)
	}
	if err := application.ControlPlane.RecordExecutionHandle(t.Context(), rundomain.ExecutionHandleRecordRequest{
		Handle: rundomain.ExecutionHandle{
			RunID: deletedRun.Run.ID, ProjectID: deleted.Project.ID, Phase: rundomain.PhaseReason,
			ContainerID: containerID, ExecID: "exec-delete", StartedAt: time.Now().UTC(),
		},
		RunFencingToken: deletedRun.Run.FencingToken,
		DispatcherID:    lease.InstanceID, DispatcherFencingToken: lease.FencingToken,
	}); err != nil {
		t.Fatal(err)
	}

	receipt, err := application.ProjectService.DeleteConfirmed(t.Context(), deleted.Project.ID, project.DeleteProjectRequest{
		ExpectedTitle: deleted.Project.Title, ExpectedRevision: deleted.Project.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ProjectID != deleted.Project.ID || receipt.ProjectRevision != deleted.Project.Revision ||
		receipt.AffectedRunCount != 1 || receipt.CleanupJobID == "" {
		t.Fatalf("delete receipt: %+v", receipt)
	}

	conn, err := application.DB.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var cleanupProject, cleanupContainer, cleanupStatus string
	if err := conn.QueryRowContext(t.Context(), `
		SELECT project_id, container_id, status
		FROM project_cleanup_jobs WHERE id = ?`, receipt.CleanupJobID).
		Scan(&cleanupProject, &cleanupContainer, &cleanupStatus); err != nil {
		t.Fatal(err)
	}
	if cleanupProject != deleted.Project.ID || cleanupContainer != containerID || cleanupStatus != "pending" {
		t.Fatalf("cleanup job = project %q container %q status %q", cleanupProject, cleanupContainer, cleanupStatus)
	}
	var cleanupRun, cleanupExec string
	if err := conn.QueryRowContext(t.Context(), `
		SELECT run_id, exec_id
		FROM project_cleanup_handles WHERE job_id = ?`, receipt.CleanupJobID).
		Scan(&cleanupRun, &cleanupExec); err != nil {
		t.Fatal(err)
	}
	if cleanupRun != string(deletedRun.Run.ID) || cleanupExec != "exec-delete" {
		t.Fatalf("cleanup handle = run %q exec %q", cleanupRun, cleanupExec)
	}
	var deletedProjects, deletedRuns, deletedClaims int
	if err := conn.QueryRowContext(t.Context(), `
		SELECT
			(SELECT COUNT(*) FROM projects WHERE id = ?),
			(SELECT COUNT(*) FROM execution_runs WHERE id = ?),
			(SELECT COUNT(*) FROM execution_claims WHERE run_id = ?)`,
		deleted.Project.ID, deletedRun.Run.ID, deletedRun.Run.ID).
		Scan(&deletedProjects, &deletedRuns, &deletedClaims); err != nil {
		t.Fatal(err)
	}
	if deletedProjects != 0 || deletedRuns != 0 || deletedClaims != 0 {
		t.Fatalf("deleted Project cascade: projects=%d runs=%d claims=%d", deletedProjects, deletedRuns, deletedClaims)
	}
	var unrelatedStatus string
	if err := conn.QueryRowContext(t.Context(),
		"SELECT status FROM execution_runs WHERE id = ?", unrelatedRun.Run.ID).Scan(&unrelatedStatus); err != nil {
		t.Fatal(err)
	}
	if unrelatedStatus != string(rundomain.StatusActive) {
		t.Fatalf("unrelated Run status = %q", unrelatedStatus)
	}
	var unrelatedClaims int
	if err := conn.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM execution_claims WHERE run_id = ?", unrelatedRun.Run.ID).Scan(&unrelatedClaims); err != nil {
		t.Fatal(err)
	}
	if unrelatedClaims != 1 {
		t.Fatalf("unrelated Run claims = %d", unrelatedClaims)
	}
}
