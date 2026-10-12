package run

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestExploreExecutorRecoversAbnormalPrimaryInSameSession(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	recoveryPrompt := "Continue the interrupted Explore task."
	recoveryConcludePrompt := "Serialize the confirmed Explore result."
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 17, PhaseSeconds: 5, FallbackPhaseSeconds: 3, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
	started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
	started.Run.Spec.PromptTemplateHash = ExplorePromptTemplateHashWithRecovery(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 9, Stdout: `{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"candidate before failure"}}`})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	adapter := &testExploreAdapter{recoverySupported: true}
	authorizer := &testProcessRecoveryAuthorizer{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil, authorizer).BindExplore(started, ExploreExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
		RecoveryConcludePrompt: recoveryConcludePrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeConcludeIntent {
		t.Fatalf("recovered completion=%+v", completion)
	}
	if authorizer.calls != 1 || adapter.continuationObservations != 1 || adapter.captureCalls != 1 ||
		!adapter.recoveryPrepared || adapter.recoverySession != "session-1" {
		t.Fatalf("recovery reservation/session: calls=%d observations=%d captures=%d prepared=%t session=%q",
			authorizer.calls, adapter.continuationObservations, adapter.captureCalls, adapter.recoveryPrepared, adapter.recoverySession)
	}
	launches := runtime.Snapshot().Launches
	if len(launches) != 2 || launches[0].Phase != PhaseExplore || launches[1].Phase != PhaseExploreRecovery ||
		launches[1].PhaseTimeout != 2*time.Second || launches[0].RenderedInput.Hash != launches[1].RenderedInput.Hash ||
		launches[0].RenderedInput.Path == launches[1].RenderedInput.Path {
		t.Fatalf("recovery launches: %+v", launches)
	}
}

func TestExploreExecutorDoesNotLaunchRecoveryAfterContinuationAuthorizationFails(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	recoveryPrompt := "Continue the interrupted Explore task."
	recoveryConcludePrompt := "Serialize the confirmed Explore result."
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 17, PhaseSeconds: 5, FallbackPhaseSeconds: 3, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
	started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
	started.Run.Spec.PromptTemplateHash = ExplorePromptTemplateHashWithRecovery(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 9})
	authorizer := &testProcessRecoveryAuthorizer{continuationErr: &DomainError{Code: ErrorFenced, Detail: "lease changed"}}
	adapter := &testExploreAdapter{recoverySupported: true}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, authorizer, authorizer).BindExplore(started, ExploreExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
		RecoveryConcludePrompt: recoveryConcludePrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionInterrupted || completion.Code != ExecutionLeaseExpired ||
		authorizer.calls != 0 || authorizer.continuationCalls != 1 || adapter.continuationObservations != 0 ||
		len(runtime.Snapshot().Launches) != 1 {
		t.Fatalf("recovery authorization: completion=%+v reservations=%d continuations=%d observations=%d launches=%d",
			completion, authorizer.calls, authorizer.continuationCalls, adapter.continuationObservations, len(runtime.Snapshot().Launches))
	}
}

func TestExploreExecutorRecoversAbnormalConcludeWithoutFinalization(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	recoveryPrompt := "Continue the interrupted Explore task."
	recoveryConcludePrompt := "Serialize the confirmed Explore result without tools."
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 20, PhaseSeconds: 5, FallbackPhaseSeconds: 3, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
	started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
	started.Run.Spec.PromptTemplateHash = ExplorePromptTemplateHashWithRecovery(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 9, Stdout: `{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"candidate before conclude failure"}}}`})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	adapter := &testExploreAdapter{recoverySupported: true}
	authorizer := &testProcessRecoveryAuthorizer{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil, authorizer).BindExplore(started, ExploreExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
		RecoveryConcludePrompt: recoveryConcludePrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeConcludeIntent {
		t.Fatalf("conclude recovery completion=%+v", completion)
	}
	if authorizer.calls != 1 || adapter.continuationObservations != 2 || adapter.captureCalls != 1 ||
		adapter.recoveryPhase != PhaseExploreConcludeRecovery || adapter.recoverySession != "session-1" ||
		adapter.recoveryPrompt != recoveryConcludePrompt ||
		adapter.recoveryInputPath != BlackboardPath(started.Run.ID, PhaseExploreConcludeRecovery) {
		t.Fatalf("conclude recovery evidence: calls=%d observations=%d captures=%d phase=%s session=%q prompt=%q path=%q",
			authorizer.calls, adapter.continuationObservations, adapter.captureCalls, adapter.recoveryPhase,
			adapter.recoverySession, adapter.recoveryPrompt, adapter.recoveryInputPath)
	}
	launches := runtime.Snapshot().Launches
	if len(launches) != 3 || launches[1].Phase != PhaseExploreConclude || launches[2].Phase != PhaseExploreConcludeRecovery ||
		launches[1].RenderedInput.Hash != launches[2].RenderedInput.Hash || launches[1].RenderedInput.Path == launches[2].RenderedInput.Path {
		t.Fatalf("conclude recovery launches: %+v", launches)
	}
}

func TestExploreExecutorDoesNotObserveConcludeSessionAfterRuntimeFailure(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	recoveryPrompt := "Continue the interrupted Explore task."
	recoveryConcludePrompt := "Serialize the confirmed Explore result."
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 20, PhaseSeconds: 5, FallbackPhaseSeconds: 3, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
	started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
	started.Run.Spec.PromptTemplateHash = ExplorePromptTemplateHashWithRecovery(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 9, ErrorCode: ProcessErrorStreamRead})
	adapter := &testExploreAdapter{recoverySupported: true}
	authorizer := &testProcessRecoveryAuthorizer{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, authorizer, authorizer).BindExplore(started, ExploreExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
		RecoveryConcludePrompt: recoveryConcludePrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionProcessStream || authorizer.calls != 0 ||
		authorizer.continuationCalls != 2 || adapter.continuationObservations != 1 || adapter.recoveryPrepared ||
		len(runtime.Snapshot().Launches) != 2 {
		t.Fatalf("Conclude runtime failure: completion=%+v reservations=%d continuations=%d observations=%d recovery=%t launches=%d",
			completion, authorizer.calls, authorizer.continuationCalls, adapter.continuationObservations,
			adapter.recoveryPrepared, len(runtime.Snapshot().Launches))
	}
}

func TestExploreExecutorDoesNotFinalizeInvalidConcludeRecovery(t *testing.T) {
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
			started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
			recoveryPrompt := "Continue the interrupted Explore task."
			recoveryConcludePrompt := "Serialize the confirmed Explore result without tools."
			started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 20, PhaseSeconds: 5, FallbackPhaseSeconds: 3, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
			started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
			started.Run.Spec.PromptTemplateHash = ExplorePromptTemplateHashWithRecovery(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
			runtime := newRuntimeTestDouble()
			runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
			runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 9})
			runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
			adapter := &testExploreAdapter{
				recoverySupported: true, recoveryCaptureError: test.captureError, recoveryInvalidResult: test.invalidResult,
			}
			authorizer := &testProcessRecoveryAuthorizer{}
			executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil, authorizer).BindExplore(started, ExploreExecution{
				Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
				RecoveryConcludePrompt: recoveryConcludePrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
				Input: input, Adapter: adapter,
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

func TestExploreExecutorPreservesCaptureFailureWhenSessionContinuityIsUnavailable(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	adapter := &testExploreAdapter{captureError: true, primaryContinuationBlocked: true}
	authorizer := &testContinuationAuthorizer{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, authorizer).BindExplore(started, ExploreExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionCaptureFailed ||
		completion.CaptureFailureReason != CaptureFailureInvalidCandidate || authorizer.calls != 0 ||
		adapter.fallbackPrepared || len(runtime.Snapshot().Launches) != 1 {
		t.Fatalf("unverified capture continuation: completion=%+v authorizations=%d prepared=%t launches=%d",
			completion, authorizer.calls, adapter.fallbackPrepared, len(runtime.Snapshot().Launches))
	}
}

func TestExploreExecutorDoesNotFinalizeUnverifiedConcludeBoundary(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	adapter := &testExploreAdapter{captureError: true, fallbackCaptureError: true, concludeContinuationBlocked: true}
	authorizer := &testContinuationAuthorizer{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, authorizer).BindExplore(started, ExploreExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionCaptureFailed ||
		completion.CaptureFailureReason != CaptureFailureInvalidCandidate || authorizer.calls != 1 ||
		adapter.concludeAttempts != 1 || len(runtime.Snapshot().Launches) != 2 {
		t.Fatalf("unverified conclude finalization: completion=%+v authorizations=%d attempts=%d launches=%d",
			completion, authorizer.calls, adapter.concludeAttempts, len(runtime.Snapshot().Launches))
	}
}

func TestExploreExecutorDoesNotReserveRecoveryWithoutVerifiedContinuity(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	recoveryPrompt := "Continue the interrupted Explore task."
	recoveryConcludePrompt := "Serialize the confirmed Explore result."
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 17, PhaseSeconds: 5, FallbackPhaseSeconds: 3, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
	started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
	started.Run.Spec.PromptTemplateHash = ExplorePromptTemplateHashWithRecovery(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 9})
	adapter := &testExploreAdapter{recoverySupported: true, primaryContinuationBlocked: true}
	authorizer := &testProcessRecoveryAuthorizer{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil, authorizer).BindExplore(started, ExploreExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
		RecoveryConcludePrompt: recoveryConcludePrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionProcessFailed ||
		authorizer.calls != 0 || adapter.recoveryPrepared || len(runtime.Snapshot().Launches) != 1 {
		t.Fatalf("unverified recovery: completion=%+v reservations=%d prepared=%t launches=%d",
			completion, authorizer.calls, adapter.recoveryPrepared, len(runtime.Snapshot().Launches))
	}
}

func TestExploreExecutorDoesNotObserveSessionForProhibitedRecoveryExitClasses(t *testing.T) {
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
		{name: "wait failure", output: ProcessOutput{Exited: true, ExitCode: 9}, waitErr: errors.New("inspect failed"), wantKind: CompletionFailed, wantCode: ExecutionProcessFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
			recoveryPrompt := "Continue the interrupted Explore task."
			recoveryConcludePrompt := "Serialize the confirmed Explore result."
			started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 17, PhaseSeconds: 5, FallbackPhaseSeconds: 3, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
			started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
			started.Run.Spec.PromptTemplateHash = ExplorePromptTemplateHashWithRecovery(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
			runtime := newRuntimeTestDouble()
			runtime.QueueResult(test.output, test.waitErr)
			adapter := &testExploreAdapter{recoverySupported: true}
			authorizer := &testProcessRecoveryAuthorizer{}
			executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil, authorizer).BindExplore(started, ExploreExecution{
				Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
				RecoveryConcludePrompt: recoveryConcludePrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
				Input: input, Adapter: adapter,
			})
			if err != nil {
				t.Fatal(err)
			}

			completion := executor.Execute(t.Context(), started)
			if completion.Kind != test.wantKind || completion.Code != test.wantCode ||
				adapter.continuationObservations != 0 || adapter.recoveryPrepared || authorizer.calls != 0 ||
				authorizer.continuationCalls != 0 || len(runtime.Snapshot().Launches) != 1 {
				t.Fatalf("prohibited recovery exit: completion=%+v observations=%d prepared=%t reservations=%d continuations=%d launches=%d",
					completion, adapter.continuationObservations, adapter.recoveryPrepared, authorizer.calls,
					authorizer.continuationCalls, len(runtime.Snapshot().Launches))
			}
		})
	}
}

func TestExploreExecutorDoesNotObserveSessionWhenRecoveryAuthorizationIsUnsafe(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		wantKind CompletionKind
		wantCode ExecutionErrorCode
	}{
		{name: "fenced", err: &DomainError{Code: ErrorFenced, Detail: "lease changed"}, wantKind: CompletionInterrupted, wantCode: ExecutionLeaseExpired},
		{name: "inactive project", err: &DomainError{Code: ErrorProjectInactive, Detail: "paused"}, wantKind: CompletionCancelled, wantCode: ErrorProjectInactive},
	} {
		t.Run(test.name, func(t *testing.T) {
			started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
			recoveryPrompt := "Continue the interrupted Explore task."
			recoveryConcludePrompt := "Serialize the confirmed Explore result."
			started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 17, PhaseSeconds: 5, FallbackPhaseSeconds: 3, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
			started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
			started.Run.Spec.PromptTemplateHash = ExplorePromptTemplateHashWithRecovery(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
			runtime := newRuntimeTestDouble()
			runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 9})
			adapter := &testExploreAdapter{recoverySupported: true}
			authorizer := &testProcessRecoveryAuthorizer{continuationErr: test.err}
			executor, err := newTestExecutionBinding(t, runtime, nil, nil, authorizer, authorizer).BindExplore(started, ExploreExecution{
				Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
				RecoveryConcludePrompt: recoveryConcludePrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
				Input: input, Adapter: adapter,
			})
			if err != nil {
				t.Fatal(err)
			}

			completion := executor.Execute(t.Context(), started)
			if completion.Kind != test.wantKind || completion.Code != test.wantCode || authorizer.calls != 0 ||
				authorizer.continuationCalls != 1 || adapter.continuationObservations != 0 || adapter.recoveryPrepared ||
				len(runtime.Snapshot().Launches) != 1 {
				t.Fatalf("unsafe recovery authorization: completion=%+v reservations=%d continuations=%d observations=%d prepared=%t launches=%d",
					completion, authorizer.calls, authorizer.continuationCalls, adapter.continuationObservations,
					adapter.recoveryPrepared, len(runtime.Snapshot().Launches))
			}
		})
	}
}

func TestExploreExecutorDoesNotReserveRecoveryForTimeoutContinuation(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	recoveryPrompt := "Continue the interrupted Explore task."
	recoveryConcludePrompt := "Serialize the confirmed Explore result."
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 17, PhaseSeconds: 5, FallbackPhaseSeconds: 3, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
	started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
	started.Run.Spec.PromptTemplateHash = ExplorePromptTemplateHashWithRecovery(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	runtime.QueueOutput(ProcessOutput{Exited: true})
	adapter := &testExploreAdapter{recoverySupported: true}
	authorizer := &testProcessRecoveryAuthorizer{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, authorizer, authorizer).BindExplore(started, ExploreExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
		RecoveryConcludePrompt: recoveryConcludePrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeConcludeIntent ||
		authorizer.calls != 0 || authorizer.continuationCalls != 2 || adapter.continuationObservations != 1 ||
		adapter.recoveryPrepared || len(runtime.Snapshot().Launches) != 2 {
		t.Fatalf("timeout continuation: completion=%+v reservations=%d continuations=%d observations=%d recovery=%t launches=%d",
			completion, authorizer.calls, authorizer.continuationCalls, adapter.continuationObservations,
			adapter.recoveryPrepared, len(runtime.Snapshot().Launches))
	}
}

func TestExploreExecutorReauthorizesAfterTimeoutSessionObservation(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	recoveryPrompt := "Continue the interrupted Explore task."
	recoveryConcludePrompt := "Serialize the confirmed Explore result."
	started.Run.Spec.Timeouts = TimeoutSnapshot{RunSeconds: 17, PhaseSeconds: 5, FallbackPhaseSeconds: 3, RecoveryPhaseSeconds: 2, GraceSeconds: 1}
	started.Run.Spec.Worker.Capabilities = []string{ProcessRecoveryCapability}
	started.Run.Spec.PromptTemplateHash = ExplorePromptTemplateHashWithRecovery(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	adapter := &testExploreAdapter{recoverySupported: true}
	authorizer := &testProcessRecoveryAuthorizer{
		continuationErr:   &DomainError{Code: ErrorProjectInactive, Detail: "paused during Session observation"},
		continuationErrAt: 2,
	}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, authorizer, authorizer).BindExplore(started, ExploreExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, RecoveryPrompt: recoveryPrompt,
		RecoveryConcludePrompt: recoveryConcludePrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionCancelled || completion.Code != ErrorProjectInactive || authorizer.calls != 0 ||
		authorizer.continuationCalls != 2 || adapter.continuationObservations != 1 || adapter.fallbackPrepared ||
		adapter.recoveryPrepared || len(runtime.Snapshot().Launches) != 1 {
		t.Fatalf("timeout reauthorization: completion=%+v reservations=%d continuations=%d observations=%d fallback=%t recovery=%t launches=%d",
			completion, authorizer.calls, authorizer.continuationCalls, adapter.continuationObservations,
			adapter.fallbackPrepared, adapter.recoveryPrepared, len(runtime.Snapshot().Launches))
	}
}

func TestExploreExecutorUsesConcludeFallbackAfterTimeout(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	runtime.QueueOutput(ProcessOutput{Exited: true})
	adapter := &testExploreAdapter{}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindExplore(started, ExploreExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeConcludeIntent || !adapter.fallbackPrepared {
		t.Fatalf("fallback completion=%+v prepared=%t", completion, adapter.fallbackPrepared)
	}
	if adapter.captureCalls != 1 {
		t.Fatalf("Result Capture calls=%d, want only the successful fallback phase", adapter.captureCalls)
	}
	launches := runtime.Snapshot().Launches
	if len(launches) != 2 || launches[0].PhaseTimeout != 5*time.Second || launches[1].PhaseTimeout != 3*time.Second {
		t.Fatalf("phase launches: %+v", launches)
	}
	if launches[0].RenderedInput == nil || launches[1].RenderedInput == nil ||
		launches[0].RenderedInput.Path != BlackboardPath(started.Run.ID, PhaseExplore) ||
		launches[1].RenderedInput.Path != BlackboardPath(started.Run.ID, PhaseExploreConclude) ||
		launches[0].RenderedInput.Path == launches[1].RenderedInput.Path {
		t.Fatalf("phase input paths: %+v", launches)
	}
}

func TestExploreExecutorFallsBackAfterCaptureFailureButNotTechnicalFailure(t *testing.T) {
	for _, test := range []struct {
		name         string
		primary      ProcessOutput
		captureError bool
		wantFallback bool
		wantCode     ExecutionErrorCode
	}{
		{name: "capture failure", primary: ProcessOutput{Exited: true}, captureError: true, wantFallback: true},
		{name: "nonzero exit", primary: ProcessOutput{Exited: true, ExitCode: 9}, wantCode: ExecutionProcessFailed},
		{name: "oversized output", primary: ProcessOutput{Exited: true, ErrorCode: ProcessErrorOutputTooLarge}, wantCode: ExecutionOutputTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
			runtime := newRuntimeTestDouble()
			runtime.QueueOutput(test.primary)
			if test.wantFallback {
				runtime.QueueOutput(ProcessOutput{Exited: true})
			}
			adapter := &testExploreAdapter{captureError: test.captureError}
			executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindExplore(started, ExploreExecution{
				Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
				Input: input, Adapter: adapter,
			})
			if err != nil {
				t.Fatal(err)
			}

			completion := executor.Execute(t.Context(), started)
			if adapter.fallbackPrepared != test.wantFallback {
				t.Fatalf("fallback prepared=%t, want %t; completion=%+v", adapter.fallbackPrepared, test.wantFallback, completion)
			}
			if test.wantFallback {
				if completion.Kind != CompletionResultReady || completion.OutcomeKind != OutcomeConcludeIntent {
					t.Fatalf("fallback completion: %+v", completion)
				}
			} else if completion.Kind != CompletionFailed || completion.Code != test.wantCode {
				t.Fatalf("technical completion: %+v", completion)
			}
		})
	}
}

func TestExploreExecutorFinalizesMalformedConcludeResultInSameSession(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	adapter := &testExploreAdapter{captureError: true, fallbackCaptureError: true}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindExplore(started, ExploreExecution{
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
	if launches := runtime.Snapshot().Launches; len(launches) != 3 || launches[1].PhaseTimeout != 3*time.Second || launches[2].PhaseTimeout != 3*time.Second {
		t.Fatalf("finalization launches: %+v", launches)
	}
}

func TestExploreExecutorFinalizesProtocolInvalidConcludeResultInSameSession(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 124, TimedOut: true})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	runtime.QueueOutput(ProcessOutput{Exited: true, ExitCode: 0})
	adapter := &testExploreAdapter{fallbackInvalidResult: true}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindExplore(started, ExploreExecution{
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

func TestExploreExecutorSkipsFallbackAfterProjectCancellation(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	ctx, cancel := context.WithCancel(t.Context())
	adapter := &testExploreAdapter{captureError: true, cancel: cancel}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, nil).BindExplore(started, ExploreExecution{
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

func TestExploreExecutorReauthorizesBeforeFallbackLaunch(t *testing.T) {
	started, input, prompt, fallbackPrompt := exploreExecutorFixture(t)
	runtime := newRuntimeTestDouble()
	adapter := &testExploreAdapter{captureError: true}
	authorizer := &testContinuationAuthorizer{err: &DomainError{Code: ErrorProjectInactive, Detail: "paused"}}
	executor, err := newTestExecutionBinding(t, runtime, nil, nil, authorizer).BindExplore(started, ExploreExecution{
		Prompt: prompt, FallbackPrompt: fallbackPrompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := executor.Execute(t.Context(), started)
	if authorizer.calls != 1 || completion.Kind != CompletionCancelled ||
		adapter.fallbackPrepared || len(runtime.Snapshot().Launches) != 1 {
		t.Fatalf("fallback authorization: calls=%d completion=%+v prepared=%t launches=%d",
			authorizer.calls, completion, adapter.fallbackPrepared, len(runtime.Snapshot().Launches))
	}
}

type testProcessRecoveryAuthorizer struct {
	calls             int
	continuationCalls int
	err               error
	continuationErr   error
	continuationErrAt int
}

func (authorizer *testProcessRecoveryAuthorizer) BeginProcessRecovery(_ context.Context, request BeginProcessRecoveryRequest) (ProcessRecoveryReservation, error) {
	authorizer.calls++
	if authorizer.err != nil {
		return ProcessRecoveryReservation{}, authorizer.err
	}
	return ProcessRecoveryReservation{
		RunID: request.RunID, FencingToken: request.FencingToken, Mode: request.Mode,
		Phase: processRecoveryPhase(request.Mode), ReservedAt: time.Now(), PhaseTimeout: 2 * time.Second,
	}, nil
}

func (authorizer *testProcessRecoveryAuthorizer) AuthorizeRunContinuation(_ context.Context, _ RunContinuationRequest) error {
	authorizer.continuationCalls++
	if authorizer.continuationErrAt > 0 && authorizer.continuationCalls != authorizer.continuationErrAt {
		return nil
	}
	return authorizer.continuationErr
}

type testContinuationAuthorizer struct {
	calls int
	err   error
}

func (authorizer *testContinuationAuthorizer) AuthorizeRunContinuation(context.Context, RunContinuationRequest) error {
	authorizer.calls++
	return authorizer.err
}

type testExploreAdapter struct {
	fallbackPrepared            bool
	recoverySupported           bool
	recoveryPrepared            bool
	recoverySession             string
	captureError                bool
	fallbackCaptureError        bool
	fallbackInvalidResult       bool
	recoveryCaptureError        bool
	recoveryInvalidResult       bool
	finalizationInvalidResult   bool
	primaryContinuationBlocked  bool
	concludeContinuationBlocked bool
	continuationObservations    int
	concludeAttempts            int
	captureCalls                int
	recoveryPhase               ExecutionPhase
	recoveryPrompt              string
	recoveryInputPath           string
	cancel                      context.CancelFunc
}

func (adapter *testExploreAdapter) TypeName() string { return "test" }

func (adapter *testExploreAdapter) PrepareExplore(ExploreRequest) (AgentInvocation, error) {
	return &testExploreInvocation{owner: adapter, session: "session-1"}, nil
}

type testExploreInvocation struct {
	owner           *testExploreAdapter
	fallback        bool
	recovery        bool
	recoveryPhase   ExecutionPhase
	session         string
	concludeAttempt int
}

func (invocation *testExploreInvocation) Command() []string { return []string{"agent", "explore"} }

func (invocation *testExploreInvocation) Environment() map[string]string { return nil }

func (invocation *testExploreInvocation) CanContinueSession() bool {
	if invocation.fallback {
		return !invocation.owner.concludeContinuationBlocked
	}
	return !invocation.owner.primaryContinuationBlocked
}

func (invocation *testExploreInvocation) ObserveContinuationSession(_ context.Context, _ ProcessOutput) bool {
	invocation.owner.continuationObservations++
	if invocation.fallback {
		return !invocation.owner.concludeContinuationBlocked
	}
	return !invocation.owner.primaryContinuationBlocked
}

func (invocation *testExploreInvocation) Capture(_ context.Context, _ ProcessOutput) (CapturedResult, error) {
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
	if invocation.owner.cancel != nil && !invocation.fallback {
		invocation.owner.cancel()
	}
	if invocation.owner.captureError && !invocation.fallback {
		return CapturedResult{CaptureFailureReason: CaptureFailureInvalidCandidate}, errors.New("invalid primary result")
	}
	if invocation.owner.fallbackCaptureError && invocation.fallback && invocation.concludeAttempt == 1 {
		return CapturedResult{CaptureFailureReason: CaptureFailureInvalidCandidate}, errors.New("invalid fallback result")
	}
	if invocation.owner.fallbackInvalidResult && invocation.fallback && invocation.concludeAttempt == 1 {
		return CapturedResult{Candidate: protocolInvalidConcludeResult}, nil
	}
	if invocation.owner.finalizationInvalidResult && invocation.fallback && invocation.concludeAttempt == 2 {
		return CapturedResult{Candidate: protocolInvalidConcludeResult}, nil
	}
	candidate := json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"confirmed"}}}`)
	return CapturedResult{Candidate: candidate}, nil
}

func (invocation *testExploreInvocation) PrepareProcessRecovery(request ProcessRecoveryRequest) (AgentInvocation, error) {
	if !invocation.owner.recoverySupported ||
		(request.Phase != PhaseExploreRecovery && request.Phase != PhaseExploreConcludeRecovery) {
		return nil, errors.New("recovery unavailable")
	}
	invocation.owner.recoveryPrepared = true
	invocation.owner.recoveryPhase = request.Phase
	invocation.owner.recoveryPrompt = request.Prompt
	invocation.owner.recoveryInputPath = request.InputPath
	invocation.owner.recoverySession = invocation.session
	return &testExploreInvocation{owner: invocation.owner, recovery: true, recoveryPhase: request.Phase, session: invocation.session}, nil
}

func (invocation *testExploreInvocation) PrepareExploreConclude(ExploreConcludeRequest) (AgentInvocation, error) {
	invocation.owner.fallbackPrepared = true
	invocation.owner.concludeAttempts++
	return &testExploreInvocation{
		owner: invocation.owner, fallback: true, concludeAttempt: invocation.owner.concludeAttempts, session: invocation.session,
	}, nil
}

func exploreExecutorFixture(t *testing.T) (StartedRun, BlackboardInput, string, string) {
	t.Helper()
	input := BlackboardInput{
		Generation: 1, Revision: 2, TargetIntentID: "i001",
		Project: InputProject{Title: "Project", Origin: "origin", Goal: "goal"},
		Facts:   []InputFact{{ID: "origin", Description: "origin", Status: "active"}},
		Intents: []InputIntent{{ID: "i001", From: []string{"origin"}, Description: "investigate", Creator: "reasoner", Status: "open"}},
	}
	inputIdentity, err := input.InputIdentityV1()
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Explore the target intent."
	fallbackPrompt := "Conclude confirmed findings."
	target := input.TargetIntentID
	started := StartedRun{
		Run: ExecutionRun{
			ID: "run-explore", ProjectID: "project-test", TargetIntentID: &target,
			WorkKey: "intent:project-test:i001:explore", ClaimScope: "intent:project-test:i001",
			Attempt: 1, Status: StatusActive, FencingToken: 7, DispatcherID: "dispatcher-a",
			DispatcherFencingToken: 1, Task: TaskExplore,
			Spec: ExecutionSpecSnapshot{
				Schema:         ExecutionSpecSchemaV2,
				ConfigRevision: "config-v1", PromptTemplateHash: ExplorePromptTemplateHash(prompt, fallbackPrompt),
				Input:  InputSnapshot{Generation: 1, Revision: 2, Identity: inputIdentity, TargetIntentID: &target},
				Worker: WorkerSnapshot{Name: "explorer", Adapter: "test"},
				Image:  ImageSnapshot{RequestedRef: "agent:latest", ImageID: "sha256:image", RepoDigest: "agent@sha256:digest"},
				Timeouts: TimeoutSnapshot{
					RunSeconds: 14, PhaseSeconds: 5, FallbackPhaseSeconds: 3, GraceSeconds: 1,
				},
				ResultSchema: ResultSchemaV1,
			},
			StartedAt: time.Now(),
		},
		Claim: Claim{
			WorkKey: "intent:project-test:i001:explore", Scope: "intent:project-test:i001", RunID: "run-explore",
			ProjectID: "project-test", TargetIntentID: &target, Task: TaskExplore,
			FencingToken: 7, DispatcherID: "dispatcher-a",
		},
	}
	return started, input, prompt, fallbackPrompt
}

var _ ExploreConcludeInvocation = (*testExploreInvocation)(nil)
var _ ProcessRecoveryInvocation = (*testExploreInvocation)(nil)
