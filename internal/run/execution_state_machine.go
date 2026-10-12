package run

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Executor turns one persisted execution right into a completion proposal.
// The scheduler owns Exec cleanup after FinishRun commits the proposal.
type Executor interface {
	Execute(context.Context, StartedRun) Completion
}

type CompletionKind string

const (
	CompletionResultReady CompletionKind = "result_ready"
	CompletionDeclined    CompletionKind = "declined"
	CompletionFailed      CompletionKind = "failed"
	CompletionCancelled   CompletionKind = "cancelled"
	CompletionInterrupted CompletionKind = "interrupted"
)

type ExecutionErrorCode = ErrorCode

const (
	ExecutionInvalidRun      ExecutionErrorCode = "invalid_run"
	ExecutionUnsupportedTask ExecutionErrorCode = "unsupported_task"
	ExecutionPrepareFailed   ExecutionErrorCode = "prepare_failed"
	ExecutionLaunchFailed    ExecutionErrorCode = "launch_failed"
	ExecutionProcessFailed   ExecutionErrorCode = "process_failed"
	ExecutionOutputTooLarge  ExecutionErrorCode = "output_too_large"
	ExecutionProcessStream   ExecutionErrorCode = "process_stream_error"
	ExecutionCaptureFailed   ExecutionErrorCode = "capture_failed"
	ExecutionInvalidResult   ExecutionErrorCode = "invalid_result"
	ExecutionDeadline        ExecutionErrorCode = "deadline"
	ExecutionFallbackMissing ExecutionErrorCode = "fallback_unavailable"
	ExecutionSecretFailed    ExecutionErrorCode = "secret_resolution_failed"
	ExecutionSecretExposed   ExecutionErrorCode = "secret_exposed"
	ExecutionLeaseExpired    ExecutionErrorCode = "lease_expired"
)

// Completion contains no raw Agent output, prompt, input, or resolved secret.
type Completion struct {
	RunID                RunID
	FencingToken         FencingToken
	Kind                 CompletionKind
	Code                 ExecutionErrorCode
	CaptureFailureReason CaptureFailureReason
	CaptureDiagnostic    CaptureDiagnostic
	ResultEvidenceSource ResultEvidenceSource
	OutcomeKind          OutcomeKind
	Candidate            json.RawMessage
}

func (completion Completion) FinishRequest() (FinishRequest, error) {
	if completion.RunID == "" || completion.FencingToken <= 0 {
		return FinishRequest{}, newError(ErrorInvalidArgument, "completion result identity is invalid")
	}
	request := FinishRequest{RunID: completion.RunID, FencingToken: completion.FencingToken}
	switch completion.Kind {
	case CompletionResultReady, CompletionDeclined:
		if len(completion.Candidate) == 0 {
			return FinishRequest{}, newError(ErrorInvalidArgument, "completion result candidate is missing")
		}
		request.Candidate = append(json.RawMessage(nil), completion.Candidate...)
		request.CaptureDiagnostic = completion.CaptureDiagnostic
		request.ResultEvidenceSource = completion.ResultEvidenceSource
	case CompletionFailed:
		if completion.Code == "" {
			return FinishRequest{}, newError(ErrorInvalidArgument, "failed completion code is required")
		}
		request.Termination = &RunTermination{
			Status: StatusFailed, Code: completion.Code,
			CaptureFailureReason: completion.CaptureFailureReason,
		}
		request.CaptureDiagnostic = completion.CaptureDiagnostic
	case CompletionCancelled:
		request.Termination = &RunTermination{Status: StatusCancelled, Code: completion.Code}
	case CompletionInterrupted:
		request.Termination = &RunTermination{Status: StatusInterrupted, Code: completion.Code}
	default:
		return FinishRequest{}, newError(ErrorInvalidArgument, "completion kind is invalid")
	}
	return request, nil
}

type RunExecutor struct {
	runtime               Runtime
	reason                *boundReasonExecution
	continuationExecution *boundContinuationExecution
	secrets               SecretEnvironmentProvider
	handles               ExecutionHandleStore
	continuation          RunContinuationAuthorizer
	recovery              ProcessRecoveryAuthorizer
	startedIdentity       boundStartedRunIdentity
}

func (executor *RunExecutor) authorizeFallback(ctx context.Context, started StartedRun) error {
	if executor.continuation == nil {
		return nil
	}
	return executor.continuation.AuthorizeRunContinuation(ctx, RunContinuationRequest{
		RunID: started.Run.ID, ProjectID: started.Run.ProjectID,
		FencingToken: started.Run.FencingToken, DispatcherID: started.Run.DispatcherID,
		DispatcherFencingToken: started.Run.DispatcherFencingToken,
	})
}

func fallbackAuthorizationCompletion(completion Completion, err error) Completion {
	var domainErr *DomainError
	if errors.As(err, &domainErr) && (domainErr.Code == ErrorProjectInactive ||
		domainErr.Code == ErrorProjectPaused || domainErr.Code == ErrorProjectCompleted ||
		domainErr.Code == ErrorNotFound || domainErr.Code == ErrorTargetUnavailable) {
		completion.Kind = CompletionCancelled
		completion.Code = ErrorProjectInactive
		return completion
	}
	completion.Kind = CompletionInterrupted
	completion.Code = ExecutionLeaseExpired
	return completion
}

func (executor *RunExecutor) Execute(ctx context.Context, started StartedRun) (completion Completion) {
	completion = Completion{RunID: started.Run.ID, FencingToken: started.Run.FencingToken}
	if err := validateStartedExecution(started); err != nil {
		completion.Kind = CompletionFailed
		completion.Code = ExecutionInvalidRun
		return completion
	}
	identity, err := startedRunBindingIdentity(started)
	if err != nil || identity != executor.startedIdentity {
		completion.Kind = CompletionFailed
		completion.Code = ExecutionInvalidRun
		return completion
	}
	if started.Run.Task == TaskReason && executor.reason == nil ||
		(started.Run.Task == TaskExplore || started.Run.Task == TaskBootstrap) && executor.continuationExecution == nil {
		completion.Kind = CompletionFailed
		completion.Code = ExecutionUnsupportedTask
		return completion
	}

	runDeadline := started.Run.StartedAt.Add(time.Duration(started.Run.Spec.Timeouts.RunSeconds) * time.Second)
	runContext, cancel := context.WithDeadline(ctx, runDeadline)
	defer cancel()
	secretEnvironment, containsSecret, cleanupSecrets, err := executor.resolveRunSecrets(
		runContext, started, started.Run.Spec.Worker.SecretReferences,
	)
	if err != nil {
		return contextCompletion(runContext, ctx, completion, ExecutionSecretFailed)
	}
	defer cleanupSecrets()

	switch started.Run.Task {
	case TaskReason:
		completion = executor.executeReason(runContext, ctx, started, completion, secretEnvironment)
	case TaskExplore, TaskBootstrap:
		completion = executor.executeContinuation(runContext, ctx, started, completion, secretEnvironment)
	default:
		completion.Kind = CompletionFailed
		completion.Code = ExecutionUnsupportedTask
	}
	if len(completion.Candidate) > 0 && containsSecret(completion.Candidate) {
		clear(completion.Candidate)
		completion.Candidate = nil
		completion.OutcomeKind = ""
		completion.ResultEvidenceSource = ""
		completion.Kind = CompletionFailed
		completion.Code = ExecutionSecretExposed
	}
	return completion
}

func validateStartedExecution(started StartedRun) error {
	run := started.Run
	claim := started.Claim
	if run.Status != StatusActive || run.ID == "" || run.FencingToken <= 0 ||
		run.StartedAt.IsZero() || run.DispatcherFencingToken <= 0 ||
		claim.RunID != run.ID || claim.FencingToken != run.FencingToken ||
		claim.ProjectID != run.ProjectID || claim.Task != run.Task ||
		claim.DispatcherID != run.DispatcherID || claim.Scope != run.ClaimScope ||
		claim.WorkKey != run.WorkKey {
		return newError(ErrorInvalidArgument, "started Run and Claim identity do not match")
	}
	if _, err := run.Spec.normalized(); err != nil {
		return err
	}
	return validateTaskExecutionConfiguration(
		run.Task, run.Spec.Input.TargetIntentID, run.Spec.Timeouts, run.Spec.MaxProposedIntents,
	)
}

func (executor *RunExecutor) launchPhase(
	ctx context.Context,
	started StartedRun,
	phase ExecutionPhase,
	invocation AgentInvocation,
	input *RenderedInput,
	secrets map[string]string,
	phaseSeconds int,
) (Process, error) {
	environment, err := mergeRunEnvironment(invocation.Environment(), secrets)
	if err != nil {
		return nil, err
	}
	spec := LaunchSpec{
		RunID: started.Run.ID, ProjectID: started.Run.ProjectID, Task: started.Run.Task,
		Phase:        phase,
		FencingToken: started.Run.FencingToken, ConfigRevision: started.Run.Spec.ConfigRevision,
		Image:   started.Run.Spec.Image.RequestedRef,
		Command: invocation.Command(), Environment: environment,
		RenderedInput: input,
		PhaseTimeout:  time.Duration(phaseSeconds) * time.Second,
		StopGrace:     time.Duration(started.Run.Spec.Timeouts.GraceSeconds) * time.Second,
	}
	if provider, ok := invocation.(outputCaptureProvider); ok {
		spec.OutputCapture = provider.NewOutputCapture()
	}
	if provider, ok := invocation.(protocolFileProvider); ok {
		spec.ProtocolFile = provider.ProtocolFile()
	}
	var process Process
	if executor.handles == nil {
		process, err = executor.runtime.Launch(ctx, spec)
	} else {
		process, err = executor.runtime.(TrackedRuntime).LaunchTracked(ctx, spec, executionHandleRegistration{
			store: executor.handles, started: started, phase: phase,
		})
	}
	if err != nil {
		return nil, err
	}
	return process, nil
}

type executionHandleRegistration struct {
	store   ExecutionHandleStore
	started StartedRun
	phase   ExecutionPhase
}

func (registration executionHandleRegistration) ContainerReady(ctx context.Context, handle ProjectContainerHandle) error {
	handle.RunID = registration.started.Run.ID
	handle.ProjectID = registration.started.Run.ProjectID
	handle.RecordedAt = time.Now().UTC()
	return registration.store.RecordProjectContainer(ctx, ProjectContainerRecordRequest{
		Handle: handle, RunFencingToken: registration.started.Run.FencingToken,
		DispatcherID:           registration.started.Run.DispatcherID,
		DispatcherFencingToken: registration.started.Run.DispatcherFencingToken,
	})
}

func (registration executionHandleRegistration) Started(ctx context.Context, handle ExecutionHandle) error {
	handle.RunID = registration.started.Run.ID
	handle.ProjectID = registration.started.Run.ProjectID
	handle.Phase = registration.phase
	handle.StartedAt = time.Now().UTC()
	return registration.store.RecordExecutionHandle(ctx, ExecutionHandleRecordRequest{
		Handle: handle, RunFencingToken: registration.started.Run.FencingToken,
		DispatcherID:           registration.started.Run.DispatcherID,
		DispatcherFencingToken: registration.started.Run.DispatcherFencingToken,
	})
}

func (registration executionHandleRegistration) Stopped(ctx context.Context, handle ExecutionHandle) error {
	handle.RunID = registration.started.Run.ID
	handle.ProjectID = registration.started.Run.ProjectID
	handle.Phase = registration.phase
	return registration.store.MarkExecutionHandleStopped(ctx, ExecutionHandleStopRequest{
		Handle: handle, DispatcherID: registration.started.Run.DispatcherID,
		DispatcherFencingToken: registration.started.Run.DispatcherFencingToken,
	})
}

func (executor *RunExecutor) resolveRunSecrets(
	ctx context.Context,
	started StartedRun,
	references []SecretReference,
) (map[string]string, func([]byte) bool, func(), error) {
	if len(references) == 0 {
		return nil, func([]byte) bool { return false }, func() {}, nil
	}
	if executor.secrets == nil {
		return nil, nil, nil, newError(ErrorInvalidArgument, "Run requires a Secret Provider")
	}
	var resolved *ResolvedSecrets
	var err error
	if scoped, ok := executor.secrets.(RunScopedSecretEnvironmentProvider); ok {
		resolved, err = scoped.ResolveRunSecrets(ctx, started, references)
	} else {
		resolved, err = executor.secrets.ResolveSecrets(ctx, references)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	environment := resolved.Environment()
	return environment, resolved.Contains, func() {
		for key := range environment {
			environment[key] = ""
			delete(environment, key)
		}
		resolved.Close()
	}, nil
}

func mergeRunEnvironment(base map[string]string, secrets map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(base)+len(secrets))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range secrets {
		if _, exists := result[key]; exists {
			return nil, newError(ErrorInvalidArgument, "Agent invocation overrides a Secret Reference")
		}
		result[key] = value
	}
	return result, nil
}

func classifyProcess(
	runContext context.Context,
	parentContext context.Context,
	output ProcessOutput,
	waitError error,
) (CompletionKind, ExecutionErrorCode, bool) {
	switch {
	case parentContext.Err() != nil:
		return CompletionCancelled, "", true
	case runContext.Err() == context.DeadlineExceeded:
		return CompletionFailed, ExecutionDeadline, true
	case output.ErrorCode == ProcessErrorOutputTooLarge:
		return CompletionFailed, ExecutionOutputTooLarge, true
	case output.ErrorCode == ProcessErrorStreamRead:
		return CompletionFailed, ExecutionProcessStream, true
	case waitError != nil || output.ErrorCode != "":
		return CompletionFailed, ExecutionProcessFailed, true
	case output.TimedOut:
		return CompletionFailed, ExecutionDeadline, true
	case output.Cancelled:
		return CompletionInterrupted, "", true
	case !output.Exited || output.ExitCode != 0:
		return CompletionFailed, ExecutionProcessFailed, true
	default:
		return "", "", false
	}
}

func completionFromCandidate(
	completion Completion,
	candidate json.RawMessage,
	task TaskType,
	maxProposedIntents int,
) (Completion, RunResult, bool) {
	result, canonical, _, err := DecodeRunResult(candidate, task, maxProposedIntents)
	if err != nil {
		return completion, RunResult{}, false
	}
	completion.Candidate = append(json.RawMessage(nil), canonical...)
	completion.OutcomeKind = result.Outcome.Kind
	if result.Outcome.Kind == OutcomeDecline {
		completion.Kind = CompletionDeclined
	} else {
		completion.Kind = CompletionResultReady
	}
	return completion, result, true
}

var _ Executor = (*RunExecutor)(nil)
