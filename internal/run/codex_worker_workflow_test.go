package run_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/testutil"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

type adapterWorkflow struct {
	projects *ProjectService
	runs     *RunService
	started  *rundomain.AdmittedRun
	executor rundomain.Executor
	runtime  *testutil.MemoryRuntime
}

func newCodexWorkflow(t *testing.T, task rundomain.TaskType) adapterWorkflow {
	t.Helper()
	runtime := testutil.NewMemoryRuntime()
	flow := newAdapterWorkflow(t, task, worker.WorkerTypeCodex, runtime)
	flow.runtime = runtime
	return flow
}

func newAdapterWorkflow(t *testing.T, task rundomain.TaskType, kind worker.WorkerType, runtime rundomain.Runtime) adapterWorkflow {
	t.Helper()
	projects, runs, cleanup := newRunTestServices(t)
	t.Cleanup(cleanup)
	created, err := projects.Create(t.Context(), CreateProjectRequest{
		Title: "Adapter outcome submission", Origin: "origin", Goal: "goal", BootstrapEnabled: task == rundomain.TaskBootstrap,
	})
	if err != nil {
		t.Fatal(err)
	}
	var target *string
	if task == rundomain.TaskExplore {
		intent := proposeIntent(t, projects, runs, created.Project.ID, "verify origin", []string{"origin"})
		target = &intent.ID
	}
	detail, err := projects.Get(t.Context(), created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task == rundomain.TaskBootstrap {
		target = &detail.Intents[0].ID
	}
	source, err := worker.CatalogSourceFromConfig(worker.CatalogConfiguration{
		ImageReference: "ghcr.io/c1ph3rx13/aether-worker:latest", PromptGroup: "default",
		Workers: []worker.WorkerConfig{{
			Name: string(kind), Type: kind, MaxRunning: 1,
			TaskTypes:    []worker.TaskType{worker.TaskTypeReason, worker.TaskTypeExplore, worker.TaskTypeBootstrap},
			Capabilities: []string{rundomain.ProcessRecoveryCapability},
			Env:          adapterWorkflowEnvironment(kind),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := worker.NewCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := catalog.PromptTemplateHash(task)
	if err != nil {
		t.Fatal(err)
	}
	workerSnapshot, err := catalog.WorkerSnapshotForTask(string(kind), task)
	if err != nil {
		t.Fatal(err)
	}
	inputSnapshot := rundomain.InputSnapshot{Generation: detail.Project.Generation, Revision: detail.Project.Revision, TargetIntentID: target}
	workKey, err := rundomain.WorkKeyFor(detail.Project.ID, task, inputSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	timeouts := rundomain.TimeoutSnapshot{RunSeconds: 30, PhaseSeconds: 5, GraceSeconds: 1}
	maxIntents := 2
	if task != rundomain.TaskReason {
		timeouts.FallbackPhaseSeconds, timeouts.RecoveryPhaseSeconds, maxIntents = 5, 3, 0
	}
	started, err := runs.StartRun(t.Context(), rundomain.StartRequest{
		ProjectID: detail.Project.ID, Task: task, TargetIntentID: target, WorkKey: workKey,
		DispatcherID: "dispatcher-a", DispatcherFencingToken: 1,
		CandidateGeneration: detail.Project.Generation, CandidateRevision: detail.Project.Revision,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: "config-v1", Worker: workerSnapshot, PromptTemplateHash: hash,
			Image:    rundomain.ImageSnapshot{RequestedRef: catalog.ImageReference()},
			Timeouts: timeouts, ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: maxIntents,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := rundomain.NewExecutionBinding(runtime, adapterFixtureSecrets{}, nil, runs, runs)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := catalog.BindExecution(binding, worker.ExecutionBindingRequest{
		Task: task, WorkerName: string(kind), BlackboardInput: started.BlackboardInput,
		BootstrapInput: started.BootstrapInput, MaxIntents: maxIntents,
	}, started.StartedRun)
	if err != nil {
		t.Fatal(err)
	}
	return adapterWorkflow{projects: projects, runs: runs, started: started, executor: executor}
}

type adapterFixtureSecrets struct{}

func (adapterFixtureSecrets) ResolveSecrets(context.Context, []rundomain.SecretReference) (*rundomain.ResolvedSecrets, error) {
	return rundomain.NewResolvedSecrets(map[string][]byte{"OPENAI_API_KEY": []byte("fixture-secret"), "PI_API_KEY": []byte("fixture-secret"), "ANTHROPIC_AUTH_TOKEN": []byte("fixture-secret")}), nil
}

func (flow adapterWorkflow) output(phase rundomain.ExecutionPhase, stdout string, withSession bool) {
	output := rundomain.ProcessOutput{Exited: true, Stdout: stdout}
	if withSession {
		output.Stderr = "session id: 12345678-abcd-4abc-8def-123456789abc"
	}
	flow.runtime.ConfigureExecution(testutil.ExecutionKey{RunID: flow.started.Run.ID, Phase: phase}, testutil.ExecutionBehavior{Output: output})
}

func TestAdapterWorkflowsCommitIdenticalTaskOutcomes(t *testing.T) {
	for _, test := range []struct {
		name           string
		task           rundomain.TaskType
		submission     string
		canonical      string
		facts, intents int
		completed      bool
	}{
		{"reason no change", rundomain.TaskReason, `{"outcome":{"kind":"no_change"}}`, `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`, 0, 0, false},
		{"reason complete", rundomain.TaskReason, `{"outcome":{"from":["origin"],"description":"  verified  ","kind":"complete_project"}}`, `{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"verified","from":["origin"]}}`, 0, 1, true},
		{"reason propose", rundomain.TaskReason, `{"outcome":{"kind":"propose_intents","intents":[{"from":["origin"],"description":"verify origin"}]}}`, `{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"from":["origin"],"description":"verify origin"}]}}`, 0, 1, false},
		{"reason decline", rundomain.TaskReason, `{"outcome":{"kind":"decline","reason":"no evidence"}}`, `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"no evidence"}}`, 0, 0, false},
		{"explore conclude", rundomain.TaskExplore, `{"outcome":{"kind":"conclude_intent","fact":{"description":"确认 \"quote\"\nC:\\report; /tmp/evidence.md"}}}`, `{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"确认 \"quote\"\nC:\\report; /tmp/evidence.md"}}}`, 1, 0, false},
		{"explore decline", rundomain.TaskExplore, `{"outcome":{"kind":"decline","reason":"no evidence"}}`, `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"no evidence"}}`, 0, 0, false},
		{"bootstrap complete", rundomain.TaskBootstrap, `{"outcome":{"kind":"complete_project","description":"verified","fact":{"description":"new evidence"}}}`, `{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"verified","fact":{"description":"new evidence"}}}`, 1, 1, true},
		{"bootstrap partial", rundomain.TaskBootstrap, `{"outcome":{"kind":"conclude_intent","fact":{"description":"partial evidence"}}}`, `{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"partial evidence"}}}`, 1, 0, false},
		{"bootstrap decline", rundomain.TaskBootstrap, `{"outcome":{"kind":"decline","reason":"no evidence"}}`, `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"no evidence"}}`, 0, 0, false},
	} {
		for _, kind := range []worker.WorkerType{worker.WorkerTypeClaudeCode, worker.WorkerTypeCodex, worker.WorkerTypePi} {
			t.Run(string(kind)+"/"+test.name, func(t *testing.T) {
				runtime := newAdapterResultRuntime(t, kind, adapterResultStep{result: test.submission}, adapterResultStep{result: test.submission})
				flow := newAdapterWorkflow(t, test.task, kind, runtime)
				before, err := flow.projects.Get(t.Context(), flow.started.Run.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				completion := flow.executor.Execute(t.Context(), flow.started.StartedRun)
				if string(completion.Candidate) != test.canonical {
					t.Fatalf("candidate=%s, want %s", completion.Candidate, test.canonical)
				}
				receipt := finishWorkflowCompletion(t, t.Context(), flow.runs, completion)
				wantStatus := rundomain.StatusCommitted
				if strings.Contains(test.name, "decline") {
					wantStatus = rundomain.StatusDeclined
				}
				wantHash := sha256.Sum256([]byte(test.canonical))
				if receipt.Status != wantStatus || receipt.ResultHash != fmt.Sprintf("%x", wantHash) || len(receipt.CreatedFactIDs) != test.facts || len(receipt.CreatedIntentIDs) != test.intents {
					t.Fatalf("receipt: %+v", receipt)
				}
				after, err := flow.projects.Get(t.Context(), flow.started.Run.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				if len(after.Facts) != len(before.Facts)+test.facts || (after.Project.Status == ProjectStatusCompleted) != test.completed {
					t.Fatalf("Project result: %+v", after)
				}
				assertAdapterLaunchMaterials(t, kind, runtime.launches)
				if (receipt.ResultEvidenceSource == rundomain.ResultEvidenceSourceFile) != (kind == worker.WorkerTypePi) {
					t.Fatalf("evidence source: %+v", receipt)
				}
			})
		}
	}
}

func TestCodexWorkflowRejectsInvalidSubmissions(t *testing.T) {
	for _, submission := range []string{
		"", `{"Outcome":{"kind":"no_change"}}`, `{"outcome":{"Kind":"no_change"}}`,
		`{"outcome":{"kind":"decline","Reason":"alias"}}`, `{"outcome":null}`, `{"outcome":[]}`,
		`{"outcome":{"kind":"no_change","extra":true}}`, `{"outcome":{"kind":"no_change","fact":null}}`,
		`{"outcome":{"kind":"no_change","kind":"no_change"}}`, `{"outcome":{"kind":"no_change"},"outcome":{"kind":"no_change"}}`,
		`{"outcome":{"kind":"decline","reason":3}}`, `{"outcome":{"kind":"no_change",},}`,
		`{"outcome":{"kind":"no_change"`, `{"outcome":{"kind":"no_change"}} {}`,
		`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`,
		"```json\n{\"outcome\":{\"kind\":\"no_change\"}}\n```",
		`explanation {"outcome":{"kind":"no_change"}}`, `{"outcome":{"kind":"no_change"}} trailing explanation`,
		`{"outcome":{"kind":"complete_project","description":"goal","from":["goal"]}}`,
		`{"outcome":{"kind":"complete_project","description":"goal","from":["origin"],"fact":null}}`,
		`{"outcome":{"kind":"propose_intents","intents":[{"description":"one","from":["origin"]},{"description":"two","from":["origin"]},{"description":"three","from":["origin"]}]}}`,
		`{"outcome":{"kind":"conclude_intent","fact":{"description":"invalid for reason"}}}`,
		string([]byte{0xff}), `{"outcome":{"kind":"decline","reason":"` + strings.Repeat("x", 2049) + `"}}`,
		`{"outcome":{"kind":"decline","reason":"` + strings.Repeat("x", rundomain.MaximumResultBytes) + `"}}`,
	} {
		t.Run(fmt.Sprintf("submission-%x", sha256.Sum256([]byte(submission)))[:28], func(t *testing.T) {
			flow := newCodexWorkflow(t, rundomain.TaskReason)
			flow.output(rundomain.PhaseReason, submission, false)
			completion := flow.executor.Execute(t.Context(), flow.started.StartedRun)
			if completion.Kind != rundomain.CompletionFailed || len(completion.Candidate) != 0 {
				t.Fatalf("invalid submission accepted: %+v", completion)
			}
			receipt := finishWorkflowCompletion(t, t.Context(), flow.runs, completion)
			if receipt.Status != rundomain.StatusFailed {
				t.Fatalf("receipt: %+v", receipt)
			}
			detail, err := flow.projects.Get(t.Context(), flow.started.Run.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.Project.Revision != 1 || len(detail.Intents) != 0 {
				t.Fatalf("invalid result changed Blackboard: %+v", detail)
			}
		})
	}
}

func TestAdapterWorkflowsRejectSourcesOutsideProject(t *testing.T) {
	for _, kind := range []worker.WorkerType{worker.WorkerTypeClaudeCode, worker.WorkerTypeCodex, worker.WorkerTypePi} {
		t.Run(string(kind), func(t *testing.T) {
			runtime := newAdapterResultRuntime(t, kind, adapterResultStep{result: `{"outcome":{"kind":"complete_project","description":"invalid source","from":["outside"]}}`})
			flow := newAdapterWorkflow(t, rundomain.TaskReason, kind, runtime)
			request, err := flow.executor.Execute(t.Context(), flow.started.StartedRun).FinishRequest()
			if err != nil {
				t.Fatal(err)
			}
			_, err = flow.runs.FinishRun(t.Context(), request)
			var domainErr *rundomain.DomainError
			if !errors.As(err, &domainErr) || domainErr.Code != rundomain.ErrorInvalidResult {
				t.Fatalf("unavailable source was not rejected: %v", err)
			}
			detail, err := flow.projects.Get(t.Context(), flow.started.Run.ProjectID)
			if err != nil || detail.Project.Revision != 1 || len(detail.Intents) != 0 {
				t.Fatalf("invalid source changed Blackboard: detail=%+v err=%v", detail, err)
			}
		})
	}
}

func TestCodexWorkflowReasonFinalization(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprintf("valid-%t", valid), func(t *testing.T) {
			flow := newCodexWorkflow(t, rundomain.TaskReason)
			flow.output(rundomain.PhaseReason, "not JSON", true)
			result := "still not JSON"
			if valid {
				result = `{"outcome":{"kind":"no_change"}}`
			}
			flow.output(rundomain.PhaseReasonFinalization, result, true)
			receipt := finishWorkflowCompletion(t, t.Context(), flow.runs, flow.executor.Execute(t.Context(), flow.started.StartedRun))
			if (receipt.Status == rundomain.StatusCommitted) != valid {
				t.Fatalf("receipt: %+v", receipt)
			}
			launches := flow.runtime.Snapshot().Launches
			if len(launches) != 2 {
				t.Fatalf("finalization must run once: %+v", launches)
			}
			assertCodexLaunchMaterials(t, launches)
			assertCodexResume(t, launches[1])
		})
	}
}

func TestCodexWorkflowContinuationSchemas(t *testing.T) {
	const partial = `{"outcome":{"kind":"conclude_intent","fact":{"description":"verified evidence"}}}`
	const complete = `{"outcome":{"kind":"complete_project","description":"complete","fact":{"description":"verified evidence"}}}`
	for _, task := range []rundomain.TaskType{rundomain.TaskExplore, rundomain.TaskBootstrap} {
		for _, mode := range []string{"conclude", "primary recovery", "conclude recovery", "invalid finalization", "forbidden completion"} {
			t.Run(string(task)+"/"+mode, func(t *testing.T) {
				flow := newCodexWorkflow(t, task)
				primary, conclude, recovery, concludeRecovery := rundomain.PhaseExplore, rundomain.PhaseExploreConclude, rundomain.PhaseExploreRecovery, rundomain.PhaseExploreConcludeRecovery
				if task == rundomain.TaskBootstrap {
					primary, conclude, recovery, concludeRecovery = rundomain.PhaseBootstrap, rundomain.PhaseBootstrapConclude, rundomain.PhaseBootstrapRecovery, rundomain.PhaseBootstrapConcludeRecovery
				}
				flow.output(primary, "not JSON", true)
				flow.output(conclude, partial, true)
				wantLaunches, wantStatus := 2, rundomain.StatusCommitted
				switch mode {
				case "primary recovery":
					flow.runtime.ConfigureExecution(testutil.ExecutionKey{RunID: flow.started.Run.ID, Phase: primary}, testutil.ExecutionBehavior{Output: rundomain.ProcessOutput{Exited: true, ExitCode: 9, Stdout: complete, Stderr: "session id: 12345678-abcd-4abc-8def-123456789abc"}})
					flow.output(recovery, partial, true)
				case "conclude recovery":
					flow.runtime.ConfigureExecution(testutil.ExecutionKey{RunID: flow.started.Run.ID, Phase: conclude}, testutil.ExecutionBehavior{Output: rundomain.ProcessOutput{Exited: true, ExitCode: 9, Stdout: complete, Stderr: "session id: 12345678-abcd-4abc-8def-123456789abc"}})
					flow.output(concludeRecovery, partial, true)
					wantLaunches = 3
				case "invalid finalization":
					flow.output(conclude, "not JSON", true)
					wantLaunches, wantStatus = 3, rundomain.StatusFailed
				case "forbidden completion":
					flow.output(conclude, complete, true)
					wantLaunches, wantStatus = 3, rundomain.StatusFailed
				}
				receipt := finishWorkflowCompletion(t, t.Context(), flow.runs, flow.executor.Execute(t.Context(), flow.started.StartedRun))
				if receipt.Status != wantStatus || (wantStatus == rundomain.StatusCommitted && (receipt.OutcomeKind != rundomain.OutcomeConcludeIntent || len(receipt.CreatedFactIDs) != 1)) {
					t.Fatalf("receipt: %+v", receipt)
				}
				launches := flow.runtime.Snapshot().Launches
				if len(launches) != wantLaunches {
					t.Fatalf("unexpected continuation count: %+v", launches)
				}
				assertCodexLaunchMaterials(t, launches)
				for _, launch := range launches {
					if launch.Phase != primary {
						assertCodexResume(t, launch)
					}
					allowsComplete := task == rundomain.TaskBootstrap && (launch.Phase == primary || launch.Phase == recovery)
					if bytes.Contains(launch.ProtocolFile.Bytes, []byte(`"complete_project"`)) != allowsComplete {
						t.Fatalf("phase %s received wrong completion schema: %s", launch.Phase, launch.ProtocolFile.Bytes)
					}
				}
			})
		}
	}
}

func TestCodexWorkflowProcessFailuresDoNotCommitApparentResults(t *testing.T) {
	const apparent = `{"outcome":{"kind":"no_change"}}`
	for _, test := range []struct {
		name      string
		output    rundomain.ProcessOutput
		waitError error
		code      rundomain.ExecutionErrorCode
	}{
		{"stderr only", rundomain.ProcessOutput{Exited: true, Stderr: apparent}, nil, rundomain.ExecutionCaptureFailed},
		{"nonzero exit", rundomain.ProcessOutput{Exited: true, ExitCode: 9, Stdout: apparent}, nil, rundomain.ExecutionProcessFailed},
		{"timeout", rundomain.ProcessOutput{Exited: true, TimedOut: true, Stdout: apparent}, nil, rundomain.ExecutionDeadline},
		{"overflow", rundomain.ProcessOutput{Exited: true, ErrorCode: rundomain.ProcessErrorOutputTooLarge, Stdout: apparent}, nil, rundomain.ExecutionOutputTooLarge},
		{"stream failure", rundomain.ProcessOutput{Exited: true, ErrorCode: rundomain.ProcessErrorStreamRead, Stdout: apparent}, nil, rundomain.ExecutionProcessStream},
		{"wait failure", rundomain.ProcessOutput{Exited: true, Stdout: apparent}, errors.New("wait failed"), rundomain.ExecutionProcessFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			flow := newCodexWorkflow(t, rundomain.TaskReason)
			flow.runtime.ConfigureExecution(testutil.ExecutionKey{RunID: flow.started.Run.ID, Phase: rundomain.PhaseReason}, testutil.ExecutionBehavior{Output: test.output, WaitError: test.waitError})
			receipt := finishWorkflowCompletion(t, t.Context(), flow.runs, flow.executor.Execute(t.Context(), flow.started.StartedRun))
			if receipt.Status != rundomain.StatusFailed || receipt.Code != test.code || len(flow.runtime.Snapshot().Launches) != 1 {
				t.Fatalf("failed process was accepted or continued: %+v", receipt)
			}
		})
	}
}

func TestCodexWorkflowProtocolPreparationFailure(t *testing.T) {
	flow := newCodexWorkflow(t, rundomain.TaskReason)
	flow.runtime.ConfigureExecution(testutil.ExecutionKey{RunID: flow.started.Run.ID, Phase: rundomain.PhaseReason}, testutil.ExecutionBehavior{LaunchError: errors.New("copy protocol file failed")})
	receipt := finishWorkflowCompletion(t, t.Context(), flow.runs, flow.executor.Execute(t.Context(), flow.started.StartedRun))
	if receipt.Status != rundomain.StatusFailed || receipt.Code != rundomain.ExecutionLaunchFailed {
		t.Fatalf("protocol preparation was not classified as launch failure: %+v", receipt)
	}
	snapshot := flow.runtime.Snapshot()
	if len(snapshot.Launches) != 0 || len(snapshot.FailedLaunches) != 1 || snapshot.FailedLaunches[0].ProtocolFile == nil {
		t.Fatalf("failed preparation started a process or lost protocol material: %+v", snapshot)
	}
}

func assertCodexResume(t *testing.T, launch rundomain.LaunchSpec) {
	t.Helper()
	if !strings.Contains(strings.Join(launch.Command, " "), "exec resume") || !strings.Contains(strings.Join(launch.Command, " "), "12345678-abcd-4abc-8def-123456789abc") {
		t.Fatalf("continuation lost its Session: %v", launch.Command)
	}
}

func assertCodexLaunchMaterials(t *testing.T, launches []rundomain.LaunchSpec) {
	t.Helper()
	seen := make(map[string]bool)
	for _, launch := range launches {
		file := launch.ProtocolFile
		if file == nil || file.Validate() != nil || !json.Valid(file.Bytes) || seen[file.Path()] {
			t.Fatalf("missing, invalid, or reused Invocation schema: %+v", file)
		}
		seen[file.Path()] = true
		args := strings.Join(launch.Command, "\n")
		if !strings.Contains(args, "--output-schema\n"+file.Path()+"\n--\n") ||
			!strings.Contains(args, "Schema file for this invocation: "+file.Path()) || strings.Contains(args, "--json\n") {
			t.Fatalf("Codex CLI and prompt do not share their schema target: %v", launch.Command)
		}
		if launch.RenderedInput.Hash != launches[0].RenderedInput.Hash ||
			!bytes.Equal(launch.RenderedInput.Bytes, launches[0].RenderedInput.Bytes) ||
			!launch.RenderedInput.Hash.Matches(launch.RenderedInput.Bytes) {
			t.Fatal("protocol preparation changed the input snapshot")
		}
	}
}
