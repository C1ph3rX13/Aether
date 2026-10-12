package run_test

import (
	"context"
	"path"
	"slices"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

// This Runtime delivers Pi stdout only through the capture supplied at launch.
// The real Catalog, RunExecutor and Pi evidence adapter handle each successor.
type piHandoffRuntime struct {
	*adapterResultRuntime
	readers []*piHandoffFileReader
}

func newPiHandoffRuntime(t *testing.T, steps ...adapterResultStep) *piHandoffRuntime {
	return &piHandoffRuntime{adapterResultRuntime: newAdapterResultRuntime(t, worker.WorkerTypePi, steps...)}
}

func (runtime *piHandoffRuntime) Launch(ctx context.Context, spec rundomain.LaunchSpec) (rundomain.Process, error) {
	if spec.OutputCapture == nil {
		runtime.t.Fatal("Pi launch omitted its output capture")
	}
	spec.OutputCapture = piChunkedCapture{ProcessOutputCapture: spec.OutputCapture}
	process, err := runtime.adapterResultRuntime.Launch(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &piHandoffProcess{Process: process, runtime: runtime}, nil
}

type piChunkedCapture struct{ rundomain.ProcessOutputCapture }

func (capture piChunkedCapture) WriteStdout(data []byte) {
	for _, value := range data {
		capture.ProcessOutputCapture.WriteStdout([]byte{value})
	}
}

type piHandoffProcess struct {
	rundomain.Process
	runtime *piHandoffRuntime
}

func (process *piHandoffProcess) Wait(ctx context.Context) (rundomain.ProcessOutput, error) {
	output, err := process.Process.Wait(ctx)
	// A fresh capture reconstructed from ProcessOutput cannot recover the
	// Session header: all evidence was delivered through the launch capture.
	output.Stdout = ""
	reader := &piHandoffFileReader{StoppedProcessFileReader: output.StoppedProcessFileReader}
	process.runtime.readers = append(process.runtime.readers, reader)
	output.StoppedProcessFileReader = reader
	return output, err
}

type piHandoffFileReader struct {
	rundomain.StoppedProcessFileReader
	requests [][]string
}

func (reader *piHandoffFileReader) ReadFiles(ctx context.Context, paths []string) map[string]rundomain.StoppedProcessFile {
	reader.requests = append(reader.requests, slices.Clone(paths))
	return reader.StoppedProcessFileReader.ReadFiles(ctx, paths)
}

func assertPiHandoffReads(t *testing.T, runtime *piHandoffRuntime) {
	t.Helper()
	if len(runtime.readers) != len(runtime.launches) {
		t.Fatalf("stopped readers: %d, want %d", len(runtime.readers), len(runtime.launches))
	}
	for i, reader := range runtime.readers {
		launch := runtime.launches[i]
		dirIndex := slices.Index(launch.Command, "--session-dir")
		if dirIndex < 0 || dirIndex+1 >= len(launch.Command) {
			t.Fatal("Pi launch omitted its Session directory")
		}
		want := []string{
			path.Join(launch.Command[dirIndex+1], "2026-08-27T13-15-30-123Z_0192f7e5-7b2a-7c01-8a11-223344556677.jsonl"),
			launch.ProtocolFile.Path(),
		}
		if len(reader.requests) != 1 || !slices.Equal(reader.requests[0], want) {
			t.Fatalf("Invocation %d file reads: %v, want one batch %v", i, reader.requests, want)
		}
	}
}

func TestPiWorkflowHandsLatestSessionFromRecoveryThroughConcludeAndFinalization(t *testing.T) {
	invalid := adapterResultStep{result: "[outcome]\nkind = \"\"\n", raw: true}
	for _, task := range []rundomain.TaskType{rundomain.TaskExplore, rundomain.TaskBootstrap} {
		t.Run(string(task), func(t *testing.T) {
			runtime := newPiHandoffRuntime(t,
				adapterResultStep{result: `{"outcome":{"kind":"complete_project","description":"unaccepted primary","fact":{"description":"unaccepted evidence"}}}`, exitCode: 9},
				invalid,
				invalid,
				adapterResultStep{result: `{"outcome":{"kind":"conclude_intent","fact":{"description":"finalized evidence"}}}`},
			)
			flow := newAdapterWorkflow(t, task, worker.WorkerTypePi, runtime)
			receipt := finishWorkflowCompletion(t, t.Context(), flow.runs, flow.executor.Execute(t.Context(), flow.started.StartedRun))
			if receipt.Status != rundomain.StatusCommitted || receipt.OutcomeKind != rundomain.OutcomeConcludeIntent ||
				len(receipt.CreatedFactIDs) != 1 || receipt.ResultEvidenceSource != rundomain.ResultEvidenceSourceFile {
				t.Fatalf("finalized receipt: %+v", receipt)
			}
			wantPhases := []rundomain.ExecutionPhase{
				rundomain.PhaseExplore, rundomain.PhaseExploreRecovery, rundomain.PhaseExploreConclude, rundomain.PhaseExploreConclude,
			}
			if task == rundomain.TaskBootstrap {
				wantPhases = []rundomain.ExecutionPhase{
					rundomain.PhaseBootstrap, rundomain.PhaseBootstrapRecovery, rundomain.PhaseBootstrapConclude, rundomain.PhaseBootstrapConclude,
				}
			}
			var phases []rundomain.ExecutionPhase
			for _, launch := range runtime.launches {
				phases = append(phases, launch.Phase)
			}
			if !slices.Equal(phases, wantPhases) {
				t.Fatalf("Invocation phases: %v, want %v", phases, wantPhases)
			}
			// The cumulative Session tree has one prompt per Invocation, so each
			// successor must resume from the last verified Cursor, even when the
			// original primary prepares it after Recovery or Conclude.
			assertAdapterLaunchMaterials(t, worker.WorkerTypePi, runtime.launches)
			assertPiHandoffReads(t, runtime)
		})
	}
}

func TestPiReasonFinalizationUsesVerifiedSessionAfterResultFileFailure(t *testing.T) {
	for _, failure := range []string{"missing", "invalid"} {
		for _, finalization := range []string{"valid", "invalid"} {
			t.Run(failure+"/"+finalization, func(t *testing.T) {
				primary := adapterResultStep{result: "[outcome]\nkind = \"\"\n", raw: true, stale: failure == "missing"}
				final := adapterResultStep{result: `{"outcome":{"kind":"no_change"}}`}
				wantStatus := rundomain.StatusCommitted
				if finalization == "invalid" {
					final = adapterResultStep{result: "[outcome]\nkind = \"\"\n", raw: true}
					wantStatus = rundomain.StatusFailed
				}
				runtime := newPiHandoffRuntime(t, primary, final)
				flow := newAdapterWorkflow(t, rundomain.TaskReason, worker.WorkerTypePi, runtime)
				before, err := flow.projects.Get(t.Context(), flow.started.Run.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				receipt := finishWorkflowCompletion(t, t.Context(), flow.runs, flow.executor.Execute(t.Context(), flow.started.StartedRun))
				if receipt.Status != wantStatus {
					t.Fatalf("Reason receipt: %+v", receipt)
				}
				if wantStatus == rundomain.StatusCommitted && (receipt.OutcomeKind != rundomain.OutcomeNoChange || receipt.ResultEvidenceSource != rundomain.ResultEvidenceSourceFile) {
					t.Fatalf("finalized Reason result: %+v", receipt)
				}
				if wantStatus == rundomain.StatusFailed && receipt.ResultHash != "" {
					t.Fatalf("invalid finalization retained a result: %+v", receipt)
				}
				if len(runtime.launches) != 2 || runtime.launches[0].Phase != rundomain.PhaseReason || runtime.launches[1].Phase != rundomain.PhaseReasonFinalization {
					t.Fatalf("Reason launches: %+v", runtime.launches)
				}
				after, err := flow.projects.Get(t.Context(), flow.started.Run.ProjectID)
				if err != nil || after.Project.Revision != before.Project.Revision || len(after.Facts) != len(before.Facts) || len(after.Intents) != len(before.Intents) {
					t.Fatalf("Reason file failure changed Blackboard: %+v %v", after, err)
				}
				assertAdapterLaunchMaterials(t, worker.WorkerTypePi, runtime.launches)
				assertPiHandoffReads(t, runtime)
			})
		}
	}
}
