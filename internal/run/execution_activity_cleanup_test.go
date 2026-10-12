package run_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestProjectActivityPaginatesFiltersAndRedactsRunLedger(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "activity", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runService.StartRun(t.Context(), reasonStartRequest(created.Project))
	if err != nil {
		t.Fatal(err)
	}
	paused, err := projectService.PauseProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := projectService.ResumeProject(t.Context(), created.Project.ID, ProjectLifecycleRequest{ExpectedRevision: paused.Revision})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runService.StartRun(t.Context(), reasonStartRequest(resumed))
	if err != nil {
		t.Fatal(err)
	}

	page, err := runService.ListProjectActivity(t.Context(), created.Project.ID, rundomain.ActivityQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 1 || page.NextCursor == nil {
		t.Fatalf("first Activity page: %+v", page)
	}
	next, err := runService.ListProjectActivity(t.Context(), created.Project.ID, rundomain.ActivityQuery{Limit: 1, Cursor: *page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Runs) != 1 || next.Runs[0].ID == page.Runs[0].ID {
		t.Fatalf("second Activity page: %+v", next)
	}
	seen := map[rundomain.RunID]bool{page.Runs[0].ID: true, next.Runs[0].ID: true}
	if !seen[first.Run.ID] || !seen[second.Run.ID] {
		t.Fatalf("Activity pages omitted Runs: first=%s second=%s pages=%+v/%+v", first.Run.ID, second.Run.ID, page, next)
	}

	active, err := runService.ListProjectActivity(t.Context(), created.Project.ID, rundomain.ActivityQuery{
		View: rundomain.ActivityActive, Task: rundomain.TaskReason, Search: "reasoner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(active.Runs) != 1 || active.Runs[0].ID != second.Run.ID || active.Runs[0].Status != rundomain.StatusActive {
		t.Fatalf("active Activity filter: %+v", active)
	}
	detail, err := runService.GetProjectActivityRun(t.Context(), created.Project.ID, first.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != rundomain.StatusCancelled || detail.FinishedAt == nil || detail.DurationMS == nil {
		t.Fatalf("cancelled Activity detail: %+v", detail)
	}
	evidence, err := runService.GetProjectActivityRunDetail(t.Context(), created.Project.ID, first.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ExecutionSpecSchema != rundomain.ExecutionSpecSchemaV2 ||
		evidence.InputEvidence != first.Run.Spec.Input.Identity || evidence.InputEvidence.ValidateV1() != nil {
		t.Fatalf("Activity Input Evidence: got=%+v want=%+v", evidence.InputEvidence, first.Run.Spec.Input.Identity)
	}
	if evidence.PromptEvidence != first.Run.Spec.PromptEvidence() ||
		evidence.PromptEvidence.Schema != rundomain.PromptTemplateIdentitySchemaV1 {
		t.Fatalf("Activity Prompt Evidence: got=%+v want=%+v", evidence.PromptEvidence, first.Run.Spec.PromptEvidence())
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(evidenceJSON), `"input_evidence":{"schema":"aether.input-hash/v1","sha256":"`) ||
		!strings.Contains(string(evidenceJSON), `"prompt_evidence":{"schema":"aether.prompt-template-hash/v1","sha256":"`) ||
		strings.Contains(string(evidenceJSON), `"input_hash"`) || strings.Contains(string(evidenceJSON), `"prompt_template_hash"`) {
		t.Fatalf("Activity Input Identity JSON: %s", evidenceJSON)
	}
	chain, err := runService.GetProjectRetryChain(t.Context(), created.Project.ID, first.Run.ID)
	if err != nil || len(chain) != 1 || chain[0].ID != first.Run.ID {
		t.Fatalf("retry chain=%+v err=%v", chain, err)
	}

	payload, err := json.Marshal(active.Runs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"execution_spec", "prompt", "environment", "output", "secret"} {
		if strings.Contains(strings.ToLower(string(payload)), forbidden) {
			t.Fatalf("Activity response leaked %q: %s", forbidden, payload)
		}
	}
	if _, err := runService.ListProjectActivity(t.Context(), created.Project.ID, rundomain.ActivityQuery{Cursor: "not-a-cursor"}); err == nil {
		t.Fatal("invalid Activity cursor was accepted")
	}
}

func TestProjectCleanupJobIsFencedRetryableAndIdempotent(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "cleanup", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(created.Project))
	if err != nil {
		t.Fatal(err)
	}
	handle := rundomain.ExecutionHandle{
		RunID: started.Run.ID, ProjectID: created.Project.ID, Phase: rundomain.PhaseReason,
		ContainerID: "container-1", ExecID: "exec-1", StartedAt: time.Now().UTC(),
	}
	if err := runService.RecordExecutionHandle(t.Context(), rundomain.ExecutionHandleRecordRequest{
		Handle: handle, RunFencingToken: started.Run.FencingToken,
		DispatcherID: "dispatcher-a", DispatcherFencingToken: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := runService.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := runService.QueueProjectCleanup(t.Context(), tx, created.Project.ID, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	assertRunErrorCode(t, rundomain.ErrorFenced, func() error {
		_, leaseErr := runService.LeaseProjectCleanupJobs(t.Context(), rundomain.CleanupLeaseRequest{
			DispatcherID: "dispatcher-a", DispatcherFencingToken: 2,
		})
		return leaseErr
	})
	jobs, err := runService.LeaseProjectCleanupJobs(t.Context(), rundomain.CleanupLeaseRequest{
		DispatcherID: "dispatcher-a", DispatcherFencingToken: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != jobID || jobs[0].ContainerID != "container-1" || len(jobs[0].Handles) != 1 || jobs[0].Handles[0].ExecID != "exec-1" || jobs[0].Attempts != 1 {
		t.Fatalf("leased cleanup job: %+v", jobs)
	}
	if err := runService.CompleteProjectCleanupJob(t.Context(), rundomain.CleanupCompletionRequest{
		JobID: jobID, DispatcherID: "dispatcher-a", DispatcherFencingToken: 1, Error: "Docker unavailable",
	}); err != nil {
		t.Fatal(err)
	}
	jobs, err = runService.LeaseProjectCleanupJobs(t.Context(), rundomain.CleanupLeaseRequest{
		DispatcherID: "dispatcher-a", DispatcherFencingToken: 1,
	})
	if err != nil || len(jobs) != 1 || jobs[0].Attempts != 2 {
		t.Fatalf("retried cleanup job=%+v err=%v", jobs, err)
	}
	completion := rundomain.CleanupCompletionRequest{
		JobID: jobID, DispatcherID: "dispatcher-a", DispatcherFencingToken: 1,
	}
	if err := runService.CompleteProjectCleanupJob(t.Context(), completion); err != nil {
		t.Fatal(err)
	}
	if err := runService.CompleteProjectCleanupJob(t.Context(), completion); err != nil {
		t.Fatalf("idempotent cleanup completion: %v", err)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := conn.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM project_cleanup_jobs WHERE id = ?", jobID,
	).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("completed cleanup job remains in database: %d", remaining)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	jobs, err = runService.LeaseProjectCleanupJobs(t.Context(), rundomain.CleanupLeaseRequest{
		DispatcherID: "dispatcher-a", DispatcherFencingToken: 1,
	})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("completed cleanup jobs=%+v err=%v", jobs, err)
	}
}

func TestProjectCleanupJobRetainsContainerAfterExecutionHandleStops(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "stopped cleanup", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(created.Project))
	if err != nil {
		t.Fatal(err)
	}
	handle := rundomain.ExecutionHandle{
		RunID: started.Run.ID, ProjectID: created.Project.ID, Phase: rundomain.PhaseReason,
		ContainerID: "container-stopped", ExecID: "exec-stopped", StartedAt: time.Now().UTC(),
	}
	if err := runService.RecordExecutionHandle(t.Context(), rundomain.ExecutionHandleRecordRequest{
		Handle: handle, RunFencingToken: started.Run.FencingToken,
		DispatcherID: "dispatcher-a", DispatcherFencingToken: 1,
	}); err != nil {
		t.Fatal(err)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE execution_runtime_handles SET status = 'stopped' WHERE exec_id = ?`, handle.ExecID); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	tx, err := runService.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runService.QueueProjectCleanup(t.Context(), tx, created.Project.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	jobs, err := runService.LeaseProjectCleanupJobs(t.Context(), rundomain.CleanupLeaseRequest{
		DispatcherID: "dispatcher-a", DispatcherFencingToken: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ContainerID != "container-stopped" || len(jobs[0].Handles) != 0 {
		t.Fatalf("stopped-handle cleanup evidence: %+v", jobs)
	}
}

func TestProjectCleanupJobUsesContainerRecordedBeforeFirstExec(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "pre-Exec cleanup", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(created.Project))
	if err != nil {
		t.Fatal(err)
	}
	if err := runService.RecordProjectContainer(t.Context(), rundomain.ProjectContainerRecordRequest{
		Handle: rundomain.ProjectContainerHandle{
			RunID: started.Run.ID, ProjectID: created.Project.ID, ContainerID: "container-before-exec",
		},
		RunFencingToken: started.Run.FencingToken, DispatcherID: started.Run.DispatcherID,
		DispatcherFencingToken: started.Run.DispatcherFencingToken,
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := runService.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runService.QueueProjectCleanup(t.Context(), tx, created.Project.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	jobs, err := runService.LeaseProjectCleanupJobs(t.Context(), rundomain.CleanupLeaseRequest{
		DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: started.Run.DispatcherFencingToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ContainerID != "container-before-exec" || len(jobs[0].Handles) != 0 {
		t.Fatalf("pre-Exec cleanup evidence: %+v", jobs)
	}
}
