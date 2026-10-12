package run_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/controlplane/input"
	projectdomain "github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/testutil"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

func TestExploreRunWorkflowAllowsUnrelatedBlackboardChange(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{Title: "explore", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	created := proposeIntent(t, projectService, runService, project.Project.ID, "investigate the origin", []string{"origin"})
	current, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	blackboardInput, err := input.BlackboardInputForRun(projectdomain.ExecutionInputProjectionFromDetail(current), &created.ID)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Investigate the target intent from the immutable Blackboard."
	fallbackPrompt := "Summarize confirmed results for the target intent."
	started := startWorkflowRun(t, ctx, runService, current.Project, created.ID, rundomain.TaskExplore, prompt, fallbackPrompt)
	if _, err := projectService.CreateHint(ctx, project.Project.ID, CreateHintRequest{
		Content: "unrelated evidence arrived", ExpectedRevision: 2,
	}); err != nil {
		t.Fatal(err)
	}

	runtime := newWorkflowRuntime(t)
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseExplore,
		workflowOutput(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"origin verified"}}}`))
	catalog := newWorkflowExecutionCatalog(t, "agent", rundomain.TaskExplore, nil, prompt, fallbackPrompt)
	executor := bindWorkflowExecution(t, runtime, started.StartedRun, catalog, worker.ExecutionBindingRequest{
		Task: rundomain.TaskExplore, WorkerName: "agent", BlackboardInput: &blackboardInput,
	})
	completion := executor.Execute(ctx, started.StartedRun)
	receipt := finishWorkflowCompletion(t, ctx, runService, completion)
	if receipt.Status != rundomain.StatusCommitted || receipt.ProjectRevision != 4 || len(receipt.CreatedFactIDs) != 1 {
		t.Fatalf("Explore receipt: %+v", receipt)
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := findIntent(detail.Intents, created.ID)
	if target == nil || target.To == nil || *target.To != receipt.CreatedFactIDs[0] ||
		len(target.From) != 1 || target.From[0] != "origin" || len(detail.Hints) != 1 {
		t.Fatalf("target-local Explore result: %+v", detail)
	}
	assertWorkflowRuntime(t, runtime, started.Run.ID, 1, true)
	disposeWorkflowRuntime(t, ctx, runtime, started.Run.ID)
}

func TestExploreConcludeRecoveryWorkflowCommitsOnlyRecoveryResult(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{Title: "explore recovery", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	created := proposeIntent(t, projectService, runService, project.Project.ID, "investigate the origin", []string{"origin"})
	current, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	blackboardInput, err := input.BlackboardInputForRun(projectdomain.ExecutionInputProjectionFromDetail(current), &created.ID)
	if err != nil {
		t.Fatal(err)
	}
	const (
		prompt                 = "Investigate the target intent from the immutable Blackboard."
		fallbackPrompt         = "Summarize confirmed results for the target intent."
		recoveryPrompt         = "Continue the interrupted Explore task."
		recoveryConcludePrompt = "Do not use tools. Serialize only the confirmed Explore result."
	)
	started := startConcludeRecoveryWorkflowRun(
		t, ctx, runService, current.Project, created.ID, rundomain.TaskExplore,
		prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt,
	)
	runtime := newWorkflowRuntime(t)
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseExplore,
		rundomain.ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseExploreConclude,
		workflowOutputWithExit(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"abnormal conclude candidate"}}}`, 9))
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseExploreConcludeRecovery,
		workflowOutput(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"recovered evidence"}}}`))
	catalog := newConcludeRecoveryWorkflowCatalog(
		t, rundomain.TaskExplore, prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt,
	)
	binding, err := rundomain.NewExecutionBinding(runtime, nil, nil, runService, runService)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := catalog.BindExecution(binding, worker.ExecutionBindingRequest{
		Task: rundomain.TaskExplore, WorkerName: "agent", BlackboardInput: &blackboardInput,
	}, started.StartedRun)
	if err != nil {
		t.Fatal(err)
	}

	receipt := finishWorkflowCompletion(t, ctx, runService, executor.Execute(ctx, started.StartedRun))
	if receipt.Status != rundomain.StatusCommitted || len(receipt.CreatedFactIDs) != 1 {
		t.Fatalf("Explore Conclude Recovery receipt: %+v", receipt)
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := findIntent(detail.Intents, created.ID)
	if target == nil || target.To == nil || *target.To != receipt.CreatedFactIDs[0] ||
		countFactsWithDescription(detail.Facts, "recovered evidence") != 1 ||
		countFactsWithDescription(detail.Facts, "abnormal conclude candidate") != 0 {
		t.Fatalf("Explore Conclude Recovery Blackboard: %+v", detail)
	}
	stored, err := runService.GetRun(ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.RecoveryUsed || stored.RecoveryPhase != rundomain.PhaseExploreConcludeRecovery {
		t.Fatalf("Explore Conclude Recovery persistence: %+v", stored)
	}
	snapshot := runtime.Snapshot()
	launchByPhase := workflowLaunchesByPhase(snapshot.Launches)
	exploreConclude := launchByPhase[rundomain.PhaseExploreConclude]
	exploreRecovery := launchByPhase[rundomain.PhaseExploreConcludeRecovery]
	if len(snapshot.Launches) != 3 || exploreConclude.RenderedInput == nil || exploreRecovery.RenderedInput == nil ||
		exploreConclude.RenderedInput.Path == exploreRecovery.RenderedInput.Path ||
		exploreConclude.RenderedInput.Hash != exploreRecovery.RenderedInput.Hash {
		t.Fatalf("Explore Conclude Recovery launches: %+v", snapshot.Launches)
	}
	disposeWorkflowRuntime(t, ctx, runtime, started.Run.ID)
}

func TestBootstrapConcludeRecoveryWorkflowCommitsPartialFactWithoutCompletingProject(t *testing.T) {
	projectService, runService, current, target := bootstrapWorkflowProject(t)
	defer runService.DB.Close()
	ctx := t.Context()
	bootstrapInput, err := input.BootstrapInputForRun(projectdomain.ExecutionInputProjectionFromDetail(current), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	const (
		prompt                 = "Complete the project when the initial facts are sufficient."
		fallbackPrompt         = "Conclude the reserved Bootstrap intent with the useful partial result."
		recoveryPrompt         = "Continue the interrupted Bootstrap task."
		recoveryConcludePrompt = "Do not use tools. Serialize only the confirmed Bootstrap result."
	)
	started := startConcludeRecoveryWorkflowRun(
		t, ctx, runService, current.Project, target.ID, rundomain.TaskBootstrap,
		prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt,
	)
	runtime := newWorkflowRuntime(t)
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseBootstrap,
		rundomain.ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseBootstrapConclude,
		workflowOutputWithExit(`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"must not commit","fact":{"description":"abnormal Bootstrap candidate"}}}`, 9))
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseBootstrapConcludeRecovery,
		workflowOutput(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"recovered partial evidence"}}}`))
	catalog := newConcludeRecoveryWorkflowCatalog(
		t, rundomain.TaskBootstrap, prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt,
	)
	binding, err := rundomain.NewExecutionBinding(runtime, nil, nil, runService, runService)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := catalog.BindExecution(binding, worker.ExecutionBindingRequest{
		Task: rundomain.TaskBootstrap, WorkerName: "agent", BootstrapInput: &bootstrapInput,
	}, started.StartedRun)
	if err != nil {
		t.Fatal(err)
	}

	receipt := finishWorkflowCompletion(t, ctx, runService, executor.Execute(ctx, started.StartedRun))
	detail, err := projectService.Get(ctx, current.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	concluded := findIntent(detail.Intents, target.ID)
	if receipt.Status != rundomain.StatusCommitted || len(receipt.CreatedFactIDs) != 1 || receipt.CompletionIntentID != "" ||
		detail.Project.Status != ProjectStatusActive || len(detail.CompletionRecords) != 0 ||
		concluded == nil || concluded.To == nil || *concluded.To != receipt.CreatedFactIDs[0] ||
		countFactsWithDescription(detail.Facts, "recovered partial evidence") != 1 ||
		countFactsWithDescription(detail.Facts, "abnormal Bootstrap candidate") != 0 {
		t.Fatalf("Bootstrap Conclude Recovery: receipt=%+v detail=%+v", receipt, detail)
	}
	stored, err := runService.GetRun(ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.RecoveryUsed || stored.RecoveryPhase != rundomain.PhaseBootstrapConcludeRecovery {
		t.Fatalf("Bootstrap Conclude Recovery persistence: %+v", stored)
	}
	snapshot := runtime.Snapshot()
	launchByPhase := workflowLaunchesByPhase(snapshot.Launches)
	bootstrapConclude := launchByPhase[rundomain.PhaseBootstrapConclude]
	bootstrapRecovery := launchByPhase[rundomain.PhaseBootstrapConcludeRecovery]
	if len(snapshot.Launches) != 3 || bootstrapConclude.RenderedInput == nil || bootstrapRecovery.RenderedInput == nil ||
		bootstrapConclude.RenderedInput.Path == bootstrapRecovery.RenderedInput.Path ||
		bootstrapConclude.RenderedInput.Hash != bootstrapRecovery.RenderedInput.Hash {
		t.Fatalf("Bootstrap Conclude Recovery launches: %+v", snapshot.Launches)
	}
	disposeWorkflowRuntime(t, ctx, runtime, started.Run.ID)
}

func TestBootstrapRunWorkflowCompletesProjectAtomically(t *testing.T) {
	projectService, runService, current, target := bootstrapWorkflowProject(t)
	defer runService.DB.Close()
	ctx := t.Context()
	bootstrapInput, err := input.BootstrapInputForRun(projectdomain.ExecutionInputProjectionFromDetail(current), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Complete the project when the initial facts are sufficient."
	fallbackPrompt := "Conclude the reserved Bootstrap intent with the useful partial result."
	started := startWorkflowRun(t, ctx, runService, current.Project, target.ID, rundomain.TaskBootstrap, prompt, fallbackPrompt)
	runtime := newWorkflowRuntime(t)
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseBootstrap,
		workflowOutput(`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"goal reached","fact":{"description":"final answer"}}}`))
	executor := newBootstrapWorkflowExecutor(t, runtime, started.StartedRun, prompt, fallbackPrompt, bootstrapInput)

	receipt := finishWorkflowCompletion(t, ctx, runService, executor.Execute(ctx, started.StartedRun))
	if receipt.Status != rundomain.StatusCommitted || receipt.ProjectRevision != 2 ||
		len(receipt.CreatedFactIDs) != 1 || receipt.CompletionIntentID == "" {
		t.Fatalf("Bootstrap receipt: %+v", receipt)
	}
	detail, err := projectService.Get(ctx, current.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	concluded := findIntent(detail.Intents, target.ID)
	if detail.Project.Status != ProjectStatusCompleted || concluded == nil || concluded.To == nil ||
		*concluded.To != receipt.CreatedFactIDs[0] || len(detail.CompletionRecords) != 1 {
		t.Fatalf("atomic Bootstrap result: %+v", detail)
	}
	assertWorkflowRuntime(t, runtime, started.Run.ID, 1, false)
	disposeWorkflowRuntime(t, ctx, runtime, started.Run.ID)
}

func TestBootstrapRunWorkflowFallsBackInSameRunSession(t *testing.T) {
	projectService, runService, current, target := bootstrapWorkflowProject(t)
	defer runService.DB.Close()
	ctx := t.Context()
	bootstrapInput, err := input.BootstrapInputForRun(projectdomain.ExecutionInputProjectionFromDetail(current), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Complete the project when the initial facts are sufficient."
	fallbackPrompt := "Conclude the reserved Bootstrap intent with the useful partial result."
	started := startWorkflowRun(t, ctx, runService, current.Project, target.ID, rundomain.TaskBootstrap, prompt, fallbackPrompt)
	runtime := newWorkflowRuntime(t)
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseBootstrap,
		rundomain.ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseBootstrapConclude,
		workflowOutput(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"partial answer"}}}`))
	executor := newBootstrapWorkflowExecutor(t, runtime, started.StartedRun, prompt, fallbackPrompt, bootstrapInput)

	receipt := finishWorkflowCompletion(t, ctx, runService, executor.Execute(ctx, started.StartedRun))
	detail, err := projectService.Get(ctx, current.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	concluded := findIntent(detail.Intents, target.ID)
	if receipt.Status != rundomain.StatusCommitted || receipt.ProjectRevision != 2 ||
		detail.Project.Status != ProjectStatusActive || concluded == nil || concluded.To == nil ||
		*concluded.To != receipt.CreatedFactIDs[0] || len(detail.CompletionRecords) != 0 {
		t.Fatalf("Bootstrap fallback: receipt=%+v detail=%+v", receipt, detail)
	}
	assertWorkflowRuntime(t, runtime, started.Run.ID, 2, false)
	snapshot := runtime.Snapshot()
	if snapshot.Launches[0].Environment["MOCK_PHASE"] != string(worker.PhaseBootstrap) ||
		snapshot.Launches[1].Environment["MOCK_PHASE"] != string(worker.PhaseBootstrapConclude) {
		t.Fatalf("Bootstrap phases: %+v", snapshot.Launches)
	}
	disposeWorkflowRuntime(t, ctx, runtime, started.Run.ID)
}

func TestBootstrapRunWorkflowFailureLeavesNoPartialStateOrClaim(t *testing.T) {
	projectService, runService, current, target := bootstrapWorkflowProject(t)
	defer runService.DB.Close()
	ctx := t.Context()
	bootstrapInput, err := input.BootstrapInputForRun(projectdomain.ExecutionInputProjectionFromDetail(current), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Complete the project when the initial facts are sufficient."
	fallbackPrompt := "Conclude the reserved Bootstrap intent with the useful partial result."
	started := startWorkflowRun(t, ctx, runService, current.Project, target.ID, rundomain.TaskBootstrap, prompt, fallbackPrompt)
	runtime := newWorkflowRuntime(t)
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseBootstrap,
		workflowOutput(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`))
	configureWorkflowExecution(runtime, started.Run.ID, rundomain.PhaseBootstrapConclude,
		workflowOutput(`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"fallback cannot complete","fact":{"description":"answer"}}}`))
	executor := newBootstrapWorkflowExecutor(t, runtime, started.StartedRun, prompt, fallbackPrompt, bootstrapInput)

	completion := executor.Execute(ctx, started.StartedRun)
	if completion.Kind != rundomain.CompletionFailed || completion.Code != rundomain.ExecutionCaptureFailed ||
		completion.CaptureFailureReason != rundomain.CaptureFailureInvalidCandidate {
		t.Fatalf("Bootstrap failure: %+v", completion)
	}
	receipt := finishWorkflowCompletion(t, ctx, runService, completion)
	if receipt.Status != rundomain.StatusFailed || receipt.Code != rundomain.ExecutionCaptureFailed || receipt.ProjectRevision != 1 {
		t.Fatalf("failure receipt: %+v", receipt)
	}
	detail, err := projectService.Get(ctx, current.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	unchanged := findIntent(detail.Intents, target.ID)
	if detail.Project.Status != ProjectStatusActive || detail.Project.Revision != 1 || len(detail.Facts) != 2 ||
		len(detail.CompletionRecords) != 0 || unchanged == nil || !unchanged.IsOpen() {
		t.Fatalf("failed Bootstrap leaked business state: %+v", detail)
	}
	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var claims int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM execution_claims WHERE run_id = ?", started.Run.ID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 0 {
		t.Fatalf("failed Bootstrap retained %d claims", claims)
	}
	assertWorkflowRuntime(t, runtime, started.Run.ID, 3, false)
	disposeWorkflowRuntime(t, ctx, runtime, started.Run.ID)
}

func bootstrapWorkflowProject(t *testing.T) (*ProjectService, *RunService, ProjectDetail, Intent) {
	t.Helper()
	projectService, runService, _ := newRunTestServices(t)
	project, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "bootstrap", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := projectService.Get(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Intents) != 1 || !schedulingIsBootstrapIntent(current.Intents[0]) {
		t.Fatalf("materialized Bootstrap intent: %+v", current.Intents)
	}
	return projectService, runService, current, current.Intents[0]
}

func startConcludeRecoveryWorkflowRun(
	t *testing.T,
	ctx context.Context,
	runService *RunService,
	project ProjectMeta,
	targetIntentID string,
	task rundomain.TaskType,
	prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt string,
) *rundomain.AdmittedRun {
	t.Helper()
	catalog := newConcludeRecoveryWorkflowCatalog(
		t, task, prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt,
	)
	promptHash, err := catalog.PromptTemplateHash(task)
	if err != nil {
		t.Fatal(err)
	}
	request := rundomain.StartRequest{
		ProjectID: project.ID, Task: task, DispatcherID: "dispatcher-a",
		DispatcherFencingToken: 1, CandidateGeneration: project.Generation,
		CandidateRevision: project.Revision, TargetIntentID: &targetIntentID,
		WorkKey: rundomain.WorkKey(fmt.Sprintf("intent:%s:%s:%s", project.ID, targetIntentID, task)),
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: "config-v1", PromptTemplateHash: promptHash,
			Worker: rundomain.WorkerSnapshot{
				Name: "agent", Adapter: "mock",
				Capabilities: []string{string(task), rundomain.ProcessRecoveryCapability},
			},
			Image: rundomain.ImageSnapshot{
				RequestedRef: "aether-agent:latest", ImageID: "sha256:test-image", RepoDigest: "aether-agent@sha256:test",
			},
			Timeouts: rundomain.TimeoutSnapshot{
				RunSeconds: 24, PhaseSeconds: 5, FallbackPhaseSeconds: 5, RecoveryPhaseSeconds: 3, GraceSeconds: 1,
			},
			ResultSchema: rundomain.ResultSchemaV1,
		},
	}
	started, err := runService.StartRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return started
}

func newConcludeRecoveryWorkflowCatalog(
	t *testing.T,
	task rundomain.TaskType,
	prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt string,
) *worker.Catalog {
	t.Helper()
	prompts := map[rundomain.TaskType]worker.PromptSet{
		rundomain.TaskReason:    {Primary: "reason", Finalization: "finalize reason"},
		rundomain.TaskExplore:   {Primary: "explore", Fallback: "conclude explore", Finalization: "finalize explore"},
		rundomain.TaskBootstrap: {Primary: "bootstrap", Fallback: "conclude bootstrap", Finalization: "finalize bootstrap"},
	}
	prompts[task] = worker.PromptSet{
		Primary: prompt, Fallback: fallbackPrompt,
		RecoveryPrimary: recoveryPrompt, RecoveryConclude: recoveryConcludePrompt,
		Finalization: "finalize " + prompt,
	}
	catalog, err := worker.NewCatalog(worker.CatalogSource{
		ImageReference: "aether-agent:latest",
		Workers: []worker.CatalogWorker{{
			Name: "agent", Adapter: "mock", TaskTypes: []rundomain.TaskType{task},
			MaxRunning:   1,
			Capabilities: []string{string(task), rundomain.ProcessRecoveryCapability},
		}},
		Prompts: prompts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func startWorkflowRun(
	t *testing.T,
	ctx context.Context,
	runService *RunService,
	project ProjectMeta,
	targetIntentID string,
	task rundomain.TaskType,
	prompt string,
	fallbackPrompt string,
) *rundomain.AdmittedRun {
	t.Helper()
	catalog := newWorkflowExecutionCatalog(t, "agent", task, nil, prompt, fallbackPrompt)
	promptHash, err := catalog.PromptTemplateHash(task)
	if err != nil {
		t.Fatal(err)
	}
	timeouts := rundomain.TimeoutSnapshot{RunSeconds: 10, PhaseSeconds: 5, GraceSeconds: 1}
	if task == rundomain.TaskBootstrap || task == rundomain.TaskExplore {
		timeouts.RunSeconds = 18
		timeouts.FallbackPhaseSeconds = 5
	}
	request := rundomain.StartRequest{
		ProjectID: project.ID, Task: task, DispatcherID: "dispatcher-a",
		DispatcherFencingToken: 1, CandidateGeneration: project.Generation,
		CandidateRevision: project.Revision, TargetIntentID: &targetIntentID,
		WorkKey: rundomain.WorkKey(fmt.Sprintf("intent:%s:%s:%s", project.ID, targetIntentID, task)),
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: "config-v1", PromptTemplateHash: promptHash,
			Worker: rundomain.WorkerSnapshot{Name: "agent", Adapter: "mock", Capabilities: []string{string(task)}},
			Image: rundomain.ImageSnapshot{
				RequestedRef: "aether-agent:latest", ImageID: "sha256:test-image", RepoDigest: "aether-agent@sha256:test",
			},
			Timeouts:     timeouts,
			ResultSchema: rundomain.ResultSchemaV1,
		},
	}
	started, err := runService.StartRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return started
}

func newBootstrapWorkflowExecutor(
	t *testing.T,
	runtime *testutil.MemoryRuntime,
	started rundomain.StartedRun,
	prompt string,
	fallbackPrompt string,
	input rundomain.BootstrapInput,
) rundomain.Executor {
	t.Helper()
	catalog := newWorkflowExecutionCatalog(t, "agent", rundomain.TaskBootstrap, nil, prompt, fallbackPrompt)
	return bindWorkflowExecution(t, runtime, started, catalog, worker.ExecutionBindingRequest{
		Task: rundomain.TaskBootstrap, WorkerName: "agent", BootstrapInput: &input,
	})
}

func newWorkflowRuntime(t *testing.T) *testutil.MemoryRuntime {
	t.Helper()
	return testutil.NewMemoryRuntime()
}

func configureWorkflowExecution(
	runtime *testutil.MemoryRuntime,
	runID rundomain.RunID,
	phase rundomain.ExecutionPhase,
	output rundomain.ProcessOutput,
) {
	runtime.ConfigureExecution(
		testutil.ExecutionKey{RunID: runID, Phase: phase},
		testutil.ExecutionBehavior{Output: output},
	)
}

func finishWorkflowCompletion(
	t *testing.T,
	ctx context.Context,
	runService *RunService,
	completion rundomain.Completion,
) *rundomain.FinishReceipt {
	t.Helper()
	request, err := completion.FinishRequest()
	if err != nil {
		t.Fatalf("completion=%+v: %v", completion, err)
	}
	receipt, err := runService.FinishRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func workflowOutput(candidate string) rundomain.ProcessOutput {
	var envelope struct {
		Outcome json.RawMessage `json:"outcome"`
	}
	if err := json.Unmarshal([]byte(candidate), &envelope); err == nil && len(envelope.Outcome) > 0 {
		candidate = fmt.Sprintf(`{"outcome":%s}`, envelope.Outcome)
	}
	return rundomain.ProcessOutput{Exited: true, Stdout: candidate + "\n"}
}

func workflowOutputWithExit(candidate string, exitCode int) rundomain.ProcessOutput {
	output := workflowOutput(candidate)
	output.ExitCode = exitCode
	return output
}

func countFactsWithDescription(facts []Fact, description string) int {
	count := 0
	for _, fact := range facts {
		if fact.Description == description {
			count++
		}
	}
	return count
}

func workflowLaunchesByPhase(launches []rundomain.LaunchSpec) map[rundomain.ExecutionPhase]rundomain.LaunchSpec {
	byPhase := make(map[rundomain.ExecutionPhase]rundomain.LaunchSpec, len(launches))
	for _, launch := range launches {
		byPhase[launch.Phase] = launch
	}
	return byPhase
}

func assertWorkflowRuntime(t *testing.T, runtime *testutil.MemoryRuntime, runID rundomain.RunID, launches int, blackboardFile bool) {
	t.Helper()
	snapshot := runtime.Snapshot()
	if len(snapshot.ActiveRunIDs) != 1 || snapshot.ActiveRunIDs[runID] == "" || len(snapshot.CompletedRunIDs) != 0 ||
		len(snapshot.Launches) != launches {
		t.Fatalf("runtime lifecycle: %+v", snapshot)
	}
	for _, launch := range snapshot.Launches {
		if (launch.RenderedInput.Path != "") != blackboardFile {
			t.Fatalf("runtime input mechanism: %+v", launch)
		}
	}
}

func disposeWorkflowRuntime(t *testing.T, ctx context.Context, runtime *testutil.MemoryRuntime, runID rundomain.RunID) {
	t.Helper()
	if err := runtime.CompleteRun(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if snapshot := runtime.Snapshot(); len(snapshot.ActiveRunIDs) != 0 || len(snapshot.CompletedRunIDs) != 1 {
		t.Fatalf("runtime cleanup: %+v", snapshot)
	}
}
