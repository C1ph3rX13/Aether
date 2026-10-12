package run

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type ProcessRecoveryRequest struct {
	InvocationID       string
	Task               TaskType
	Phase              ExecutionPhase
	Prompt             string
	InputPath          string
	InlineInput        string
	MaxProposedIntents int
}

type ProcessRecoveryInvocation interface {
	AgentInvocation
	PrepareProcessRecovery(ProcessRecoveryRequest) (AgentInvocation, error)
}

type continuationPhases struct {
	primary              ExecutionPhase
	conclude             ExecutionPhase
	primaryRecovery      ExecutionPhase
	concludeRecovery     ExecutionPhase
	primaryRecoveryMode  ProcessRecoveryMode
	concludeRecoveryMode ProcessRecoveryMode
}

func taskContinuationPhases(task TaskType) (continuationPhases, bool) {
	switch task {
	case TaskExplore:
		return continuationPhases{
			primary: PhaseExplore, conclude: PhaseExploreConclude,
			primaryRecovery: PhaseExploreRecovery, concludeRecovery: PhaseExploreConcludeRecovery,
			primaryRecoveryMode: ProcessRecoveryExplorePrimary, concludeRecoveryMode: ProcessRecoveryExploreConclude,
		}, true
	case TaskBootstrap:
		return continuationPhases{
			primary: PhaseBootstrap, conclude: PhaseBootstrapConclude,
			primaryRecovery: PhaseBootstrapRecovery, concludeRecovery: PhaseBootstrapConcludeRecovery,
			primaryRecoveryMode: ProcessRecoveryBootstrapPrimary, concludeRecoveryMode: ProcessRecoveryBootstrapConclude,
		}, true
	default:
		return continuationPhases{}, false
	}
}

func continuationInput(started StartedRun, phase ExecutionPhase, execution *boundContinuationExecution) *RenderedInput {
	input := &RenderedInput{Bytes: execution.inputYAML, Hash: execution.inputHash}
	if started.Run.Task == TaskExplore || phase == PhaseBootstrapRecovery || phase == PhaseBootstrapConcludeRecovery {
		input.Path = BlackboardPath(started.Run.ID, phase)
	}
	return input
}

func (executor *RunExecutor) executeContinuation(
	runContext, parentContext context.Context,
	started StartedRun,
	completion Completion,
	secrets map[string]string,
) Completion {
	phases, ok := taskContinuationPhases(started.Run.Task)
	if !ok {
		completion.Kind, completion.Code = CompletionFailed, ExecutionUnsupportedTask
		return completion
	}
	execution := executor.continuationExecution
	primary := execution.invocation
	process, err := executor.launchPhase(
		runContext, started, phases.primary, primary,
		continuationInput(started, phases.primary, execution), secrets, started.Run.Spec.Timeouts.PhaseSeconds,
	)
	if err != nil {
		return contextCompletion(runContext, parentContext, completion, ExecutionLaunchFailed)
	}
	output, waitError := process.Wait(runContext)
	if continuationTimeout(runContext, parentContext, output, waitError) {
		if err := executor.authorizeFallback(runContext, started); err != nil {
			return fallbackAuthorizationCompletion(completion, err)
		}
		if !observeContinuationSession(runContext, primary, output) {
			completion.Kind, completion.Code = CompletionFailed, ExecutionDeadline
			return completion
		}
		return executor.executeContinuationConclude(runContext, parentContext, started, completion, phases, primary, secrets)
	}
	if kind, code, terminal := classifyProcess(runContext, parentContext, output, waitError); terminal {
		completion.Kind, completion.Code = kind, code
		if kind == CompletionFailed && code == ExecutionProcessFailed && eligibleContinuationRecovery(runContext, parentContext, output, waitError) {
			if recovered, attempted := executor.executeContinuationRecovery(
				runContext, parentContext, started, completion, phases, phases.primary, primary, secrets, output,
			); attempted {
				return recovered
			}
		}
		return completion
	}
	captured, err := primary.Capture(runContext, output)
	if err != nil {
		if !canContinueSession(primary) {
			return captureFailureCompletion(completion, captured)
		}
		return executor.executeContinuationConclude(runContext, parentContext, started, completion, phases, primary, secrets)
	}
	completion.CaptureDiagnostic = captured.CaptureDiagnostic
	completion.ResultEvidenceSource = captured.ResultEvidenceSource
	completed, result, valid := completionFromCandidate(completion, captured.Candidate, started.Run.Task, 0)
	// Bootstrap Primary reserves partial results for Conclude. Its Primary
	// Recovery accepts any valid Bootstrap result, including a partial result.
	if !valid || started.Run.Task == TaskBootstrap && result.Outcome.Kind == OutcomeConcludeIntent {
		if !canContinueSession(primary) {
			return invalidResultCompletion(completion)
		}
		return executor.executeContinuationConclude(runContext, parentContext, started, completion, phases, primary, secrets)
	}
	return completed
}

func prepareContinuationConclude(
	started StartedRun,
	primary AgentInvocation,
	prompt string,
	input *RenderedInput,
	maxProposedIntents int,
) (AgentInvocation, bool, error) {
	switch started.Run.Task {
	case TaskExplore:
		conclude, ok := primary.(ExploreConcludeInvocation)
		if !ok {
			return nil, false, nil
		}
		invocation, err := conclude.PrepareExploreConclude(ExploreConcludeRequest{
			InvocationID: uuid.NewString(),
			Prompt:       prompt, InputPath: input.Path, MaxProposedIntents: maxProposedIntents,
		})
		return invocation, true, err
	case TaskBootstrap:
		conclude, ok := primary.(BootstrapConcludeInvocation)
		if !ok {
			return nil, false, nil
		}
		invocation, err := conclude.PrepareBootstrapConclude(BootstrapConcludeRequest{
			InvocationID: uuid.NewString(),
			Prompt:       prompt, InlineInput: string(input.Bytes), MaxProposedIntents: maxProposedIntents,
		})
		return invocation, true, err
	default:
		return nil, false, nil
	}
}

func (executor *RunExecutor) executeContinuationConclude(
	runContext, parentContext context.Context,
	started StartedRun,
	completion Completion,
	phases continuationPhases,
	primary AgentInvocation,
	secrets map[string]string,
) Completion {
	if !canContinueSession(primary) {
		completion.Kind, completion.Code = CompletionFailed, ExecutionFallbackMissing
		return completion
	}
	if runContext.Err() != nil || parentContext.Err() != nil {
		return contextCompletion(runContext, parentContext, completion, ExecutionLeaseExpired)
	}
	if err := executor.authorizeFallback(runContext, started); err != nil {
		return fallbackAuthorizationCompletion(completion, err)
	}
	execution := executor.continuationExecution
	input := continuationInput(started, phases.conclude, execution)
	invocation, supported, err := prepareContinuationConclude(started, primary, execution.fallbackPrompt, input, 0)
	if !supported {
		completion.Kind, completion.Code = CompletionFailed, ExecutionFallbackMissing
		return completion
	}
	if err != nil || invocation == nil {
		return contextCompletion(runContext, parentContext, completion, ExecutionPrepareFailed)
	}
	process, err := executor.launchPhase(
		runContext, started, phases.conclude, invocation, input, secrets, started.Run.Spec.Timeouts.FallbackPhaseSeconds,
	)
	if err != nil {
		return contextCompletion(runContext, parentContext, completion, ExecutionLaunchFailed)
	}
	output, waitError := process.Wait(runContext)
	if kind, code, terminal := classifyProcess(runContext, parentContext, output, waitError); terminal {
		completion.Kind, completion.Code = kind, code
		if kind == CompletionFailed && code == ExecutionProcessFailed && eligibleContinuationRecovery(runContext, parentContext, output, waitError) {
			if recovered, attempted := executor.executeContinuationRecovery(
				runContext, parentContext, started, completion, phases, phases.conclude, invocation, secrets, output,
			); attempted {
				return recovered
			}
		}
		return completion
	}
	captured, err := invocation.Capture(runContext, output)
	if err != nil {
		if shouldFinalizeCapture(captured.CaptureFailureReason) && canContinueSession(invocation) {
			return executor.finalizeContinuationResult(runContext, parentContext, started, completion, phases, primary, secrets)
		}
		return captureFailureCompletion(completion, captured)
	}
	completion.CaptureDiagnostic = captured.CaptureDiagnostic
	completion.ResultEvidenceSource = captured.ResultEvidenceSource
	completed, result, valid := completionFromCandidate(completion, captured.Candidate, started.Run.Task, 0)
	if !valid || !completionPhaseOutcome(result.Outcome.Kind) {
		if !canContinueSession(invocation) {
			return invalidResultCompletion(completion)
		}
		return executor.finalizeContinuationResult(runContext, parentContext, started, completion, phases, primary, secrets)
	}
	return completed
}

func (executor *RunExecutor) finalizeContinuationResult(
	runContext, parentContext context.Context,
	started StartedRun,
	completion Completion,
	phases continuationPhases,
	primary AgentInvocation,
	secrets map[string]string,
) Completion {
	if !canContinueSession(primary) {
		completion.Kind, completion.Code = CompletionFailed, ExecutionFallbackMissing
		return completion
	}
	if runContext.Err() != nil || parentContext.Err() != nil {
		return contextCompletion(runContext, parentContext, completion, ExecutionLeaseExpired)
	}
	if err := executor.authorizeFallback(runContext, started); err != nil {
		return fallbackAuthorizationCompletion(completion, err)
	}
	execution := executor.continuationExecution
	input := continuationInput(started, phases.conclude, execution)
	invocation, _, err := prepareContinuationConclude(started, primary, execution.finalizationPrompt, input, started.Run.Spec.MaxProposedIntents)
	if err != nil || invocation == nil {
		return contextCompletion(runContext, parentContext, completion, ExecutionPrepareFailed)
	}
	process, err := executor.launchPhase(
		runContext, started, phases.conclude, invocation, input, secrets,
		resultFinalizationSeconds(started.Run.Spec.Timeouts.FallbackPhaseSeconds),
	)
	if err != nil {
		return contextCompletion(runContext, parentContext, completion, ExecutionLaunchFailed)
	}
	output, waitError := process.Wait(runContext)
	if kind, code, terminal := classifyProcess(runContext, parentContext, output, waitError); terminal {
		completion.Kind, completion.Code = kind, code
		return completion
	}
	captured, err := invocation.Capture(runContext, output)
	if err != nil {
		return captureFailureCompletion(completion, captured)
	}
	completion.CaptureDiagnostic = captured.CaptureDiagnostic
	completion.ResultEvidenceSource = captured.ResultEvidenceSource
	completed, result, valid := completionFromCandidate(completion, captured.Candidate, started.Run.Task, 0)
	if !valid || !completionPhaseOutcome(result.Outcome.Kind) {
		return invalidResultCompletion(completion)
	}
	return completed
}

func completionPhaseOutcome(kind OutcomeKind) bool {
	return kind == OutcomeConcludeIntent || kind == OutcomeDecline
}

func eligibleContinuationRecovery(runContext, parentContext context.Context, output ProcessOutput, waitError error) bool {
	return output.Exited && output.ExitCode != 0 && !output.TimedOut && !output.Cancelled &&
		output.ErrorCode == "" && waitError == nil && parentContext.Err() == nil && runContext.Err() == nil
}

func continuationTimeout(runContext, parentContext context.Context, output ProcessOutput, waitError error) bool {
	return output.Exited && output.TimedOut && !output.Cancelled && output.ErrorCode == "" && waitError == nil &&
		parentContext.Err() == nil && runContext.Err() == nil
}

func (executor *RunExecutor) executeContinuationRecovery(
	runContext, parentContext context.Context,
	started StartedRun,
	completion Completion,
	phases continuationPhases,
	failedPhase ExecutionPhase,
	failedInvocation AgentInvocation,
	secrets map[string]string,
	abnormalOutput ProcessOutput,
) (Completion, bool) {
	if executor.recovery == nil || started.Run.Spec.Timeouts.RecoveryPhaseSeconds <= 0 {
		return completion, false
	}
	if deadline, ok := runContext.Deadline(); !ok || time.Until(deadline) < time.Duration(
		started.Run.Spec.Timeouts.RecoveryPhaseSeconds+started.Run.Spec.Timeouts.GraceSeconds,
	)*time.Second {
		return completion, false
	}
	recoveryAdapter, ok := failedInvocation.(ProcessRecoveryInvocation)
	if !ok {
		return completion, false
	}
	if err := executor.authorizeFallback(runContext, started); err != nil {
		return fallbackAuthorizationCompletion(completion, err), true
	}
	// An abnormal process can only establish a continuation Session. Its
	// output never enters Capture, even if it contains an apparent result.
	if !observeContinuationSession(runContext, failedInvocation, abnormalOutput) {
		return completion, false
	}
	execution := executor.continuationExecution
	recoveryPhase, mode, prompt := phases.primaryRecovery, phases.primaryRecoveryMode, execution.recoveryPrompt
	if failedPhase == phases.conclude {
		recoveryPhase, mode, prompt = phases.concludeRecovery, phases.concludeRecoveryMode, execution.recoveryConcludePrompt
	}
	input := continuationInput(started, recoveryPhase, execution)
	request := ProcessRecoveryRequest{
		InvocationID: uuid.NewString(), Task: started.Run.Task, Phase: recoveryPhase, Prompt: prompt, InputPath: input.Path,
	}
	if started.Run.Task == TaskBootstrap {
		request.InlineInput = string(input.Bytes)
	}
	recoveryInvocation, err := recoveryAdapter.PrepareProcessRecovery(request)
	if err != nil || recoveryInvocation == nil {
		return completion, false
	}
	reservation, err := executor.recovery.BeginProcessRecovery(runContext, BeginProcessRecoveryRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, DispatcherFencingToken: started.Run.DispatcherFencingToken,
		FailedPhase: failedPhase, Mode: mode,
	})
	if err != nil {
		return completion, false
	}
	if err := reservation.Validate(); err != nil || reservation.RunID != started.Run.ID ||
		reservation.FencingToken != started.Run.FencingToken || reservation.Mode != mode || reservation.Phase != recoveryPhase {
		completion.Kind, completion.Code = CompletionFailed, ExecutionPrepareFailed
		return completion, true
	}
	process, err := executor.launchPhase(
		runContext, started, recoveryPhase, recoveryInvocation, input, secrets, started.Run.Spec.Timeouts.RecoveryPhaseSeconds,
	)
	if err != nil {
		return contextCompletion(runContext, parentContext, completion, ExecutionLaunchFailed), true
	}
	output, waitError := process.Wait(runContext)
	if kind, code, terminal := classifyProcess(runContext, parentContext, output, waitError); terminal {
		completion.Kind, completion.Code = kind, code
		return completion, true
	}
	captured, err := recoveryInvocation.Capture(runContext, output)
	if err != nil {
		if failedPhase == phases.primary && canContinueSession(recoveryInvocation) {
			return executor.executeContinuationConclude(runContext, parentContext, started, completion, phases, failedInvocation, secrets), true
		}
		return captureFailureCompletion(completion, captured), true
	}
	completion.CaptureDiagnostic = captured.CaptureDiagnostic
	completion.ResultEvidenceSource = captured.ResultEvidenceSource
	completed, result, valid := completionFromCandidate(completion, captured.Candidate, started.Run.Task, 0)
	if failedPhase == phases.conclude {
		if !valid || !completionPhaseOutcome(result.Outcome.Kind) {
			return invalidResultCompletion(completion), true
		}
	} else if !valid {
		if !canContinueSession(recoveryInvocation) {
			return invalidResultCompletion(completion), true
		}
		return executor.executeContinuationConclude(runContext, parentContext, started, completion, phases, failedInvocation, secrets), true
	}
	return completed, true
}
