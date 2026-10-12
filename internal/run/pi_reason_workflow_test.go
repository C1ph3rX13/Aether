package run_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/testutil"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

func TestPiReasonAdapterMemoryRuntimeFinishRunOutcomes(t *testing.T) {
	for _, test := range []struct {
		name     string
		toml     string
		kind     rundomain.OutcomeKind
		status   rundomain.Status
		longText bool
	}{
		{name: "no change", toml: "[outcome]\nkind = \"no_change\"\n", kind: rundomain.OutcomeNoChange, status: rundomain.StatusCommitted},
		{name: "complete", toml: "[outcome]\nkind = \"complete_project\"\ndescription = \"verified\"\nfrom = [\"origin\"]\n", kind: rundomain.OutcomeCompleteProject, status: rundomain.StatusCommitted},
		{name: "propose intents", toml: "[outcome]\nkind = \"propose_intents\"\n\n[[outcome.intents]]\nfrom = [\"origin\"]\ndescription = \"verify the origin\"\n", kind: rundomain.OutcomeProposeIntents, status: rundomain.StatusCommitted},
		{name: "decline", toml: "[outcome]\nkind = \"decline\"\nreason = \"outside scope\"\n", kind: rundomain.OutcomeDecline, status: rundomain.StatusDeclined},
		{name: "multiline", toml: "[outcome]\nkind = \"complete_project\"\ndescription = \"\"\"verified \\\nwith a quote \\\" and a backslash \\\\ in a long explanation\"\"\"\nfrom = [\"origin\"]\n", kind: rundomain.OutcomeCompleteProject, status: rundomain.StatusCommitted, longText: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			flow := newPiReasonWorkflow(t, test.toml)
			captured, err := flow.capture()
			if err != nil || captured.ResultEvidenceSource != rundomain.ResultEvidenceSourceFile {
				t.Fatalf("Pi capture=%+v err=%v", captured, err)
			}
			if test.longText && !strings.Contains(string(captured.Candidate), "long explanation") {
				t.Fatalf("multiline candidate lost text: %s", captured.Candidate)
			}
			request := rundomain.FinishRequest{RunID: flow.started.Run.ID, FencingToken: flow.started.Run.FencingToken,
				Candidate: captured.Candidate, CaptureDiagnostic: captured.CaptureDiagnostic,
				ResultEvidenceSource: captured.ResultEvidenceSource}
			receipt, err := flow.runs.FinishRun(t.Context(), request)
			if err != nil || receipt.Status != test.status || receipt.OutcomeKind != test.kind {
				t.Fatalf("FinishRun receipt=%+v err=%v", receipt, err)
			}
			if err := flow.runtime.CompleteRun(t.Context(), flow.started.Run.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPiReasonMemoryRuntimeRejectsInvalidResultFile(t *testing.T) {
	flow := newPiReasonWorkflow(t, "[outcome]\nkind = \"no_change\"\nunknown = true\n")
	captured, err := flow.capture()
	var captureErr *worker.CaptureError
	if !errors.As(err, &captureErr) || captureErr.Code != worker.CaptureInvalidCandidate || captured.Candidate != nil ||
		captured.CaptureDiagnostic != rundomain.CaptureDiagnosticResultFileInvalid {
		t.Fatalf("invalid Pi result file was accepted: capture=%+v err=%v", captured, err)
	}
	request := rundomain.FinishRequest{RunID: flow.started.Run.ID, FencingToken: flow.started.Run.FencingToken,
		CaptureDiagnostic: captured.CaptureDiagnostic,
		Termination: &rundomain.RunTermination{Status: rundomain.StatusFailed, Code: rundomain.ExecutionCaptureFailed,
			CaptureFailureReason: captured.CaptureFailureReason}}
	receipt, err := flow.runs.FinishRun(t.Context(), request)
	if err != nil || receipt.Status != rundomain.StatusFailed {
		t.Fatalf("invalid Pi FinishRun receipt=%+v err=%v", receipt, err)
	}
}

type piReasonWorkflow struct {
	runs    *RunService
	started *rundomain.AdmittedRun
	adapter worker.AgentAdapter
	runtime *testutil.MemoryRuntime
	input   *rundomain.BlackboardInput
	toml    string
}

func newPiReasonWorkflow(t *testing.T, toml string) piReasonWorkflow {
	t.Helper()
	projects, runs, cleanup := newRunTestServices(t)
	t.Cleanup(cleanup)
	created, err := projects.Create(t.Context(), CreateProjectRequest{
		Title: "Pi Reason workflow", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := projects.Get(t.Context(), created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := worker.CatalogSourceFromConfig(worker.CatalogConfiguration{
		ImageReference: "ghcr.io/c1ph3rx13/aether-worker:latest", PromptGroup: "default",
		Workers: []worker.WorkerConfig{{
			Name: "pi", Type: worker.WorkerTypePi, MaxRunning: 1,
			TaskTypes:    []worker.TaskType{worker.TaskTypeReason},
			Capabilities: []string{rundomain.ProcessRecoveryCapability},
			Env: map[string]string{
				"PI_MODEL": "fixture", "PI_BASE_URL": "https://example.invalid",
				"PI_PROVIDER_API": "openai-completions", "PI_API_KEY": "fixture-secret",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := worker.NewCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := catalog.PromptTemplateHash(rundomain.TaskReason)
	if err != nil {
		t.Fatal(err)
	}
	workerSnapshot, err := catalog.WorkerSnapshotForTask("pi", rundomain.TaskReason)
	if err != nil {
		t.Fatal(err)
	}
	inputSnapshot := rundomain.InputSnapshot{Generation: detail.Project.Generation, Revision: detail.Project.Revision}
	workKey, err := rundomain.WorkKeyFor(detail.Project.ID, rundomain.TaskReason, inputSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	started, err := runs.StartRun(t.Context(), rundomain.StartRequest{
		ProjectID: detail.Project.ID, Task: rundomain.TaskReason, WorkKey: workKey,
		DispatcherID: "dispatcher-a", DispatcherFencingToken: 1,
		CandidateGeneration: detail.Project.Generation, CandidateRevision: detail.Project.Revision,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: "config-v1", Worker: workerSnapshot, PromptTemplateHash: hash,
			Image:        rundomain.ImageSnapshot{RequestedRef: catalog.ImageReference()},
			Timeouts:     rundomain.TimeoutSnapshot{RunSeconds: 30, PhaseSeconds: 5, GraceSeconds: 1},
			ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := catalog.AgentAdapter("pi")
	if err != nil {
		t.Fatal(err)
	}
	input := started.BlackboardInput
	if input == nil {
		t.Fatal("Reason Blackboard input missing")
	}
	return piReasonWorkflow{runs: runs, started: started, adapter: adapter, runtime: testutil.NewMemoryRuntime(), input: input, toml: toml}
}

func (flow piReasonWorkflow) capture() (rundomain.CapturedResult, error) {
	prompt := "Assess the immutable Blackboard and write the TOML result file."
	invocation, err := flow.adapter.Prepare(worker.AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc", Task: rundomain.TaskReason,
		Phase: worker.PhaseReason, Prompt: prompt,
		Input:              worker.InputReference{Kind: worker.InputBlackboardFile, Path: rundomain.BlackboardPath(flow.started.Run.ID, rundomain.PhaseReason)},
		MaxProposedIntents: flow.started.Run.Spec.MaxProposedIntents,
	})
	if err != nil {
		return rundomain.CapturedResult{}, err
	}
	inputBytes, inputHash, err := flow.input.MarshalYAML()
	if err != nil {
		return rundomain.CapturedResult{}, err
	}
	flow.runtime.ConfigureExecution(testutil.ExecutionKey{RunID: flow.started.Run.ID, Phase: rundomain.PhaseReason}, testutil.ExecutionBehavior{
		Output:       rundomain.ProcessOutput{Stdout: piWorkflowStdout(), Exited: true, ExitCode: 0},
		StoppedFiles: map[string][]byte{invocation.ProtocolFile().Path(): []byte(flow.toml)},
	})
	process, err := flow.runtime.Launch(context.Background(), rundomain.LaunchSpec{
		RunID: flow.started.Run.ID, ProjectID: flow.started.Run.ProjectID, Task: rundomain.TaskReason,
		Phase: rundomain.PhaseReason, FencingToken: flow.started.Run.FencingToken,
		ConfigRevision: flow.started.Run.Spec.ConfigRevision, Image: flow.started.Run.Spec.Image.RequestedRef,
		Command: invocation.Argv(), Environment: invocation.Env(),
		RenderedInput: &rundomain.RenderedInput{Path: rundomain.BlackboardPath(flow.started.Run.ID, rundomain.PhaseReason), Bytes: inputBytes, Hash: inputHash},
		ProtocolFile:  invocation.ProtocolFile(), PhaseTimeout: 5 * time.Second, StopGrace: time.Second,
	})
	if err != nil {
		return rundomain.CapturedResult{}, err
	}
	output, err := process.Wait(context.Background())
	if err != nil {
		return rundomain.CapturedResult{}, err
	}
	captured, err := flow.adapter.Capture(context.Background(), invocation, output)
	result := rundomain.CapturedResult{Candidate: captured.Candidate, ResultHash: captured.ResultHash,
		CaptureFailureReason: captured.CaptureFailureReason, CaptureDiagnostic: captured.CaptureDiagnostic,
		ResultEvidenceSource: captured.ResultEvidenceSource}
	if err != nil {
		return result, err
	}
	return result, nil
}

func piWorkflowStdout() string {
	message := `{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":` + fmt.Sprintf("%q", "completed") + `}]} `
	message = strings.TrimSpace(message)
	return `{"schema":"aether.pi-cwd/v1","cwd":"/workspace"}` + "\n" +
		`{"type":"session","version":3,"id":"0192f7e5-7b2a-7c01-8a11-223344556677","timestamp":"2026-08-27T13:15:30.123Z","cwd":"/workspace"}` + "\n" +
		`{"type":"agent_start"}` + "\n" +
		`{"type":"message_end","message":` + message + "}\n" +
		`{"type":"turn_end","message":` + message + "}\n" +
		`{"type":"agent_end","messages":[` + message + "]}\n"
}
