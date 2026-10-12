package run

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestBootstrapExecutorRecoversAbnormalPrimaryInSameSession(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	recoveryPrompt := "Continue the interrupted Bootstrap task."
	recoveryConcludePrompt := "Serialize the confirmed Bootstrap result."
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 30, PhaseSeconds: 5, FallbackPhaseSeconds: 5, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
	started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
	started.Run.Spec.PromptTemplateHash = BootstrapPromptTemplateHashWithRecoveryModes(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 9, Stdout: `{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","fact":{"description":"candidate before failure"}}}`})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	adapter := &testBootstrapAdapter{recoverySupported: true}
	authorizer := &testProcessRecoveryAuthorizer{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil, authorizer).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
		RecoveryConcludePrompt: recoveryConcludePrompt,
		PromptTemplateHash:     started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeConcludeIntent {
		t.Fatalf("recovered completion=%+v", completion)
	}
	if authorizer.calls != 1 || adapter.continuationObservations != 1 || adapter.captureCalls != 1 ||
		!adapter.recoveryPrepared || adapter.recoverySession != "bootstrap-session-1" {
		t.Fatalf("recovery reservation/session: calls=%d observations=%d captures=%d prepared=%t session=%q",
			authorizer.calls, adapter.continuationObservations, adapter.captureCalls, adapter.recoveryPrepared, adapter.recoverySession)
	}
	launches := runtime.Snapshot().Launches
	if len(launches) != 2 || launches[0].Phase != PhaseBootstrap || launches[1].Phase != PhaseBootstrapRecovery ||
		launches[1].PhaseTimeout != 2*time.Second || launches[0].RenderedInput.Hash != launches[1].RenderedInput.Hash ||
		launches[0].RenderedInput.Path != "" || launches[1].RenderedInput.Path == "" ||
		launches[1].RenderedInput.Path == launches[0].RenderedInput.Path {
		t.Fatalf("recovery launches: %+v", launches)
	}
}

func TestBootstrapExecutorDoesNotObserveSessionForProhibitedRecoveryExitClasses(t *testing.T) {
	for _, test := range []struct {
		name     string
		output   ProcessOutput
		waitErr  error
		wantKind CompletionKind
		wantCode ExecutionErrorCode
	}{
		{name: "cancelled", output: ProcessOutput{Exited: true, ExitCode: 130, Cancelled: true}, wantKind: CompletionInterrupted},
		{name: "stream failure", output: ProcessOutput{Exited: true, ExitCode: 9, ErrorCode: ProcessErrorStreamRead}, wantKind: CompletionFailed, wantCode: ExecutionProcessStream},
		{name: "output overflow", output: ProcessOutput{Exited: true, ExitCode: 9, ErrorCode: ProcessErrorOutputTooLarge}, wantKind: CompletionFailed, wantCode: ExecutionOutputTooLarge},
		{name: "container state failure", output: ProcessOutput{Exited: true, ExitCode: 9, ErrorCode: ProcessErrorContainerState}, wantKind: CompletionFailed, wantCode: ExecutionProcessFailed},
		{name: "unconfirmed stop", output: ProcessOutput{Exited: false}, wantKind: CompletionFailed, wantCode: ExecutionProcessFailed},
		{name: "wait failure", output: ProcessOutput{Exited: true, ExitCode: 9}, waitErr: errors.New("attach failed"), wantKind: CompletionFailed, wantCode: ExecutionProcessFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
			recoveryPrompt := "Continue the interrupted Bootstrap task."
			recoveryConcludePrompt := "Serialize the confirmed Bootstrap result."
			started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 30, PhaseSeconds: 5, FallbackPhaseSeconds: 5, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
			started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
			started.Run.Spec.PromptTemplateHash = BootstrapPromptTemplateHashWithRecoveryModes(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
			runtime := newRuntimeTestDouble()
			runtime.QueueResult(test.output, test.waitErr)
			adapter := &testBootstrapAdapter{recoverySupported: true}
			authorizer := &testProcessRecoveryAuthorizer{}
			executor, err := newTestExecutionBinding(t, runtime, nil, nil, authorizer, authorizer).BindBootstrap(started, BootstrapExecution{
				Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
				RecoveryConcludePrompt: recoveryConcludePrompt,
				PromptTemplateHash:     started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
			})
			if err != nil {
				t.Fatal(err)
			}

			completion := executor.Execute(t.Context(), started)
			if completion.Kind != test.wantKind || completion.Code != test.wantCode ||
				adapter.continuationObservations != 0 || adapter.recoveryPrepared || authorizer.calls != 0 ||
				authorizer.continuationCalls != 0 || len(runtime.Snapshot().Launches) != 1 {
				t.Fatalf("prohibited Bootstrap recovery exit: completion=%+v observations=%d prepared=%t reservations=%d continuations=%d launches=%d",
					completion, adapter.continuationObservations, adapter.recoveryPrepared, authorizer.calls,
					authorizer.continuationCalls, len(runtime.Snapshot().Launches))
			}
		})
	}
}

func TestBootstrapExecutorDoesNotObserveSessionWhenRecoveryAuthorizationFails(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	recoveryPrompt := "Continue the interrupted Bootstrap task."
	recoveryConcludePrompt := "Serialize the confirmed Bootstrap result."
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 30, PhaseSeconds: 5, FallbackPhaseSeconds: 5, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
	started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
	started.Run.Spec.PromptTemplateHash = BootstrapPromptTemplateHashWithRecoveryModes(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 9})
	adapter := &testBootstrapAdapter{recoverySupported: true}
	authorizer := &testProcessRecoveryAuthorizer{continuationErr: &DomainError{Code: ErrorProjectInactive, Detail: "paused"}}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, authorizer, authorizer).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
		RecoveryConcludePrompt: recoveryConcludePrompt,
		PromptTemplateHash:     started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionCancelled || completion.Code != ErrorProjectInactive || authorizer.calls != 0 ||
		authorizer.continuationCalls != 1 || adapter.continuationObservations != 0 || adapter.recoveryPrepared ||
		len(runtime.Snapshot().Launches) != 1 {
		t.Fatalf("unsafe Bootstrap recovery authorization: completion=%+v reservations=%d continuations=%d observations=%d prepared=%t launches=%d",
			completion, authorizer.calls, authorizer.continuationCalls, adapter.continuationObservations,
			adapter.recoveryPrepared, len(runtime.Snapshot().Launches))
	}
}

func TestBootstrapExecutorReauthorizesAfterTimeoutSessionObservation(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	recoveryPrompt := "Continue the interrupted Bootstrap task."
	recoveryConcludePrompt := "Serialize the confirmed Bootstrap result."
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 30, PhaseSeconds: 5, FallbackPhaseSeconds: 5, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
	started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
	started.Run.Spec.PromptTemplateHash = BootstrapPromptTemplateHashWithRecoveryModes(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	adapter := &testBootstrapAdapter{recoverySupported: true}
	authorizer := &testProcessRecoveryAuthorizer{
		continuationErr:   &DomainError{Code: ErrorProjectInactive, Detail: "paused during Session observation"},
		continuationErrAt: 2,
	}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, authorizer, authorizer).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
		RecoveryConcludePrompt: recoveryConcludePrompt,
		PromptTemplateHash:     started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionCancelled || completion.Code != ErrorProjectInactive || authorizer.calls != 0 ||
		authorizer.continuationCalls != 2 || adapter.continuationObservations != 1 || adapter.fallbackPrepared ||
		adapter.recoveryPrepared || len(runtime.Snapshot().Launches) != 1 {
		t.Fatalf("Bootstrap timeout reauthorization: completion=%+v reservations=%d continuations=%d observations=%d fallback=%t recovery=%t launches=%d",
			completion, authorizer.calls, authorizer.continuationCalls, adapter.continuationObservations,
			adapter.fallbackPrepared, adapter.recoveryPrepared, len(runtime.Snapshot().Launches))
	}
}

func TestBootstrapExecutorRecoversAbnormalConcludeWithoutCompletingProject(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	recoveryPrompt := "Continue the interrupted Bootstrap task."
	recoveryConcludePrompt := "Serialize the confirmed Bootstrap result without tools."
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 30, PhaseSeconds: 5, FallbackPhaseSeconds: 5, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
	started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
	started.Run.Spec.PromptTemplateHash = BootstrapPromptTemplateHashWithRecoveryModes(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 9, Stdout: `{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","fact":{"description":"candidate before conclude failure"}}}`})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	adapter := &testBootstrapAdapter{recoverySupported: true, primaryConclude: true}
	authorizer := &testProcessRecoveryAuthorizer{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil, authorizer).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
		RecoveryConcludePrompt: recoveryConcludePrompt,
		PromptTemplateHash:     started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeConcludeIntent {
		t.Fatalf("conclude recovery completion=%+v", completion)
	}
	if authorizer.calls != 1 || adapter.continuationObservations != 1 || adapter.captureCalls != 2 ||
		adapter.recoveryPhase != PhaseBootstrapConcludeRecovery || adapter.recoverySession != "bootstrap-session-1" ||
		adapter.recoveryPrompt != recoveryConcludePrompt ||
		adapter.recoveryInputPath != BlackboardPath(started.Run.ID, PhaseBootstrapConcludeRecovery) {
		t.Fatalf("conclude recovery evidence: calls=%d observations=%d captures=%d phase=%s session=%q prompt=%q path=%q",
			authorizer.calls, adapter.continuationObservations, adapter.captureCalls, adapter.recoveryPhase,
			adapter.recoverySession, adapter.recoveryPrompt, adapter.recoveryInputPath)
	}
	launches := runtime.Snapshot().Launches
	if len(launches) != 3 || launches[1].Phase != PhaseBootstrapConclude || launches[2].Phase != PhaseBootstrapConcludeRecovery ||
		launches[1].RenderedInput.Hash != launches[2].RenderedInput.Hash || launches[1].RenderedInput.Path == launches[2].RenderedInput.Path {
		t.Fatalf("conclude recovery launches: %+v", launches)
	}
}

func TestBootstrapExecutorDoesNotFinalizeInvalidConcludeRecovery(t *testing.T) {
	for _, test := range []struct {
		name               string
		captureError       bool
		invalidResult      bool
		wantCode           ExecutionErrorCode
		wantCaptureFailure CaptureFailureReason
	}{
		{name: "capture failure", captureError: true, wantCode: ExecutionCaptureFailed, wantCaptureFailure: CaptureFailureInvalidCandidate},
		{name: "invalid result", invalidResult: true, wantCode: ExecutionInvalidResult},
	} {
		t.Run(test.name, func(t *testing.T) {
			started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
			recoveryPrompt := "Continue the interrupted Bootstrap task."
			recoveryConcludePrompt := "Serialize the confirmed Bootstrap result without tools."
			started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 30, PhaseSeconds: 5, FallbackPhaseSeconds: 5, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
			started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
			started.Run.Spec.PromptTemplateHash = BootstrapPromptTemplateHashWithRecoveryModes(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
			runtime := newRuntimeTestDouble()
			runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
			runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 9})
			runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
			adapter := &testBootstrapAdapter{
				recoverySupported: true, primaryConclude: true,
				recoveryCaptureError: test.captureError, recoveryInvalidResult: test.invalidResult,
			}
			authorizer := &testProcessRecoveryAuthorizer{}
			executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil, authorizer).BindBootstrap(started, BootstrapExecution{
				Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
				RecoveryConcludePrompt: recoveryConcludePrompt,
				PromptTemplateHash:     started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
			})
			if err != nil {
				t.Fatal(err)
			}

			completion := executor.Execute(t.Context(), started)
			if completion.Kind != CompletionFailed || completion.Code != test.wantCode ||
				completion.CaptureFailureReason != test.wantCaptureFailure || authorizer.calls != 1 ||
				adapter.concludeAttempts != 1 || len(runtime.Snapshot().Launches) != 3 {
				t.Fatalf("invalid conclude recovery: completion=%+v calls=%d conclude=%d launches=%d",
					completion, authorizer.calls, adapter.concludeAttempts, len(runtime.Snapshot().Launches))
			}
		})
	}
}

func TestBootstrapExecutorDoesNotFinalizeUnverifiedConcludeBoundary(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	adapter := &testBootstrapAdapter{primaryMalformed: true, fallbackMalformed: true, concludeContinuationBlocked: true}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionCaptureFailed ||
		completion.CaptureFailureReason != CaptureFailureInvalidCandidate || adapter.concludeAttempts != 1 ||
		len(runtime.Snapshot().Launches) != 2 {
		t.Fatalf("unverified Bootstrap finalization: completion=%+v attempts=%d launches=%d",
			completion, adapter.concludeAttempts, len(runtime.Snapshot().Launches))
	}
}

func TestBootstrapExecutorDoesNotFallbackOnCombinedTechnicalFailure(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true, ErrorCode: ProcessErrorOutputTooLarge})
	adapter := &testBootstrapAdapter{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionOutputTooLarge || adapter.fallbackPrepared {
		t.Fatalf("combined technical failure: completion=%+v fallback=%t", completion, adapter.fallbackPrepared)
	}
	if snapshot := runtime.Snapshot(); len(snapshot.Launches) != 1 {
		t.Fatalf("technical failure launched fallback: %+v", snapshot)
	}
}

func TestBootstrapExecutorBindsFallbackPromptToSnapshot(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	_, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt + " changed", PromptTemplateHash: "changed",
		Input: input, Adapter: &testBootstrapAdapter{},
	})
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != ErrorExecutionContractMismatch {
		t.Fatalf("fallback prompt drift: %v", err)
	}
	if snapshot := runtime.Snapshot(); len(snapshot.Launches) != 0 {
		t.Fatalf("drifted fallback prompt reached runtime: %+v", snapshot)
	}
}

func TestBootstrapExecutorUsesDedicatedFallbackPhaseTimeout(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	started.Run.Spec.Timeouts = TimeoutSnapshot{
		RunSeconds: 15, PhaseSeconds: 6, FallbackPhaseSeconds: 3, GraceSeconds: 1,
	}
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	runtime.QueueOutput(ProcessOutput{Exited: true})
	adapter := &testBootstrapAdapter{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeConcludeIntent {
		t.Fatalf("fallback completion: %+v", completion)
	}
	if adapter.captureCalls != 1 {
		t.Fatalf("Result Capture calls=%d, want only the successful fallback phase", adapter.captureCalls)
	}
	launches := runtime.Snapshot().Launches
	if len(launches) != 2 || launches[0].PhaseTimeout != 6*time.Second || launches[1].PhaseTimeout != 3*time.Second {
		t.Fatalf("phase timeouts: %+v", launches)
	}
}

func TestBootstrapExecutorFinalizesMalformedConcludeResultInSameSession(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	adapter := &testBootstrapAdapter{primaryMalformed: true, fallbackMalformed: true}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeConcludeIntent {
		t.Fatalf("finalized completion: %+v", completion)
	}
	if adapter.concludeAttempts != 2 {
		t.Fatalf("conclude attempts=%d, want same-session fallback plus finalization", adapter.concludeAttempts)
	}
	if launches := runtime.Snapshot().Launches; len(launches) != 3 || launches[1].PhaseTimeout != 5*time.Second || launches[2].PhaseTimeout != 5*time.Second {
		t.Fatalf("finalization launches: %+v", launches)
	}
}

func TestBootstrapExecutorFinalizesProtocolInvalidConcludeResultInSameSession(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	adapter := &testBootstrapAdapter{fallbackInvalidResult: true}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeConcludeIntent {
		t.Fatalf("finalized completion: %+v", completion)
	}
	if adapter.concludeAttempts != 2 {
		t.Fatalf("conclude attempts=%d, want same-session fallback plus finalization", adapter.concludeAttempts)
	}
	if launches := runtime.Snapshot().Launches; len(launches) != 3 {
		t.Fatalf("finalization launches: %+v", launches)
	}
}

func TestBootstrapExecutorStopsAfterFinalizationFailure(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	adapter := &testBootstrapAdapter{primaryMalformed: true, fallbackMalformed: true, finalizationMalformed: true}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionCaptureFailed ||
		completion.CaptureFailureReason != CaptureFailureInvalidCandidate {
		t.Fatalf("finalization failure: %+v", completion)
	}
	if adapter.concludeAttempts != 2 || len(runtime.Snapshot().Launches) != 3 {
		t.Fatalf("finalization retried beyond one recovery phase: attempts=%d launches=%d", adapter.concludeAttempts, len(runtime.Snapshot().Launches))
	}
}

func TestBootstrapExecutorAcceptsDeclineFromConcludeFallback(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	runtime.QueueOutput(ProcessOutput{Exited: true})
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input,
		Adapter: &testBootstrapAdapter{fallbackDecline: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionDeclined || completion.OutcomeKind != OutcomeDecline {
		t.Fatalf("fallback decline: %+v", completion)
	}
}

func TestBootstrapExecutorSkipsFallbackAfterProjectCancellation(t *testing.T) {
	started, input, prompt, fallbackPrompt := bootstrapExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	ctx, cancel := context.WithCancel(t.Context())
	adapter := &testBootstrapAdapter{primaryConclude: true, cancel: cancel}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindBootstrap(started, BootstrapExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := executor.Execute(ctx, started)
	if completion.Kind != CompletionCancelled || adapter.fallbackPrepared || len(runtime.Snapshot().Launches) != 1 {
		t.Fatalf("cancelled fallback: completion=%+v prepared=%t launches=%d",
			completion, adapter.fallbackPrepared, len(runtime.Snapshot().Launches))
	}
}

type testBootstrapAdapter struct {
	fallbackPrepared            bool
	recoverySupported           bool
	recoveryPrepared            bool
	recoverySession             string
	fallbackDecline             bool
	primaryConclude             bool
	primaryMalformed            bool
	fallbackMalformed           bool
	fallbackInvalidResult       bool
	recoveryPhase               ExecutionPhase
	recoveryPrompt              string
	recoveryInputPath           string
	recoveryCaptureError        bool
	recoveryInvalidResult       bool
	finalizationMalformed       bool
	finalizationInvalidResult   bool
	primaryContinuationBlocked  bool
	concludeContinuationBlocked bool
	continuationObservations    int
	concludeAttempts            int
	captureCalls                int
	cancel                      context.CancelFunc
}

func (adapter *testBootstrapAdapter) TypeName() string { return "test" }

func (adapter *testBootstrapAdapter) PrepareBootstrap(BootstrapRequest) (AgentInvocation, error) {
	return &testBootstrapInvocation{owner: adapter, session: "bootstrap-session-1"}, nil
}

type testBootstrapInvocation struct {
	owner           *testBootstrapAdapter
	fallback        bool
	recovery        bool
	recoveryPhase   ExecutionPhase
	session         string
	concludeAttempt int
}

func (invocation *testBootstrapInvocation) Command() []string { return []string{"agent", "bootstrap"} }

func (invocation *testBootstrapInvocation) Environment() map[string]string { return nil }

func (invocation *testBootstrapInvocation) CanContinueSession() bool {
	if invocation.fallback {
		return !invocation.owner.concludeContinuationBlocked
	}
	return !invocation.owner.primaryContinuationBlocked
}

func (invocation *testBootstrapInvocation) ObserveContinuationSession(_ context.Context, _ ProcessOutput) bool {
	invocation.owner.continuationObservations++
	if invocation.fallback {
		return !invocation.owner.concludeContinuationBlocked
	}
	return !invocation.owner.primaryContinuationBlocked
}

func (invocation *testBootstrapInvocation) Capture(_ context.Context, _ ProcessOutput) (CapturedResult, error) {
	invocation.owner.captureCalls++
	if invocation.recovery {
		if invocation.owner.recoveryCaptureError {
			return CapturedResult{CaptureFailureReason: CaptureFailureInvalidCandidate}, errors.New("invalid recovery capture")
		}
		if invocation.owner.recoveryInvalidResult {
			return CapturedResult{Candidate: json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"must reject","fact":{"description":"must not commit"}}}`)}, nil
		}
		candidate := json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"confirmed by recovery"}}}`)
		return CapturedResult{Candidate: candidate}, nil
	}
	if !invocation.fallback && invocation.owner.primaryMalformed {
		return CapturedResult{CaptureFailureReason: CaptureFailureInvalidCandidate}, errors.New("malformed primary result")
	}
	if invocation.fallback && invocation.owner.finalizationMalformed && invocation.concludeAttempt == 2 {
		return CapturedResult{CaptureFailureReason: CaptureFailureInvalidCandidate}, errors.New("malformed finalization result")
	}
	if invocation.fallback && invocation.owner.fallbackMalformed && invocation.concludeAttempt == 1 {
		return CapturedResult{CaptureFailureReason: CaptureFailureInvalidCandidate}, errors.New("malformed fallback result")
	}
	if invocation.fallback && invocation.owner.fallbackInvalidResult && invocation.concludeAttempt == 1 {
		return CapturedResult{Candidate: protocolInvalidConcludeResult}, nil
	}
	if invocation.fallback && invocation.owner.finalizationInvalidResult && invocation.concludeAttempt == 2 {
		return CapturedResult{Candidate: protocolInvalidConcludeResult}, nil
	}
	candidate := json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"done","fact":{"description":"answer"}}}`)
	if invocation.owner.cancel != nil && !invocation.fallback {
		invocation.owner.cancel()
	}
	if invocation.owner.primaryConclude && !invocation.fallback {
		candidate = json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"partial"}}}`)
	}
	if invocation.fallback {
		if invocation.owner.fallbackDecline {
			candidate = json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"no confirmed fact"}}`)
		} else {
			candidate = json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"partial"}}}`)
		}
	}
	return CapturedResult{Candidate: candidate}, nil
}

func (invocation *testBootstrapInvocation) AuthorizeRunContinuation(context.Context, RunContinuationRequest) error {
	return nil
}

func (invocation *testBootstrapInvocation) PrepareProcessRecovery(request ProcessRecoveryRequest) (AgentInvocation, error) {
	if !invocation.owner.recoverySupported ||
		(request.Phase != PhaseBootstrapRecovery && request.Phase != PhaseBootstrapConcludeRecovery) {
		return nil, errors.New("recovery unavailable")
	}
	invocation.owner.recoveryPrepared = true
	invocation.owner.recoveryPhase = request.Phase
	invocation.owner.recoveryPrompt = request.Prompt
	invocation.owner.recoveryInputPath = request.InputPath
	invocation.owner.recoverySession = invocation.session
	return &testBootstrapInvocation{owner: invocation.owner, recovery: true, recoveryPhase: request.Phase, session: invocation.session}, nil
}

func (invocation *testBootstrapInvocation) PrepareBootstrapConclude(BootstrapConcludeRequest) (AgentInvocation, error) {
	invocation.owner.fallbackPrepared = true
	invocation.owner.concludeAttempts++
	return &testBootstrapInvocation{
		owner: invocation.owner, fallback: true, concludeAttempt: invocation.owner.concludeAttempts, session: invocation.session,
	}, nil
}

func bootstrapExecutorFixture(t *testing.T) (StartedRun, BootstrapInput, string, string) {
	t.Helper()
	input := BootstrapInput{
		Generation: 1, Revision: 2, TargetIntentID: "i001", Origin: "origin", Goal: "goal",
	}
	inputIdentity, err := input.InputIdentityV1()
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Try the Bootstrap fast path."
	fallbackPrompt := "Conclude the reserved Bootstrap intent."
	target := input.TargetIntentID
	started := StartedRun{
		Run: ExecutionRun{
			ID: "run-bootstrap", ProjectID: "project-test", TargetIntentID: &target,
			WorkKey: "intent:project-test:i001:bootstrap", ClaimScope: "intent:project-test:i001",
			Attempt: 1, Status: StatusActive, FencingToken: 7, DispatcherID: "dispatcher-a",
			DispatcherFencingToken: 1, Task: TaskBootstrap,
			Spec: ExecutionSpecSnapshot{
				Schema:         ExecutionSpecSchemaV2,
				ConfigRevision: "config-v1", PromptTemplateHash: BootstrapPromptTemplateHash(prompt, fallbackPrompt),
				Input:  InputSnapshot{Generation: 1, Revision: 2, Identity: inputIdentity, TargetIntentID: &target},
				Worker: WorkerSnapshot{Name: "bootstrapper", Adapter: "test"},
				Image:  ImageSnapshot{RequestedRef: "agent:latest", ImageID: "sha256:image", RepoDigest: "agent@sha256:digest"},
				Timeouts: TimeoutSnapshot{
					RunSeconds: 18, PhaseSeconds: 5, FallbackPhaseSeconds: 5, GraceSeconds: 1,
				},
				ResultSchema: ResultSchemaV1,
			},
			StartedAt: time.Now(),
		},
		Claim: Claim{
			WorkKey: "intent:project-test:i001:bootstrap", Scope: "intent:project-test:i001", RunID: "run-bootstrap",
			ProjectID: "project-test", TargetIntentID: &target, Task: TaskBootstrap,
			FencingToken: 7, DispatcherID: "dispatcher-a",
		},
	}
	return started, input, prompt, fallbackPrompt
}

var protocolInvalidConcludeResult = json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"confirmed"}},"description":"unexpected summary"}`)

var _ BootstrapConcludeInvocation = (*testBootstrapInvocation)(nil)
var _ ProcessRecoveryInvocation = (*testBootstrapInvocation)(nil)
