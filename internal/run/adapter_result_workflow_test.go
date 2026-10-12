package run_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/testutil"
	"github.com/C1ph3rX13/Aether/internal/worker"
	"github.com/pelletier/go-toml/v2"
)

func adapterWorkflowEnvironment(kind worker.WorkerType) map[string]string {
	switch kind {
	case worker.WorkerTypeClaudeCode:
		return map[string]string{"IS_SANDBOX": "1", "ANTHROPIC_MODEL": "fixture", "ANTHROPIC_BASE_URL": "https://example.invalid", "ANTHROPIC_AUTH_TOKEN": "fixture-secret"}
	case worker.WorkerTypePi:
		return map[string]string{"PI_MODEL": "fixture", "PI_BASE_URL": "https://example.invalid", "PI_PROVIDER_API": "openai-completions", "PI_API_KEY": "fixture-secret"}
	default:
		return map[string]string{"CODEX_MODEL": "fixture", "CODEX_BASE_URL": "https://example.invalid", "OPENAI_API_KEY": "fixture-secret"}
	}
}

type adapterResultStep struct {
	result   string
	raw      bool
	timedOut bool
	exitCode int
	stale    bool
}

// Only the process boundary is simulated: admission, prompts, all three real
// adapters, continuation planning, capture, and FinishRun execute normally.
type adapterResultRuntime struct {
	*testutil.MemoryRuntime
	t        *testing.T
	kind     worker.WorkerType
	steps    []adapterResultStep
	launches []rundomain.LaunchSpec
	session  []byte
	files    map[string][]byte
}

func newAdapterResultRuntime(t *testing.T, kind worker.WorkerType, steps ...adapterResultStep) *adapterResultRuntime {
	return &adapterResultRuntime{MemoryRuntime: testutil.NewMemoryRuntime(), t: t, kind: kind, steps: steps, files: make(map[string][]byte)}
}

func (runtime *adapterResultRuntime) Launch(ctx context.Context, spec rundomain.LaunchSpec) (rundomain.Process, error) {
	index := len(runtime.launches)
	if index >= len(runtime.steps) {
		runtime.t.Fatalf("unexpected Invocation %s", spec.Phase)
	}
	step := runtime.steps[index]
	runtime.launches = append(runtime.launches, spec)
	output := rundomain.ProcessOutput{Exited: true, TimedOut: step.timedOut, ExitCode: step.exitCode}
	result := []byte(step.result)
	if runtime.kind == worker.WorkerTypePi {
		if !step.raw {
			var document map[string]any
			if err := json.Unmarshal(result, &document); err != nil {
				runtime.t.Fatal(err)
			}
			var err error
			result, err = toml.Marshal(document)
			if err != nil {
				runtime.t.Fatal(err)
			}
		}
		if !step.stale {
			runtime.files[spec.ProtocolFile.Path()] = result
		}
		const header = `{"type":"session","version":3,"id":"0192f7e5-7b2a-7c01-8a11-223344556677","timestamp":"2026-08-27T13:15:30.123Z","cwd":"/workspace"}`
		if index == 0 {
			runtime.session = []byte(header + "\n")
		}
		var parent any
		if index > 0 {
			parent = fmt.Sprintf("%08x", index*2)
		}
		for offset, message := range []map[string]any{
			{"role": "user", "content": spec.Command[len(spec.Command)-1]},
			{"role": "assistant", "stopReason": "stop", "content": `{"outcome":{"kind":"decline","reason":"alternate channel"}}`},
		} {
			id := fmt.Sprintf("%08x", index*2+offset+1)
			entry, err := json.Marshal(map[string]any{"type": "message", "id": id, "parentId": parent, "timestamp": "2026-08-27T13:15:31.123Z", "message": message})
			if err != nil {
				runtime.t.Fatal(err)
			}
			runtime.session = append(runtime.session, append(entry, '\n')...)
			parent = id
		}
		for i, arg := range spec.Command {
			if arg == "--session-dir" {
				runtime.files[path.Join(spec.Command[i+1], "2026-08-27T13-15-30-123Z_0192f7e5-7b2a-7c01-8a11-223344556677.jsonl")] = bytes.Clone(runtime.session)
			}
		}
		output.Stdout = piWorkflowStdout()
	} else if runtime.kind == worker.WorkerTypeClaudeCode {
		if step.raw {
			envelope, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "result": step.result})
			output.Stdout = string(envelope)
		} else {
			output.Stdout = `{"type":"result","subtype":"success","structured_output":` + step.result + `}`
		}
	} else {
		output.Stdout = step.result
		output.Stderr = "session id: 12345678-abcd-4abc-8def-123456789abc\n" + `{"outcome":{"kind":"decline","reason":"alternate channel"}}`
	}
	if spec.OutputCapture != nil {
		spec.OutputCapture.WriteStdout([]byte(output.Stdout))
		spec.OutputCapture.WriteStderr([]byte(output.Stderr))
	}
	runtime.ConfigureExecution(testutil.ExecutionKey{RunID: spec.RunID, Phase: spec.Phase}, testutil.ExecutionBehavior{Output: output, StoppedFiles: runtime.files})
	return runtime.MemoryRuntime.Launch(ctx, spec)
}

func assertAdapterLaunchMaterials(t *testing.T, kind worker.WorkerType, launches []rundomain.LaunchSpec) {
	t.Helper()
	if kind == worker.WorkerTypeCodex {
		assertCodexLaunchMaterials(t, launches)
		return
	}
	seen := make(map[string]bool)
	for i, launch := range launches {
		if launch.RenderedInput.Hash != launches[0].RenderedInput.Hash || !bytes.Equal(launch.RenderedInput.Bytes, launches[0].RenderedInput.Bytes) || !launch.RenderedInput.Hash.Matches(launch.RenderedInput.Bytes) {
			t.Fatal("continuation changed admitted input")
		}
		if kind != worker.WorkerTypePi {
			continue
		}
		file := launch.ProtocolFile
		if file == nil || file.Validate() != nil || !file.Writable || seen[file.Path()] || !strings.Contains(launch.Command[len(launch.Command)-1], file.Path()) {
			t.Fatalf("invalid or reused result target: %+v", file)
		}
		seen[file.Path()] = true
		if i > 0 && !strings.Contains(strings.Join(launch.Command, "\n"), "--session\n0192f7e5-7b2a-7c01-8a11-223344556677") {
			t.Fatal("continuation lost verified Session")
		}
	}
}

func TestAdapterWorkflowsRejectLegacyTextWithoutAlternativeChannelRescue(t *testing.T) {
	const result = `{"outcome":{"kind":"no_change"}}`
	for _, legacy := range []string{"----- AETHER RUN RESULT BEGIN -----\n" + result + "\n----- AETHER RUN RESULT END -----", "```json\n" + result + "\n```", "explanation " + result, result} {
		for _, kind := range []worker.WorkerType{worker.WorkerTypeClaudeCode, worker.WorkerTypeCodex, worker.WorkerTypePi} {
			t.Run(string(kind)+"/"+legacy[:8], func(t *testing.T) {
				submission := legacy
				if legacy == result && kind == worker.WorkerTypeCodex {
					submission = `{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`
				}
				runtime := newAdapterResultRuntime(t, kind, adapterResultStep{result: submission, raw: true}, adapterResultStep{result: submission, raw: true})
				flow := newAdapterWorkflow(t, rundomain.TaskReason, kind, runtime)
				receipt := finishWorkflowCompletion(t, t.Context(), flow.runs, flow.executor.Execute(t.Context(), flow.started.StartedRun))
				if receipt.Status != rundomain.StatusFailed || receipt.ResultHash != "" {
					t.Fatalf("legacy text accepted: %+v", receipt)
				}
				detail, err := flow.projects.Get(t.Context(), flow.started.Run.ProjectID)
				if err != nil || detail.Project.Revision != 1 || len(detail.Intents) != 0 {
					t.Fatalf("rejected result changed Blackboard: %+v %v", detail, err)
				}
			})
		}
	}
}

func TestPiWorkflowsContinueWithIndependentResultFiles(t *testing.T) {
	const partial = `{"outcome":{"kind":"conclude_intent","fact":{"description":"verified evidence"}}}`
	const complete = `{"outcome":{"kind":"complete_project","description":"complete","fact":{"description":"verified evidence"}}}`
	invalid := adapterResultStep{result: "[outcome]\nkind = \"\"\n", raw: true}
	for _, task := range []rundomain.TaskType{rundomain.TaskExplore, rundomain.TaskBootstrap} {
		for _, mode := range []string{"timeout conclude", "primary recovery", "conclude recovery", "finalization success", "finalization failure", "stale finalization", "forbidden completion", "unknown field"} {
			t.Run(string(task)+"/"+mode, func(t *testing.T) {
				steps := []adapterResultStep{invalid, {result: partial}}
				want := rundomain.StatusCommitted
				switch mode {
				case "timeout conclude":
					steps[0].timedOut = true
				case "primary recovery":
					steps[0] = adapterResultStep{result: complete, exitCode: 9}
				case "conclude recovery":
					steps[0].timedOut = true
					steps[1].exitCode = 9
					steps = append(steps, adapterResultStep{result: partial})
				case "finalization success":
					steps[1] = invalid
					steps = append(steps, adapterResultStep{result: partial})
				case "finalization failure":
					steps[1] = invalid
					steps = append(steps, invalid)
					want = rundomain.StatusFailed
				case "stale finalization":
					steps[0] = adapterResultStep{result: partial, timedOut: true}
					steps[1] = invalid
					steps = append(steps, adapterResultStep{stale: true, raw: true})
					want = rundomain.StatusFailed
				case "forbidden completion":
					steps[1] = adapterResultStep{result: complete}
					steps = append(steps, adapterResultStep{result: complete})
					want = rundomain.StatusFailed
				case "unknown field":
					bad := adapterResultStep{result: "[outcome]\nkind = \"decline\"\nreason = \"blocked\"\nextra = \"invalid\"\n", raw: true}
					steps = []adapterResultStep{bad, bad, bad}
					want = rundomain.StatusFailed
				}
				runtime := newAdapterResultRuntime(t, worker.WorkerTypePi, steps...)
				flow := newAdapterWorkflow(t, task, worker.WorkerTypePi, runtime)
				before, err := flow.projects.Get(t.Context(), flow.started.Run.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				receipt := finishWorkflowCompletion(t, t.Context(), flow.runs, flow.executor.Execute(t.Context(), flow.started.StartedRun))
				if receipt.Status != want || (want == rundomain.StatusCommitted && (receipt.OutcomeKind != rundomain.OutcomeConcludeIntent || len(receipt.CreatedFactIDs) != 1 || receipt.ResultEvidenceSource != rundomain.ResultEvidenceSourceFile)) {
					t.Fatalf("receipt: %+v", receipt)
				}
				if len(runtime.launches) != len(steps) {
					t.Fatalf("Invocation count: %d, want %d", len(runtime.launches), len(steps))
				}
				if want == rundomain.StatusFailed {
					after, err := flow.projects.Get(t.Context(), flow.started.Run.ProjectID)
					if err != nil || after.Project.Revision != before.Project.Revision || len(after.Facts) != len(before.Facts) || len(after.Intents) != len(before.Intents) || receipt.ResultHash != "" {
						t.Fatalf("rejected result changed Blackboard: %+v %v", after, err)
					}
				}
				assertAdapterLaunchMaterials(t, worker.WorkerTypePi, runtime.launches)
				if strings.HasPrefix(mode, "finalization") || mode == "stale finalization" || mode == "forbidden completion" {
					if runtime.launches[1].Phase != runtime.launches[2].Phase {
						t.Fatal("finalization no longer uses Conclude phase")
					}
				}
			})
		}
	}
}

func TestPiPrimaryRecoveryCommitsEveryPermittedOutcome(t *testing.T) {
	for _, test := range []struct {
		task   rundomain.TaskType
		result string
		kind   rundomain.OutcomeKind
	}{
		{rundomain.TaskExplore, `{"outcome":{"kind":"conclude_intent","fact":{"description":"recovered evidence"}}}`, rundomain.OutcomeConcludeIntent},
		{rundomain.TaskExplore, `{"outcome":{"kind":"decline","reason":"blocked"}}`, rundomain.OutcomeDecline},
		{rundomain.TaskBootstrap, `{"outcome":{"kind":"complete_project","description":"complete","fact":{"description":"recovered evidence"}}}`, rundomain.OutcomeCompleteProject},
		{rundomain.TaskBootstrap, `{"outcome":{"kind":"conclude_intent","fact":{"description":"recovered evidence"}}}`, rundomain.OutcomeConcludeIntent},
		{rundomain.TaskBootstrap, `{"outcome":{"kind":"decline","reason":"blocked"}}`, rundomain.OutcomeDecline},
	} {
		t.Run(string(test.task)+"/"+string(test.kind), func(t *testing.T) {
			runtime := newAdapterResultRuntime(t, worker.WorkerTypePi, adapterResultStep{result: test.result, exitCode: 9}, adapterResultStep{result: test.result})
			flow := newAdapterWorkflow(t, test.task, worker.WorkerTypePi, runtime)
			receipt := finishWorkflowCompletion(t, t.Context(), flow.runs, flow.executor.Execute(t.Context(), flow.started.StartedRun))
			want := rundomain.StatusCommitted
			if test.kind == rundomain.OutcomeDecline {
				want = rundomain.StatusDeclined
			}
			if receipt.Status != want || receipt.OutcomeKind != test.kind || receipt.ResultEvidenceSource != rundomain.ResultEvidenceSourceFile || len(runtime.launches) != 2 {
				t.Fatalf("recovered receipt: %+v", receipt)
			}
			assertAdapterLaunchMaterials(t, worker.WorkerTypePi, runtime.launches)
		})
	}
}
