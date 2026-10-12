package run_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/controlplane/input"
	projectdomain "github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

func TestMockDriverOutcomeSubmissionRunsThroughWorkflow(t *testing.T) {
	t.Run("reason", func(t *testing.T) {
		projectService, runService, cleanup := newRunTestServices(t)
		defer cleanup()
		ctx := t.Context()
		project, err := projectService.Create(ctx, CreateProjectRequest{Title: "reason", Origin: "origin", Goal: "goal"})
		if err != nil {
			t.Fatal(err)
		}
		current, err := projectService.Get(ctx, project.Project.ID)
		if err != nil {
			t.Fatal(err)
		}
		blackboard, err := input.BlackboardInputForRun(projectdomain.ExecutionInputProjectionFromDetail(current), nil)
		if err != nil {
			t.Fatal(err)
		}
		catalog := newWorkflowExecutionCatalog(t, "reasoner", rundomain.TaskReason,
			map[string]string{"MOCK_REASON": `{"delay":[0,0],"outcomes":{"noop":1}}`}, "reason", "")
		started := startMockReasonRun(t, ctx, runService, current, catalog)
		runtime := &mockCommandRuntime{}
		executor := bindWorkflowExecution(t, runtime, started.StartedRun, catalog, worker.ExecutionBindingRequest{
			Task: rundomain.TaskReason, WorkerName: "reasoner", BlackboardInput: &blackboard, MaxIntents: 3,
		})
		receipt := finishWorkflowCompletion(t, ctx, runService, executor.Execute(ctx, started.StartedRun))
		if receipt.Status != rundomain.StatusCommitted || receipt.OutcomeKind != rundomain.OutcomeNoChange {
			t.Fatalf("Reason receipt: %+v", receipt)
		}
		runtime.assertOutcomeSubmission(t)
	})

	t.Run("explore", func(t *testing.T) {
		projectService, runService, cleanup := newRunTestServices(t)
		defer cleanup()
		ctx := t.Context()
		project, err := projectService.Create(ctx, CreateProjectRequest{Title: "explore", Origin: "origin", Goal: "goal"})
		if err != nil {
			t.Fatal(err)
		}
		intent := proposeIntent(t, projectService, runService, project.Project.ID, "investigate the origin", []string{"origin"})
		current, err := projectService.Get(ctx, project.Project.ID)
		if err != nil {
			t.Fatal(err)
		}
		blackboard, err := input.BlackboardInputForRun(projectdomain.ExecutionInputProjectionFromDetail(current), &intent.ID)
		if err != nil {
			t.Fatal(err)
		}
		catalog := newWorkflowExecutionCatalog(t, "agent", rundomain.TaskExplore,
			map[string]string{"MOCK_EXPLORE_EXECUTE": `{"delay":[0,0],"outcomes":{"fact":1}}`}, "explore", "conclude")
		started := startWorkflowRun(t, ctx, runService, current.Project, intent.ID, rundomain.TaskExplore, "explore", "conclude")
		runtime := &mockCommandRuntime{}
		executor := bindWorkflowExecution(t, runtime, started.StartedRun, catalog, worker.ExecutionBindingRequest{
			Task: rundomain.TaskExplore, WorkerName: "agent", BlackboardInput: &blackboard,
		})
		receipt := finishWorkflowCompletion(t, ctx, runService, executor.Execute(ctx, started.StartedRun))
		if receipt.Status != rundomain.StatusCommitted || receipt.OutcomeKind != rundomain.OutcomeConcludeIntent || len(receipt.CreatedFactIDs) != 1 {
			t.Fatalf("Explore receipt: %+v", receipt)
		}
		runtime.assertOutcomeSubmission(t)
	})

	t.Run("bootstrap", func(t *testing.T) {
		_, runService, current, target := bootstrapWorkflowProject(t)
		defer runService.DB.Close()
		ctx := t.Context()
		bootstrapInput, err := input.BootstrapInputForRun(projectdomain.ExecutionInputProjectionFromDetail(current), target.ID)
		if err != nil {
			t.Fatal(err)
		}
		catalog := newWorkflowExecutionCatalog(t, "agent", rundomain.TaskBootstrap,
			map[string]string{"MOCK_BOOTSTRAP": `{"delay":[0,0],"outcomes":{"complete":1}}`}, "bootstrap", "conclude")
		started := startWorkflowRun(t, ctx, runService, current.Project, target.ID, rundomain.TaskBootstrap, "bootstrap", "conclude")
		runtime := &mockCommandRuntime{}
		executor := bindWorkflowExecution(t, runtime, started.StartedRun, catalog, worker.ExecutionBindingRequest{
			Task: rundomain.TaskBootstrap, WorkerName: "agent", BootstrapInput: &bootstrapInput,
		})
		receipt := finishWorkflowCompletion(t, ctx, runService, executor.Execute(ctx, started.StartedRun))
		if receipt.Status != rundomain.StatusCommitted || receipt.OutcomeKind != rundomain.OutcomeCompleteProject {
			t.Fatalf("Bootstrap receipt: %+v", receipt)
		}
		runtime.assertOutcomeSubmission(t)
	})

	t.Run("rejects-invalid-outcome", func(t *testing.T) {
		projectService, runService, cleanup := newRunTestServices(t)
		defer cleanup()
		ctx := t.Context()
		project, err := projectService.Create(ctx, CreateProjectRequest{Title: "invalid", Origin: "origin", Goal: "goal"})
		if err != nil {
			t.Fatal(err)
		}
		current, err := projectService.Get(ctx, project.Project.ID)
		if err != nil {
			t.Fatal(err)
		}
		blackboard, err := input.BlackboardInputForRun(projectdomain.ExecutionInputProjectionFromDetail(current), nil)
		if err != nil {
			t.Fatal(err)
		}
		catalog := newWorkflowExecutionCatalog(t, "reasoner", rundomain.TaskReason,
			map[string]string{"MOCK_REASON": `{"delay":[0,0],"outcomes":{"invalid_payload":1}}`}, "reason", "")
		started := startMockReasonRun(t, ctx, runService, current, catalog)
		runtime := &mockCommandRuntime{}
		executor := bindWorkflowExecution(t, runtime, started.StartedRun, catalog, worker.ExecutionBindingRequest{
			Task: rundomain.TaskReason, WorkerName: "reasoner", BlackboardInput: &blackboard, MaxIntents: 3,
		})
		completion := executor.Execute(ctx, started.StartedRun)
		if completion.Kind != rundomain.CompletionFailed || completion.Code != rundomain.ExecutionCaptureFailed {
			t.Fatalf("invalid Mock completion: %+v", completion)
		}
		receipt := finishWorkflowCompletion(t, ctx, runService, completion)
		if receipt.Status != rundomain.StatusFailed || receipt.Code != rundomain.ExecutionCaptureFailed {
			t.Fatalf("invalid Mock receipt: %+v", receipt)
		}
		runtime.assertOutcomeSubmission(t)
	})
}

func startMockReasonRun(t *testing.T, ctx context.Context, runService *RunService, project ProjectDetail, catalog *worker.Catalog) *rundomain.AdmittedRun {
	t.Helper()
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
	started, err := runService.StartRun(ctx, rundomain.StartRequest{
		ProjectID: project.Project.ID, Task: rundomain.TaskReason, DispatcherID: "dispatcher-a",
		DispatcherFencingToken: 1, CandidateGeneration: project.Project.Generation,
		CandidateRevision: project.Project.Revision, WorkKey: workKey,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: "config-v1", PromptTemplateHash: promptHash,
			Worker:       rundomain.WorkerSnapshot{Name: "reasoner", Adapter: "mock", Capabilities: []string{"reason"}},
			Image:        rundomain.ImageSnapshot{RequestedRef: "aether-agent:latest", ImageID: "sha256:test-image", RepoDigest: "aether-agent@sha256:test"},
			Timeouts:     rundomain.TimeoutSnapshot{RunSeconds: 12, PhaseSeconds: 5, GraceSeconds: 1},
			ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return started
}

type mockCommandRuntime struct {
	launches []rundomain.LaunchSpec
}

func (runtime *mockCommandRuntime) Launch(_ context.Context, spec rundomain.LaunchSpec) (rundomain.Process, error) {
	runtime.launches = append(runtime.launches, spec)
	return &mockCommandProcess{spec: spec}, nil
}

func (runtime *mockCommandRuntime) assertOutcomeSubmission(t *testing.T) {
	t.Helper()
	if len(runtime.launches) == 0 {
		t.Fatalf("Mock workflow did not launch an invocation")
	}
	for _, launch := range runtime.launches {
		if launch.Environment["AETHER_RESULT_PROTOCOL"] != worker.OutcomeSubmissionSchemaV1 {
			t.Fatalf("Mock launch did not use the outcome submission protocol: %+v", runtime.launches)
		}
	}
}

type mockCommandProcess struct {
	spec rundomain.LaunchSpec
}

func (process *mockCommandProcess) Wait(ctx context.Context) (rundomain.ProcessOutput, error) {
	command := exec.CommandContext(ctx, process.spec.Command[0], process.spec.Command[1:]...)
	command.Env = append(os.Environ(), environmentEntries(process.spec.Environment)...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	output := rundomain.ProcessOutput{Stdout: stdout.String(), Stderr: stderr.String(), Exited: true, ExitCode: 0}
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			output.ExitCode = exitError.ExitCode()
		} else if ctx.Err() != nil {
			output.Exited = false
			output.Cancelled = true
			return output, nil
		} else {
			return output, err
		}
	}
	return output, nil
}

func (process *mockCommandProcess) Stop(context.Context, time.Duration) error { return nil }

func environmentEntries(environment map[string]string) []string {
	entries := make([]string, 0, len(environment))
	for key, value := range environment {
		entries = append(entries, key+"="+value)
	}
	return entries
}
