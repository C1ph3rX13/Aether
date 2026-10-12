package run

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

const maximumReasonPromptBytes = 256 * 1024

type ReasonExecution struct {
	PromptTemplateHash string
	Prompt             string
	FinalizationPrompt string
	Input              BlackboardInput
	RenderedInput      *RenderedInput
	Adapter            ReasonAdapter
}

type ReasonRequest struct {
	InvocationID       string
	Prompt             string
	InputPath          string
	MaxProposedIntents int
}

type ReasonFinalizationRequest struct {
	InvocationID       string
	Prompt             string
	InputPath          string
	MaxProposedIntents int
}

type ReasonFinalizationInvocation interface {
	AgentInvocation
	PrepareReasonFinalization(ReasonFinalizationRequest) (AgentInvocation, error)
}

func canFinalizeReason(invocation AgentInvocation) bool {
	_, ok := invocation.(ReasonFinalizationInvocation)
	return ok && canContinueSession(invocation)
}

type ReasonAdapter interface {
	TypeName() string
	PrepareReason(ReasonRequest) (AgentInvocation, error)
}

type AgentInvocation interface {
	Command() []string
	Environment() map[string]string
	Capture(context.Context, ProcessOutput) (CapturedResult, error)
}

// protocolFileProvider is implemented by Invocations whose CLI needs a
// submission schema on disk before it starts.
type protocolFileProvider interface {
	ProtocolFile() *ProtocolFile
}

// outputCaptureProvider is optional so Adapters using the normal bounded
// collector do not need an Adapter-specific capture object.
type outputCaptureProvider interface {
	NewOutputCapture() ProcessOutputCapture
}

type continuationSessionObserver interface {
	ObserveContinuationSession(context.Context, ProcessOutput) bool
}

type sessionContinuation interface {
	CanContinueSession() bool
}

func canContinueSession(invocation AgentInvocation) bool {
	continuation, ok := invocation.(sessionContinuation)
	return ok && continuation.CanContinueSession()
}

func observeContinuationSession(ctx context.Context, invocation AgentInvocation, output ProcessOutput) bool {
	observer, ok := invocation.(continuationSessionObserver)
	return ok && observer.ObserveContinuationSession(ctx, output)
}

type CapturedResult struct {
	Candidate            json.RawMessage
	ResultHash           string
	CaptureFailureReason CaptureFailureReason
	CaptureDiagnostic    CaptureDiagnostic
	ResultEvidenceSource ResultEvidenceSource
}

func invalidResultCompletion(completion Completion) Completion {
	completion.Kind = CompletionFailed
	completion.Code = ExecutionInvalidResult
	completion.Candidate = nil
	completion.OutcomeKind = ""
	completion.ResultEvidenceSource = ""
	return completion
}

func captureFailureCompletion(completion Completion, captured CapturedResult) Completion {
	completion.Kind = CompletionFailed
	completion.Code = ExecutionCaptureFailed
	completion.CaptureFailureReason = captured.CaptureFailureReason
	completion.CaptureDiagnostic = captured.CaptureDiagnostic
	completion.ResultEvidenceSource = ""
	completion.Candidate = nil
	completion.OutcomeKind = ""
	return completion
}

func (executor *RunExecutor) executeReason(
	runContext context.Context,
	parentContext context.Context,
	started StartedRun,
	completion Completion,
	secrets map[string]string,
) Completion {
	execution := executor.reason
	inputPath := BlackboardPath(started.Run.ID, PhaseReason)
	process, err := executor.launchPhase(
		runContext, started, PhaseReason, execution.invocation,
		&RenderedInput{Path: inputPath, Bytes: execution.inputYAML, Hash: execution.inputHash},
		secrets, started.Run.Spec.Timeouts.PhaseSeconds,
	)
	if err != nil {
		return contextCompletion(runContext, parentContext, completion, ExecutionLaunchFailed)
	}
	output, err := process.Wait(runContext)
	if kind, code, terminal := classifyProcess(runContext, parentContext, output, err); terminal {
		completion.Kind = kind
		completion.Code = code
		return completion
	}
	captured, err := execution.invocation.Capture(runContext, output)
	if err != nil {
		if shouldFinalizeReasonCapture(captured) && canFinalizeReason(execution.invocation) {
			return executor.finalizeReasonResult(
				runContext, parentContext, started, completion, execution, execution.invocation, secrets,
			)
		}
		return captureFailureCompletion(completion, captured)
	}
	completion.CaptureDiagnostic = captured.CaptureDiagnostic
	completion.ResultEvidenceSource = captured.ResultEvidenceSource
	completed, _, valid := completionFromCandidate(
		completion, captured.Candidate, TaskReason, started.Run.Spec.MaxProposedIntents,
	)
	if !valid {
		if canFinalizeReason(execution.invocation) {
			return executor.finalizeReasonResult(
				runContext, parentContext, started, completion, execution, execution.invocation, secrets,
			)
		}
		return invalidResultCompletion(completion)
	}
	return completed
}

func (executor *RunExecutor) finalizeReasonResult(
	runContext context.Context,
	parentContext context.Context,
	started StartedRun,
	completion Completion,
	execution *boundReasonExecution,
	primary AgentInvocation,
	secrets map[string]string,
) Completion {
	if !canContinueSession(primary) {
		return invalidResultCompletion(completion)
	}
	if runContext.Err() != nil || parentContext.Err() != nil {
		return contextCompletion(runContext, parentContext, completion, ExecutionLeaseExpired)
	}
	if err := executor.authorizeFallback(runContext, started); err != nil {
		return fallbackAuthorizationCompletion(completion, err)
	}
	finalizer, ok := primary.(ReasonFinalizationInvocation)
	if !ok {
		return invalidResultCompletion(completion)
	}
	inputPath := BlackboardPath(started.Run.ID, PhaseReasonFinalization)
	invocation, err := finalizer.PrepareReasonFinalization(ReasonFinalizationRequest{
		InvocationID: uuid.NewString(),
		Prompt:       execution.finalizationPrompt, InputPath: inputPath,
		MaxProposedIntents: started.Run.Spec.MaxProposedIntents,
	})
	if err != nil || invocation == nil {
		return contextCompletion(runContext, parentContext, completion, ExecutionPrepareFailed)
	}
	process, err := executor.launchPhase(
		runContext, started, PhaseReasonFinalization, invocation,
		&RenderedInput{Path: inputPath, Bytes: execution.inputYAML, Hash: execution.inputHash}, secrets,
		resultFinalizationSeconds(started.Run.Spec.Timeouts.PhaseSeconds),
	)
	if err != nil {
		return contextCompletion(runContext, parentContext, completion, ExecutionLaunchFailed)
	}
	output, waitError := process.Wait(runContext)
	if kind, code, terminal := classifyProcess(runContext, parentContext, output, waitError); terminal {
		completion.Kind = kind
		completion.Code = code
		return completion
	}
	captured, err := invocation.Capture(runContext, output)
	if err != nil {
		return captureFailureCompletion(completion, captured)
	}
	completion.CaptureDiagnostic = captured.CaptureDiagnostic
	completion.ResultEvidenceSource = captured.ResultEvidenceSource
	completed, _, valid := completionFromCandidate(
		completion, captured.Candidate, TaskReason, started.Run.Spec.MaxProposedIntents,
	)
	if !valid {
		return invalidResultCompletion(completion)
	}
	return completed
}

func reasonExecutionEvidence(
	spec ExecutionSpecSnapshot,
	execution ReasonExecution,
) ([]byte, RenderedInputHash, string) {
	if execution.Adapter == nil || execution.Adapter.TypeName() != spec.Worker.Adapter {
		return nil, "", "Worker Adapter"
	}
	if strings.TrimSpace(execution.Prompt) == "" || len(execution.Prompt) > maximumReasonPromptBytes {
		return nil, "", "rendered Prompt"
	}
	if spec.ResultSchema != ResultSchemaV1 {
		return nil, "", "Result Schema"
	}
	if execution.PromptTemplateHash != spec.PromptTemplateHash {
		return nil, "", "Prompt Template identity"
	}
	if !execution.Input.MatchesInputSnapshot(spec.Input) {
		return nil, "", "Blackboard Input identity"
	}
	data, hash, valid := validatedRenderedExecutionInput(execution.Input, execution.RenderedInput)
	if !valid {
		return nil, "", "rendered Input bytes or hash"
	}
	return data, hash, ""
}

func contextCompletion(
	runContext context.Context,
	parentContext context.Context,
	completion Completion,
	fallback ExecutionErrorCode,
) Completion {
	switch {
	case parentContext.Err() != nil:
		completion.Kind = CompletionCancelled
	case runContext.Err() == context.DeadlineExceeded:
		completion.Kind = CompletionFailed
		completion.Code = ExecutionDeadline
	default:
		completion.Kind = CompletionFailed
		completion.Code = fallback
	}
	return completion
}
