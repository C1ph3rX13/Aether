package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"

	"github.com/google/uuid"
)

type executionYAMLInput interface {
	MarshalYAML() ([]byte, RenderedInputHash, error)
}

// ExecutionBinding connects immutable task material to the Run Executor while
// keeping Runtime and operational capabilities behind their own seams.
type ExecutionBinding interface {
	BindReason(StartedRun, ReasonExecution) (Executor, error)
	BindExplore(StartedRun, ExploreExecution) (Executor, error)
	BindBootstrap(StartedRun, BootstrapExecution) (Executor, error)
}

type executionBinding struct {
	runtime      Runtime
	secrets      SecretEnvironmentProvider
	handles      ExecutionHandleStore
	continuation RunContinuationAuthorizer
	recovery     ProcessRecoveryAuthorizer
}

func NewExecutionBinding(
	runtime Runtime,
	secrets SecretEnvironmentProvider,
	handles ExecutionHandleStore,
	continuation RunContinuationAuthorizer,
	recovery ...ProcessRecoveryAuthorizer,
) (ExecutionBinding, error) {
	if runtime == nil {
		return nil, newError(ErrorInvalidArgument, "execution binding runtime is required")
	}
	if handles != nil {
		if _, ok := runtime.(TrackedRuntime); !ok {
			return nil, newError(ErrorInvalidArgument, "execution handle persistence requires a tracked runtime")
		}
	}
	var recoveryAuthorizer ProcessRecoveryAuthorizer
	if len(recovery) > 0 {
		recoveryAuthorizer = recovery[0]
	}
	if recoveryAuthorizer != nil && continuation == nil {
		return nil, newError(ErrorInvalidArgument, "process recovery requires Run continuation authorization")
	}
	return &executionBinding{
		runtime: runtime, secrets: secrets, handles: handles, continuation: continuation,
		recovery: recoveryAuthorizer,
	}, nil
}

type boundReasonExecution struct {
	inputYAML          []byte
	inputHash          RenderedInputHash
	finalizationPrompt string
	invocation         AgentInvocation
}

type boundContinuationExecution struct {
	inputYAML              []byte
	inputHash              RenderedInputHash
	fallbackPrompt         string
	recoveryPrompt         string
	recoveryConcludePrompt string
	finalizationPrompt     string
	invocation             AgentInvocation
}

type boundStartedRunIdentity [sha256.Size]byte

func startedRunBindingIdentity(started StartedRun) (boundStartedRunIdentity, error) {
	encoded, err := json.Marshal(started)
	if err != nil {
		return boundStartedRunIdentity{}, err
	}
	return sha256.Sum256(encoded), nil
}

func (binding *executionBinding) executorFor(started StartedRun, task TaskType) (*RunExecutor, error) {
	if binding == nil || binding.runtime == nil {
		return nil, executionContractError("Execution Binding dependencies are unavailable")
	}
	if err := validateStartedExecution(started); err != nil {
		return nil, executionContractError("Started Run evidence is invalid during Execution Binding")
	}
	if started.Run.Task != task {
		return nil, executionContractError("admitted Task does not match the task-specific Execution Binding")
	}
	identity, err := startedRunBindingIdentity(started)
	if err != nil {
		return nil, executionContractError("Started Run identity could not be frozen during Execution Binding")
	}
	return &RunExecutor{
		runtime: binding.runtime, secrets: binding.secrets,
		handles: binding.handles, continuation: binding.continuation,
		recovery: binding.recovery, startedIdentity: identity,
	}, nil
}

func (binding *executionBinding) BindReason(
	started StartedRun,
	execution ReasonExecution,
) (Executor, error) {
	executor, err := binding.executorFor(started, TaskReason)
	if err != nil {
		return nil, err
	}
	inputYAML, inputHash, evidence := reasonExecutionEvidence(started.Run.Spec, execution)
	if evidence != "" {
		return nil, executionContractError("Reason " + evidence + " does not match admitted evidence")
	}
	invocation, err := execution.Adapter.PrepareReason(ReasonRequest{
		InvocationID: uuid.NewString(),
		Prompt:       execution.Prompt, InputPath: BlackboardPath(started.Run.ID, PhaseReason),
		MaxProposedIntents: started.Run.Spec.MaxProposedIntents,
	})
	if err != nil || invocation == nil {
		return nil, executionContractError("Reason Adapter invocation could not be reproduced")
	}
	executor.reason = &boundReasonExecution{
		inputYAML: inputYAML, inputHash: inputHash,
		finalizationPrompt: execution.FinalizationPrompt, invocation: invocation,
	}
	return executor, nil
}

func (binding *executionBinding) BindExplore(
	started StartedRun,
	execution ExploreExecution,
) (Executor, error) {
	executor, err := binding.executorFor(started, TaskExplore)
	if err != nil {
		return nil, err
	}
	inputYAML, inputHash, evidence := exploreExecutionEvidence(started.Run.Spec, execution)
	if evidence != "" {
		return nil, executionContractError("Explore " + evidence + " does not match admitted evidence")
	}
	invocation, err := execution.Adapter.PrepareExplore(ExploreRequest{
		InvocationID: uuid.NewString(),
		Prompt:       execution.Prompt, InputPath: BlackboardPath(started.Run.ID, PhaseExplore),
		MaxProposedIntents: started.Run.Spec.MaxProposedIntents,
	})
	if err != nil || invocation == nil {
		return nil, executionContractError("Explore Adapter invocation could not be reproduced")
	}
	executor.continuationExecution = &boundContinuationExecution{
		inputYAML: inputYAML, inputHash: inputHash,
		fallbackPrompt: execution.FallbackPrompt,
		recoveryPrompt: execution.RecoveryPrompt, recoveryConcludePrompt: execution.RecoveryConcludePrompt,
		finalizationPrompt: execution.FinalizationPrompt, invocation: invocation,
	}
	return executor, nil
}

func (binding *executionBinding) BindBootstrap(
	started StartedRun,
	execution BootstrapExecution,
) (Executor, error) {
	executor, err := binding.executorFor(started, TaskBootstrap)
	if err != nil {
		return nil, err
	}
	inputYAML, inputHash, evidence := bootstrapExecutionEvidence(started.Run.Spec, execution)
	if evidence != "" {
		return nil, executionContractError("Bootstrap " + evidence + " does not match admitted evidence")
	}
	invocation, err := execution.Adapter.PrepareBootstrap(BootstrapRequest{
		InvocationID: uuid.NewString(),
		Prompt:       execution.Prompt, InlineInput: string(inputYAML),
		MaxProposedIntents: started.Run.Spec.MaxProposedIntents,
	})
	if err != nil || invocation == nil {
		return nil, executionContractError("Bootstrap Adapter invocation could not be reproduced")
	}
	executor.continuationExecution = &boundContinuationExecution{
		inputYAML: inputYAML, inputHash: inputHash,
		fallbackPrompt: execution.FallbackPrompt, recoveryPrompt: execution.RecoveryPrompt,
		recoveryConcludePrompt: execution.RecoveryConcludePrompt,
		finalizationPrompt:     execution.FinalizationPrompt, invocation: invocation,
	}
	return executor, nil
}

func validatedRenderedExecutionInput(
	input executionYAMLInput,
	rendered *RenderedInput,
) ([]byte, RenderedInputHash, bool) {
	expectedBytes, expectedHash, err := input.MarshalYAML()
	if err != nil {
		return nil, "", false
	}
	if rendered == nil {
		return expectedBytes, expectedHash, true
	}
	if rendered.Path != "" || rendered.Hash != expectedHash ||
		!rendered.Hash.Matches(rendered.Bytes) || !bytes.Equal(rendered.Bytes, expectedBytes) {
		return nil, "", false
	}
	return append([]byte(nil), rendered.Bytes...), rendered.Hash, true
}
