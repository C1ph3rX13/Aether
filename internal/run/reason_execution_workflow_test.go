package run_test

import (
	"context"
	"strings"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/controlplane/input"
	projectdomain "github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/testutil"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

func TestReasonRunWorkflowNoChangeAdvancesCheckpoint(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{Title: "quiet", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}

	started, executor, runtime := startReasonWorkflow(t, ctx, runService, project,
		`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`)
	completion := executor.Execute(ctx, started.StartedRun)
	assertReasonRunActive(t, runtime, started.Run.ID)
	finishRequest, err := completion.FinishRequest()
	if err != nil {
		t.Fatalf("completion=%+v: %v", completion, err)
	}
	receipt, err := runService.FinishRun(ctx, finishRequest)
	if err != nil {
		t.Fatal(err)
	}
	completeReasonRun(t, ctx, runtime, started.Run.ID)
	if receipt.Status != rundomain.StatusCommitted || receipt.OutcomeKind != rundomain.OutcomeNoChange || receipt.ProjectRevision != 1 {
		t.Fatalf("receipt: %+v", receipt)
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Revision != 1 || len(detail.Intents) != 0 || len(detail.Hints) != 0 {
		t.Fatalf("no_change mutated Blackboard: %+v", detail)
	}

	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var revision int64
	var runID string
	if err := conn.QueryRowContext(ctx,
		"SELECT input_revision, run_id FROM reason_checkpoints WHERE project_id = ? AND generation = ?",
		project.Project.ID, project.Project.Generation,
	).Scan(&revision, &runID); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || runID != string(started.Run.ID) {
		t.Fatalf("checkpoint revision=%d run=%q", revision, runID)
	}
	assertReasonRunCompleted(t, runtime, started.Run.ID)
}

func TestReasonRunWorkflowCommitsOnlyReadSourceFacts(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{Title: "source", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	candidate := `{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"from":["origin"],"description":"investigate the origin"}]}}`
	started, executor, runtime := startReasonWorkflow(t, ctx, runService, project, candidate)
	completion := executor.Execute(ctx, started.StartedRun)
	assertReasonRunActive(t, runtime, started.Run.ID)
	finishRequest, err := completion.FinishRequest()
	if err != nil {
		t.Fatalf("completion=%+v: %v", completion, err)
	}
	receipt, err := runService.FinishRun(ctx, finishRequest)
	if err != nil {
		t.Fatal(err)
	}
	completeReasonRun(t, ctx, runtime, started.Run.ID)
	if receipt.Status != rundomain.StatusCommitted || len(receipt.CreatedIntentIDs) != 1 || receipt.ProjectRevision != 2 {
		t.Fatalf("receipt: %+v", receipt)
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Intents) != 1 || len(detail.Intents[0].From) != 1 || detail.Intents[0].From[0] != "origin" {
		t.Fatalf("committed intent sources: %+v", detail.Intents)
	}
	assertReasonRunCompleted(t, runtime, started.Run.ID)
}

func TestReasonRunWorkflowMarksChangedInputStale(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{Title: "stale", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	candidate := `{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"from":["origin"],"description":"obsolete work"}]}}`
	started, executor, runtime := startReasonWorkflow(t, ctx, runService, project, candidate)
	if _, err := projectService.UpdateTitle(ctx, project.Project.ID, projectdomain.UpdateProjectTitleRequest{
		Title: "new title", ExpectedRevision: project.Project.Revision,
	}); err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(ctx, started.StartedRun)
	assertReasonRunActive(t, runtime, started.Run.ID)
	finishRequest, err := completion.FinishRequest()
	if err != nil {
		t.Fatalf("completion=%+v: %v", completion, err)
	}
	receipt, err := runService.FinishRun(ctx, finishRequest)
	if err != nil {
		t.Fatal(err)
	}
	completeReasonRun(t, ctx, runtime, started.Run.ID)
	if receipt.Status != rundomain.StatusStale || receipt.Code != rundomain.ErrorStaleInput || receipt.ProjectRevision != 2 {
		t.Fatalf("receipt: %+v", receipt)
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Revision != 2 || detail.Project.Title != "new title" || len(detail.Hints) != 0 || len(detail.Intents) != 0 {
		t.Fatalf("stale result mutated Blackboard: %+v", detail)
	}
	assertReasonRunCompleted(t, runtime, started.Run.ID)
}

func startReasonWorkflow(
	t *testing.T,
	ctx context.Context,
	runService *RunService,
	project ProjectDetail,
	candidate string,
) (*rundomain.AdmittedRun, rundomain.Executor, *testutil.MemoryRuntime) {
	t.Helper()
	blackboardInput, err := input.BlackboardInputForRun(projectdomain.ExecutionInputProjectionFromDetail(project), nil)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Assess the immutable Blackboard and return one Reason result."
	catalog := newWorkflowExecutionCatalog(t, "reasoner", rundomain.TaskReason,
		map[string]string{"MOCK_REASON": `{"delay":[0,0],"outcomes":{"noop":1}}`}, prompt, "")
	promptHash, err := catalog.PromptTemplateHash(rundomain.TaskReason)
	if err != nil {
		t.Fatal(err)
	}
	workKey, err := rundomain.WorkKeyFor(project.Project.ID, rundomain.TaskReason, rundomain.InputSnapshot{
		Generation: project.Project.Generation, Revision: project.Project.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := rundomain.StartRequest{
		ProjectID: project.Project.ID, Task: rundomain.TaskReason, DispatcherID: "dispatcher-a",
		DispatcherFencingToken: 1, CandidateGeneration: project.Project.Generation,
		CandidateRevision: project.Project.Revision, WorkKey: workKey,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: "config-v1", PromptTemplateHash: promptHash,
			Worker: rundomain.WorkerSnapshot{Name: "reasoner", Adapter: "mock", Capabilities: []string{"reason"}},
			Image: rundomain.ImageSnapshot{
				RequestedRef: "aether-agent:latest", ImageID: "sha256:test-image", RepoDigest: "aether-agent@sha256:test",
			},
			Timeouts:     rundomain.TimeoutSnapshot{RunSeconds: 12, PhaseSeconds: 5, GraceSeconds: 1},
			ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: 3,
		},
	}
	started, err := runService.StartRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newWorkflowRuntime(t)
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseReason, workflowOutput(candidate))
	executor := bindWorkflowExecution(t, runtime, started.StartedRun, catalog, worker.ExecutionBindingRequest{
		Task: rundomain.TaskReason, WorkerName: "reasoner", BlackboardInput: &blackboardInput, MaxIntents: 3,
	})
	return started, executor, runtime
}

func assertReasonRunActive(t *testing.T, runtime *testutil.MemoryRuntime, runID rundomain.RunID) {
	t.Helper()
	snapshot := runtime.Snapshot()
	if len(snapshot.ActiveRunIDs) != 1 || snapshot.ActiveRunIDs[runID] == "" || len(snapshot.CompletedRunIDs) != 0 {
		t.Fatalf("Run execution was not retained through FinishRun: %+v", snapshot)
	}
}

func completeReasonRun(t *testing.T, ctx context.Context, runtime *testutil.MemoryRuntime, runID rundomain.RunID) {
	t.Helper()
	if err := runtime.CompleteRun(ctx, runID); err != nil {
		t.Fatal(err)
	}
}

func assertReasonRunCompleted(t *testing.T, runtime *testutil.MemoryRuntime, runID rundomain.RunID) {
	t.Helper()
	snapshot := runtime.Snapshot()
	if len(snapshot.ActiveRunIDs) != 0 || len(snapshot.CompletedRunIDs) != 1 || snapshot.CompletedRunIDs[0] != runID ||
		len(snapshot.Launches) != 1 || !strings.Contains(string(snapshot.Launches[0].RenderedInput.Bytes), "revision: 1") {
		t.Fatalf("runtime snapshot: %+v", snapshot)
	}
}
