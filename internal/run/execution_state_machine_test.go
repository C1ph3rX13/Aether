package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReasonExecutorValidatesSnapshotAndLaunchesBoundExecution(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	candidate := json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`)
	captureRequested := false
	adapter := &testReasonAdapter{candidate: candidate, captureRequested: &captureRequested}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeNoChange || completion.Code != "" {
		t.Fatalf("completion: %+v", completion)
	}
	finish, err := completion.FinishRequest()
	if err != nil {
		t.Fatal(err)
	}
	if finish.RunID != started.Run.ID || finish.FencingToken != started.Run.FencingToken {
		t.Fatalf("finish identity: %+v", finish)
	}
	result, _, _, err := DecodeRunResult(finish.Candidate, TaskReason, 3)
	if err != nil || result.Outcome.Kind != OutcomeNoChange {
		t.Fatalf("finish candidate: result=%+v err=%v", result, err)
	}

	snapshot := runtime.Snapshot()
	if len(snapshot.Launches) != 1 {
		t.Fatalf("runtime snapshot: %+v", snapshot)
	}
	launch := snapshot.Launches[0]
	if !captureRequested || launch.OutputCapture == nil {
		t.Fatalf("optional Adapter capture was not attached: requested=%t launch=%+v", captureRequested, launch)
	}
	if launch.Image != started.Run.Spec.Image.RequestedRef || launch.RenderedInput == nil ||
		launch.Task != TaskReason || !launch.RenderedInput.Hash.Matches(launch.RenderedInput.Bytes) ||
		string(launch.RenderedInput.Hash) == started.Run.Spec.Input.Identity.SHA256 {
		t.Fatalf("launch spec: %+v", launch)
	}
	if len(adapter.lastCommand) == 0 || adapter.lastCommand[0] != "agent" {
		t.Fatalf("adapter invocation was not used: %+v", adapter.lastCommand)
	}
	captureDeadline, ok := adapter.captureContext.Deadline()
	if !ok || !captureDeadline.Equal(started.Run.StartedAt.Add(time.Duration(started.Run.Spec.Timeouts.RunSeconds)*time.Second)) {
		t.Fatalf("Adapter capture context deadline=%v present=%t", captureDeadline, ok)
	}
}

func TestReasonExecutorFinalizesInvalidResultInSameSession(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 77, PhaseSeconds: 45, GraceSeconds: 1}
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true})
	runtime.QueueOutput(ProcessOutput{Exited: true})
	adapter := &testReasonFinalizationAdapter{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, FinalizationPrompt: "Re-serialize only the confirmed Reason result.",
		PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeNoChange {
		t.Fatalf("finalized completion: %+v", completion)
	}
	if adapter.finalizationCalls != 1 || adapter.finalizationSession != "reason-session" ||
		adapter.finalizationPrompt != "Re-serialize only the confirmed Reason result." {
		t.Fatalf("finalization continuation: calls=%d session=%q prompt=%q",
			adapter.finalizationCalls, adapter.finalizationSession, adapter.finalizationPrompt)
	}
	launches := runtime.Snapshot().Launches
	if len(launches) != 2 || launches[0].Phase != PhaseReason || launches[1].Phase != PhaseReasonFinalization ||
		launches[1].PhaseTimeout != 30*time.Second || launches[1].RenderedInput == nil ||
		launches[1].RenderedInput.Path != BlackboardPath(started.Run.ID, PhaseReasonFinalization) ||
		launches[0].FencingToken != launches[1].FencingToken || launches[0].RunID != launches[1].RunID {
		t.Fatalf("finalization launches: %+v", launches)
	}
}

func TestReasonExecutorFinalizesEligibleCaptureFailures(t *testing.T) {
	for _, failure := range []CaptureFailureReason{CaptureFailureNoCandidate, CaptureFailureInvalidCandidate} {
		t.Run(string(failure), func(t *testing.T) {
			started, input, prompt := reasonExecutorFixture(t)
			runtime := newRuntimeTestDouble()
			runtime.QueueOutput(ProcessOutput{Exited: true})
			runtime.QueueOutput(ProcessOutput{Exited: true})
			adapter := &testReasonFinalizationAdapter{
				primaryCapture: CapturedResult{CaptureFailureReason: failure},
				primaryErr:     errors.New("private parser failure"),
			}
			executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
				Prompt: prompt, FinalizationPrompt: "Finalize the confirmed Reason outcome.",
				PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
			})
			if err != nil {
				t.Fatal(err)
			}

			completion := executor.Execute(t.Context(), started)
			if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeNoChange ||
				adapter.finalizationCalls != 1 || len(runtime.Snapshot().Launches) != 2 {
				t.Fatalf("eligible capture Finalization: completion=%+v calls=%d launches=%+v",
					completion, adapter.finalizationCalls, runtime.Snapshot().Launches)
			}
		})
	}
}

func TestReasonExecutorDoesNotFinalizeIneligibleFailures(t *testing.T) {
	tests := []struct {
		name       string
		output     ProcessOutput
		capture    CapturedResult
		captureErr error
		kind       CompletionKind
		code       ExecutionErrorCode
		reason     CaptureFailureReason
	}{
		{name: "multiple candidates", output: ProcessOutput{Exited: true}, capture: CapturedResult{CaptureFailureReason: CaptureFailureMultipleCandidates}, captureErr: errors.New("multiple"), kind: CompletionFailed, code: ExecutionCaptureFailed, reason: CaptureFailureMultipleCandidates},
		{name: "candidate too large", output: ProcessOutput{Exited: true}, capture: CapturedResult{CaptureFailureReason: CaptureFailureCandidateTooLarge}, captureErr: errors.New("large"), kind: CompletionFailed, code: ExecutionCaptureFailed, reason: CaptureFailureCandidateTooLarge},
		{name: "evidence conflict", output: ProcessOutput{Exited: true}, capture: CapturedResult{CaptureFailureReason: CaptureFailureBoundaryError}, captureErr: errors.New("conflict"), kind: CompletionFailed, code: ExecutionCaptureFailed, reason: CaptureFailureBoundaryError},
		{name: "process failure", output: ProcessOutput{Exited: true, ExitCode: 9}, kind: CompletionFailed, code: ExecutionProcessFailed},
		{name: "timeout", output: ProcessOutput{Exited: true, TimedOut: true}, kind: CompletionFailed, code: ExecutionDeadline},
		{name: "cancellation", output: ProcessOutput{Exited: true, Cancelled: true}, kind: CompletionInterrupted},
		{name: "stream failure", output: ProcessOutput{Exited: true, ErrorCode: ProcessErrorStreamRead}, kind: CompletionFailed, code: ExecutionProcessStream},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started, input, prompt := reasonExecutorFixture(t)
			runtime := newRuntimeTestDouble()
			runtime.QueueOutput(test.output)
			adapter := &testReasonFinalizationAdapter{primaryCapture: test.capture, primaryErr: test.captureErr}
			executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
				Prompt: prompt, FinalizationPrompt: "Finalize the confirmed Reason outcome.",
				PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
			})
			if err != nil {
				t.Fatal(err)
			}

			completion := executor.Execute(t.Context(), started)
			if completion.Kind != test.kind || completion.Code != test.code ||
				completion.CaptureFailureReason != test.reason || adapter.finalizationCalls != 0 ||
				len(runtime.Snapshot().Launches) != 1 {
				t.Fatalf("ineligible failure: completion=%+v calls=%d launches=%+v",
					completion, adapter.finalizationCalls, runtime.Snapshot().Launches)
			}
		})
	}
}

func TestReasonExecutorDoesNotFinalizeAfterContinuationAuthorizationFailure(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true})
	adapter := &testReasonFinalizationAdapter{}
	authorizer := &testContinuationAuthorizer{err: &DomainError{Code: ErrorProjectInactive, Detail: "paused"}}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, authorizer).BindReason(started, ReasonExecution{
		Prompt: prompt, FinalizationPrompt: "Finalize the confirmed Reason outcome.",
		PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionCancelled || completion.Code != ErrorProjectInactive ||
		authorizer.calls != 1 || adapter.finalizationCalls != 0 || len(runtime.Snapshot().Launches) != 1 {
		t.Fatalf("authorization failure: completion=%+v authorizations=%d finalizations=%d launches=%+v",
			completion, authorizer.calls, adapter.finalizationCalls, runtime.Snapshot().Launches)
	}
}

func TestReasonExecutorRequiresFinalizationCapability(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true})
	adapter := &testReasonAdapter{candidate: protocolInvalidReasonResult, continuation: true}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, FinalizationPrompt: "Finalize the confirmed Reason outcome.",
		PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionInvalidResult ||
		len(runtime.Snapshot().Launches) != 1 {
		t.Fatalf("unsupported Finalization capability: completion=%+v launches=%+v",
			completion, runtime.Snapshot().Launches)
	}
}

func TestReasonExecutorStopsAfterProtocolInvalidFinalization(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true})
	runtime.QueueOutput(ProcessOutput{Exited: true})
	adapter := &testReasonFinalizationAdapter{
		finalizationCapture: CapturedResult{Candidate: protocolInvalidReasonResult},
	}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, FinalizationPrompt: "Finalize the confirmed Reason outcome.",
		PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionInvalidResult ||
		adapter.finalizationCalls != 1 || len(runtime.Snapshot().Launches) != 2 {
		t.Fatalf("recursive Finalization: completion=%+v calls=%d launches=%+v",
			completion, adapter.finalizationCalls, runtime.Snapshot().Launches)
	}
}

type testReasonFinalizationAdapter struct {
	finalizationCalls   int
	finalizationSession string
	finalizationPrompt  string
	primaryCapture      CapturedResult
	primaryErr          error
	finalizationCapture CapturedResult
	finalizationErr     error
}

func (adapter *testReasonFinalizationAdapter) TypeName() string { return "test" }

func (adapter *testReasonFinalizationAdapter) PrepareReason(ReasonRequest) (AgentInvocation, error) {
	return &testReasonFinalizationInvocation{owner: adapter, session: "reason-session"}, nil
}

type testReasonFinalizationInvocation struct {
	owner        *testReasonFinalizationAdapter
	finalization bool
	session      string
}

func (invocation *testReasonFinalizationInvocation) Command() []string {
	return []string{"agent", "reason"}
}

func (invocation *testReasonFinalizationInvocation) Environment() map[string]string { return nil }

func (invocation *testReasonFinalizationInvocation) CanContinueSession() bool {
	return invocation.session != ""
}

func (invocation *testReasonFinalizationInvocation) Capture(context.Context, ProcessOutput) (CapturedResult, error) {
	if !invocation.finalization {
		if invocation.owner.primaryErr != nil || len(invocation.owner.primaryCapture.Candidate) > 0 ||
			invocation.owner.primaryCapture.CaptureFailureReason != "" {
			return invocation.owner.primaryCapture, invocation.owner.primaryErr
		}
		return CapturedResult{Candidate: protocolInvalidReasonResult}, nil
	}
	if invocation.owner.finalizationErr != nil || len(invocation.owner.finalizationCapture.Candidate) > 0 ||
		invocation.owner.finalizationCapture.CaptureFailureReason != "" {
		return invocation.owner.finalizationCapture, invocation.owner.finalizationErr
	}
	return CapturedResult{Candidate: json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`)}, nil
}

func (invocation *testReasonFinalizationInvocation) PrepareReasonFinalization(
	request ReasonFinalizationRequest,
) (AgentInvocation, error) {
	invocation.owner.finalizationCalls++
	invocation.owner.finalizationSession = invocation.session
	invocation.owner.finalizationPrompt = request.Prompt
	return &testReasonFinalizationInvocation{
		owner: invocation.owner, finalization: true, session: invocation.session,
	}, nil
}

var protocolInvalidReasonResult = json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change","unexpected":true}}`)

var _ ReasonFinalizationInvocation = (*testReasonFinalizationInvocation)(nil)

func TestExecutionBindingRejectsStructuredInputIdentityMismatch(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	input.Project.Goal = "changed after admission"
	runtime := newRuntimeTestDouble()
	_, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: &testReasonAdapter{},
	})
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != ErrorExecutionContractMismatch {
		t.Fatalf("structured Input mismatch classification: %v", err)
	}
	snapshot := runtime.Snapshot()
	if len(snapshot.Launches) != 0 {
		t.Fatalf("mismatched material reached runtime: %+v", snapshot)
	}
}

func TestBindExecutorRejectsDeterministicRenderedInputMismatchBeforeRuntime(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	_, expectedHash, err := input.MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	_, err = newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, RenderedInput: &RenderedInput{
			Bytes: []byte("private forged input"), Hash: expectedHash,
		},
		Adapter: &testReasonAdapter{},
	})
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != ErrorExecutionContractMismatch {
		t.Fatalf("deterministic rendering mismatch classification: %v", err)
	}
	if !strings.Contains(err.Error(), "rendered Input bytes or hash") {
		t.Fatalf("deterministic rendering mismatch omitted the evidence field: %v", err)
	}
	for _, sensitive := range []string{"private forged input", prompt, "/runtime/"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("deterministic rendering diagnostic exposed %q: %v", sensitive, err)
		}
	}
	if snapshot := runtime.Snapshot(); len(snapshot.Launches) != 0 {
		t.Fatalf("deterministic rendering mismatch reached Runtime: %+v", snapshot)
	}
}

func TestBindExecutorFreezesMaterialAndKeepsProcessFailureOrdinary(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 9})
	adapter := &testReasonAdapter{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if adapter.prepared != 1 {
		t.Fatalf("Adapter prepared %d times during Binding, want 1", adapter.prepared)
	}
	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionProcessFailed {
		t.Fatalf("post-Binding process failure classification: %+v", completion)
	}
	if adapter.prepared != 1 {
		t.Fatalf("frozen Adapter prepared %d times after Execute", adapter.prepared)
	}
}

func TestBoundExecutorRejectsDifferentStartedRunBeforeRuntime(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: &testReasonAdapter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	different := started
	different.Run.ID = "different-run"
	different.Claim.RunID = different.Run.ID
	completion := executor.Execute(t.Context(), different)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionInvalidRun {
		t.Fatalf("different Started Run completion: %+v", completion)
	}
	if snapshot := runtime.Snapshot(); len(snapshot.Launches) != 0 {
		t.Fatalf("different Started Run reached Runtime: %+v", snapshot)
	}
}

func TestExecutionBindingSupportsEveryAdmittedTask(t *testing.T) {
	reasonStarted, reasonInput, reasonPrompt := reasonExecutorFixture(t)
	exploreStarted, exploreInput, explorePrompt, exploreFallback := exploreExecutorFixture(t)
	bootstrapStarted, bootstrapInput, bootstrapPrompt, bootstrapFallback := bootstrapExecutorFixture(t)
	tests := []struct {
		name    string
		started StartedRun
		bind    func(ExecutionBinding) (Executor, error)
	}{
		{
			name: "reason", started: reasonStarted,
			bind: func(binding ExecutionBinding) (Executor, error) {
				return binding.BindReason(reasonStarted, ReasonExecution{
					Prompt: reasonPrompt, PromptTemplateHash: reasonStarted.Run.Spec.PromptTemplateHash,
					Input: reasonInput, Adapter: &testReasonAdapter{},
				})
			},
		},
		{
			name: "explore", started: exploreStarted,
			bind: func(binding ExecutionBinding) (Executor, error) {
				return binding.BindExplore(exploreStarted, ExploreExecution{
					Prompt: explorePrompt, FallbackPrompt: exploreFallback,
					PromptTemplateHash: exploreStarted.Run.Spec.PromptTemplateHash,
					Input:              exploreInput, Adapter: &testExploreAdapter{},
				})
			},
		},
		{
			name: "bootstrap", started: bootstrapStarted,
			bind: func(binding ExecutionBinding) (Executor, error) {
				return binding.BindBootstrap(bootstrapStarted, BootstrapExecution{
					Prompt: bootstrapPrompt, FallbackPrompt: bootstrapFallback,
					PromptTemplateHash: bootstrapStarted.Run.Spec.PromptTemplateHash,
					Input:              bootstrapInput, Adapter: &testBootstrapAdapter{},
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newRuntimeTestDouble()
			executor, err := test.bind(newTestExecutionBinding(t, runtime, nil, nil, nil))
			if err != nil || executor == nil {
				t.Fatalf("bind %s execution: executor=%v error=%v", test.name, executor, err)
			}
			if snapshot := runtime.Snapshot(); len(snapshot.Launches) != 0 {
				t.Fatalf("%s Binding reached Runtime: %+v", test.name, snapshot)
			}
		})
	}
}

func TestExecutionBindingRejectsTaskMismatchBeforeRuntime(t *testing.T) {
	reasonStarted, reasonInput, reasonPrompt := reasonExecutorFixture(t)
	exploreStarted, exploreInput, explorePrompt, exploreFallback := exploreExecutorFixture(t)
	bootstrapStarted, bootstrapInput, bootstrapPrompt, bootstrapFallback := bootstrapExecutorFixture(t)
	for _, test := range []struct {
		name    string
		started StartedRun
		bind    func(ExecutionBinding) (Executor, error)
	}{
		{name: "reason with Explore binding", started: reasonStarted, bind: func(binding ExecutionBinding) (Executor, error) {
			return binding.BindExplore(reasonStarted, ExploreExecution{
				Prompt: explorePrompt, FallbackPrompt: exploreFallback,
				PromptTemplateHash: reasonStarted.Run.Spec.PromptTemplateHash,
				Input:              exploreInput, Adapter: &testExploreAdapter{},
			})
		}},
		{name: "explore with Bootstrap binding", started: exploreStarted, bind: func(binding ExecutionBinding) (Executor, error) {
			return binding.BindBootstrap(exploreStarted, BootstrapExecution{
				Prompt: bootstrapPrompt, FallbackPrompt: bootstrapFallback,
				PromptTemplateHash: exploreStarted.Run.Spec.PromptTemplateHash,
				Input:              bootstrapInput, Adapter: &testBootstrapAdapter{},
			})
		}},
		{name: "bootstrap with Reason binding", started: bootstrapStarted, bind: func(binding ExecutionBinding) (Executor, error) {
			return binding.BindReason(bootstrapStarted, ReasonExecution{
				Prompt: reasonPrompt, PromptTemplateHash: bootstrapStarted.Run.Spec.PromptTemplateHash,
				Input: reasonInput, Adapter: &testReasonAdapter{},
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := newRuntimeTestDouble()
			_, err := test.bind(newTestExecutionBinding(t, runtime, nil, nil, nil))
			var domainErr *DomainError
			if !errors.As(err, &domainErr) || domainErr.Code != ErrorExecutionContractMismatch {
				t.Fatalf("task Binding mismatch classification: %v", err)
			}
			if snapshot := runtime.Snapshot(); len(snapshot.Launches) != 0 {
				t.Fatalf("task Binding mismatch reached Runtime: %+v", snapshot)
			}
		})
	}
}

func TestExecutionBindingClassifiesAdapterPreparationFailure(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	privateFailure := "private adapter preparation failure"
	_, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: &testReasonAdapter{prepareError: errors.New(privateFailure)},
	})
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != ErrorExecutionContractMismatch ||
		!strings.Contains(domainErr.Detail, "Reason Adapter invocation") {
		t.Fatalf("Adapter preparation classification: %v", err)
	}
	if strings.Contains(err.Error(), privateFailure) {
		t.Fatalf("Adapter preparation mismatch exposed the underlying error: %v", err)
	}
	if snapshot := runtime.Snapshot(); len(snapshot.Launches) != 0 {
		t.Fatalf("Adapter preparation failure reached Runtime: %+v", snapshot)
	}
}

func TestReasonExecutorPreservesStableProcessErrorCodes(t *testing.T) {
	tests := []struct {
		name string
		from ProcessErrorCode
		want ExecutionErrorCode
	}{
		{name: "output too large", from: ProcessErrorOutputTooLarge, want: ExecutionOutputTooLarge},
		{name: "stream read", from: ProcessErrorStreamRead, want: ExecutionProcessStream},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started, input, prompt := reasonExecutorFixture(t)
			runtime := newRuntimeTestDouble()
			runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 1, ErrorCode: test.from})
			adapter := &testReasonAdapter{candidate: json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`)}
			executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
				Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
			})
			if err != nil {
				t.Fatal(err)
			}

			completion := executor.Execute(t.Context(), started)
			if completion.Kind != CompletionFailed || completion.Code != test.want || adapter.captured {
				t.Fatalf("completion=%+v captured=%t", completion, adapter.captured)
			}
		})
	}
}

func TestReasonExecutorDoesNotTreatQuietProcessAsStalled(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	process := &quietProcess{delay: 25 * time.Millisecond}
	runtime := &quietRuntime{process: process}
	adapter := &testReasonAdapter{candidate: json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`)}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || process.stopped || runtime.disposed != "" {
		t.Fatalf("quiet execution: completion=%+v stopped=%t disposed=%q", completion, process.stopped, runtime.disposed)
	}
}

func TestReasonExecutorClassifiesContextCancellationWithoutCapturingOutput(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	process := &quietProcess{block: true}
	runtime := &quietRuntime{process: process}
	adapter := &testReasonAdapter{candidate: json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`)}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		time.Sleep(15 * time.Millisecond)
		cancel()
	}()
	completion := executor.Execute(ctx, started)
	if completion.Kind != CompletionCancelled || adapter.captured {
		t.Fatalf("cancelled completion=%+v captured=%t", completion, adapter.captured)
	}
}

func TestReasonExecutorClassifiesItsOwnDeadlineWithoutCapturingOutput(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 4, PhaseSeconds: 1, GraceSeconds: 1}
	process := &quietProcess{block: true}
	runtime := &quietRuntime{process: process}
	adapter := &testReasonAdapter{candidate: json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`)}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionDeadline || adapter.captured {
		t.Fatalf("deadline completion=%+v captured=%t", completion, adapter.captured)
	}
}

func TestReasonExecutorUsesPersistedStartedAtForRunDeadline(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	started.Run.StartedAt = time.Now().Add(-5 * time.Second)
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 4, PhaseSeconds: 1, GraceSeconds: 1}
	process := &quietProcess{delay: 10 * time.Millisecond}
	runtime := &quietRuntime{process: process}
	adapter := &testReasonAdapter{candidate: json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`)}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionDeadline || adapter.captured {
		t.Fatalf("persisted Run deadline completion=%+v captured=%t", completion, adapter.captured)
	}
}

type testReasonAdapter struct {
	candidate         json.RawMessage
	lastCommand       []string
	captured          bool
	prepared          int
	prepareError      error
	captureDiagnostic CaptureDiagnostic
	captureRequested  *bool
	captureContext    context.Context
	continuation      bool
}

func (adapter *testReasonAdapter) TypeName() string { return "test" }

func (adapter *testReasonAdapter) PrepareReason(ReasonRequest) (AgentInvocation, error) {
	adapter.prepared++
	if adapter.prepareError != nil {
		return nil, adapter.prepareError
	}
	invocation := &testReasonInvocation{owner: adapter}
	adapter.lastCommand = invocation.Command()
	return invocation, nil
}

type testReasonInvocation struct{ owner *testReasonAdapter }

type testProcessOutputCapture struct{}

func (testProcessOutputCapture) WriteStdout([]byte) {}
func (testProcessOutputCapture) WriteStderr([]byte) {}
func (testProcessOutputCapture) Result() ProcessOutputCaptureResult {
	return ProcessOutputCaptureResult{}
}

func (invocation *testReasonInvocation) NewOutputCapture() ProcessOutputCapture {
	if invocation.owner.captureRequested == nil {
		return nil
	}
	*invocation.owner.captureRequested = true
	return testProcessOutputCapture{}
}

func (invocation *testReasonInvocation) Command() []string { return []string{"agent", "reason"} }

func (invocation *testReasonInvocation) CanContinueSession() bool {
	return invocation.owner.continuation
}

func (invocation *testReasonInvocation) Environment() map[string]string {
	return map[string]string{"MODEL": "test"}
}

func (invocation *testReasonInvocation) Capture(ctx context.Context, _ ProcessOutput) (CapturedResult, error) {
	invocation.owner.captureContext = ctx
	invocation.owner.captured = true
	if len(invocation.owner.candidate) == 0 {
		return CapturedResult{}, errors.New("candidate missing")
	}
	return CapturedResult{
		Candidate:         append(json.RawMessage(nil), invocation.owner.candidate...),
		CaptureDiagnostic: invocation.owner.captureDiagnostic,
	}, nil
}

type quietRuntime struct {
	process  *quietProcess
	disposed RunID
}

func (runtime *quietRuntime) Launch(context.Context, LaunchSpec) (Process, error) {
	return runtime.process, nil
}

type quietProcess struct {
	delay   time.Duration
	block   bool
	stopped bool
}

func (process *quietProcess) Wait(ctx context.Context) (ProcessOutput, error) {
	if process.block {
		<-ctx.Done()
		process.stopped = true
		return ProcessOutput{Exited: true, Cancelled: true, ExitCode: 130}, ctx.Err()
	}
	select {
	case <-time.After(process.delay):
		return ProcessOutput{Exited: true}, nil
	case <-ctx.Done():
		return ProcessOutput{Exited: true, Cancelled: true, ExitCode: 130}, ctx.Err()
	}
}

func (process *quietProcess) Stop(context.Context, time.Duration) error {
	process.stopped = true
	return nil
}

func reasonExecutorFixture(t *testing.T) (StartedRun, BlackboardInput, string) {
	t.Helper()
	input := BlackboardInput{
		Schema: BlackboardInputSchema, Generation: 1, Revision: 4,
		Project: InputProject{Title: "Project", Origin: "origin", Goal: "goal"},
		Facts:   []InputFact{{ID: "origin", Description: "origin", Status: "active"}},
	}
	inputIdentity, err := input.InputIdentityV1()
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Review the Blackboard snapshot."
	promptDigest := sha256.Sum256([]byte(prompt))
	started := StartedRun{
		Run: ExecutionRun{
			ID: "run-test", ProjectID: "project-test", WorkKey: "reason:project-test:g1:r4",
			ClaimScope: "reason:project-test", Attempt: 1, Status: StatusActive, FencingToken: 7,
			DispatcherID: "dispatcher-a", DispatcherFencingToken: 1,
			Task: TaskReason, StartedAt: time.Now(),
			Spec: ExecutionSpecSnapshot{
				Schema:         ExecutionSpecSchemaV2,
				ConfigRevision: "config-v1", PromptTemplateHash: hex.EncodeToString(promptDigest[:]),
				Input:        InputSnapshot{Generation: 1, Revision: 4, Identity: inputIdentity},
				Worker:       WorkerSnapshot{Name: "reasoner", Adapter: "test"},
				Image:        ImageSnapshot{RequestedRef: "agent:latest", ImageID: "sha256:image", RepoDigest: "agent@sha256:digest"},
				Timeouts:     TimeoutSnapshot{RunSeconds: 12, PhaseSeconds: 5, GraceSeconds: 1},
				ResultSchema: ResultSchemaV1, MaxProposedIntents: 3,
			},
		},
		Claim: Claim{
			WorkKey: "reason:project-test:g1:r4", Scope: "reason:project-test", RunID: "run-test",
			ProjectID: "project-test", Task: TaskReason, FencingToken: 7, DispatcherID: "dispatcher-a",
		},
	}
	return started, input, prompt
}
