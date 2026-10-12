package worker

import (
	"context"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type reasonExecutorAdapter struct {
	adapter AgentAdapter
}

func newReasonExecutorAdapter(adapter AgentAdapter) (*reasonExecutorAdapter, error) {
	if adapter == nil {
		return nil, &CaptureError{Code: CaptureBoundaryError, Detail: "Reason adapter is required"}
	}
	return &reasonExecutorAdapter{adapter: adapter}, nil
}

func (adapter *reasonExecutorAdapter) TypeName() string { return adapter.adapter.TypeName() }

func (adapter *reasonExecutorAdapter) PrepareReason(request rundomain.ReasonRequest) (rundomain.AgentInvocation, error) {
	invocation, err := adapter.adapter.Prepare(AgentRequest{
		InvocationID: request.InvocationID, Task: rundomain.TaskReason, Phase: PhaseReason, Prompt: request.Prompt,
		Input:              InputReference{Kind: InputBlackboardFile, Path: request.InputPath},
		MaxProposedIntents: request.MaxProposedIntents,
	})
	if err != nil {
		return nil, err
	}
	base := newExecutorInvocation(adapter.adapter, invocation)
	conclude, ok := adapter.adapter.(ConcludeAdapter)
	if !ok {
		return base, nil
	}
	return &reasonFinalizationInvocation{executorInvocation: base, conclude: conclude}, nil
}

type reasonFinalizationInvocation struct {
	*executorInvocation
	conclude ConcludeAdapter
}

func (invocation *reasonFinalizationInvocation) PrepareReasonFinalization(
	request rundomain.ReasonFinalizationRequest,
) (rundomain.AgentInvocation, error) {
	if !invocation.CanContinueSession() {
		return nil, &CaptureError{Code: CaptureBoundaryError, Detail: "Reason finalization requires an adapter session"}
	}
	prepared, err := invocation.conclude.PrepareConclude(ConcludeRequest{
		AgentRequest: AgentRequest{
			InvocationID: request.InvocationID, Task: rundomain.TaskReason, Phase: PhaseReasonFinalization, Prompt: request.Prompt,
			Input:              InputReference{Kind: InputBlackboardFile, Path: request.InputPath},
			MaxProposedIntents: request.MaxProposedIntents,
		},
		Session: invocation.currentSession(),
	})
	if err != nil {
		return nil, err
	}
	return invocation.shareSession(newExecutorInvocation(invocation.adapter, prepared)), nil
}

type exploreExecutorAdapter struct{ adapter AgentAdapter }

func newExploreExecutorAdapter(adapter AgentAdapter) (*exploreExecutorAdapter, error) {
	if adapter == nil {
		return nil, &CaptureError{Code: CaptureBoundaryError, Detail: "Explore adapter is required"}
	}
	return &exploreExecutorAdapter{adapter: adapter}, nil
}

func (adapter *exploreExecutorAdapter) TypeName() string { return adapter.adapter.TypeName() }

func (adapter *exploreExecutorAdapter) PrepareExplore(request rundomain.ExploreRequest) (rundomain.AgentInvocation, error) {
	invocation, err := adapter.adapter.Prepare(AgentRequest{
		InvocationID: request.InvocationID, Task: rundomain.TaskExplore, Phase: PhaseExplore, Prompt: request.Prompt,
		Input:              InputReference{Kind: InputBlackboardFile, Path: request.InputPath},
		MaxProposedIntents: request.MaxProposedIntents,
	})
	if err != nil {
		return nil, err
	}
	base := newExecutorInvocation(adapter.adapter, invocation)
	conclude, ok := adapter.adapter.(ConcludeAdapter)
	if !ok {
		return base, nil
	}
	return &exploreConcludeInvocation{executorInvocation: base, conclude: conclude}, nil
}

type exploreConcludeInvocation struct {
	*executorInvocation
	conclude ConcludeAdapter
}

func (invocation *exploreConcludeInvocation) PrepareExploreConclude(
	request rundomain.ExploreConcludeRequest,
) (rundomain.AgentInvocation, error) {
	if !invocation.CanContinueSession() {
		return nil, &CaptureError{Code: CaptureBoundaryError, Detail: "Explore fallback requires an adapter session"}
	}
	prepared, err := invocation.conclude.PrepareConclude(ConcludeRequest{
		AgentRequest: AgentRequest{
			InvocationID: request.InvocationID, Task: rundomain.TaskExplore, Phase: PhaseExploreConclude, Prompt: request.Prompt,
			Input:              InputReference{Kind: InputBlackboardFile, Path: request.InputPath},
			MaxProposedIntents: request.MaxProposedIntents,
		},
		Session: invocation.currentSession(),
	})
	if err != nil {
		return nil, err
	}
	return invocation.shareSession(newExecutorInvocation(invocation.adapter, prepared)), nil
}

type bootstrapExecutorAdapter struct{ adapter AgentAdapter }

func newBootstrapExecutorAdapter(adapter AgentAdapter) (*bootstrapExecutorAdapter, error) {
	if adapter == nil {
		return nil, &CaptureError{Code: CaptureBoundaryError, Detail: "Bootstrap adapter is required"}
	}
	return &bootstrapExecutorAdapter{adapter: adapter}, nil
}

func (adapter *bootstrapExecutorAdapter) TypeName() string { return adapter.adapter.TypeName() }

func (adapter *bootstrapExecutorAdapter) PrepareBootstrap(request rundomain.BootstrapRequest) (rundomain.AgentInvocation, error) {
	invocation, err := adapter.adapter.Prepare(AgentRequest{
		InvocationID: request.InvocationID, Task: rundomain.TaskBootstrap, Phase: PhaseBootstrap, Prompt: request.Prompt,
		Input:              InputReference{Kind: InputBootstrapInline, Inline: request.InlineInput},
		MaxProposedIntents: request.MaxProposedIntents,
	})
	if err != nil {
		return nil, err
	}
	base := newExecutorInvocation(adapter.adapter, invocation)
	conclude, ok := adapter.adapter.(ConcludeAdapter)
	if !ok {
		return base, nil
	}
	return &bootstrapConcludeInvocation{executorInvocation: base, conclude: conclude}, nil
}

// executorInvocation shares the latest verified Session with successors.
// Each Invocation retains its starting Session for evidence verification.
type executorInvocation struct {
	adapter    AgentAdapter
	invocation Invocation
	sessionRef *sharedSession
}

type sharedSession struct {
	value *Session
}

func newExecutorInvocation(adapter AgentAdapter, invocation Invocation) *executorInvocation {
	return &executorInvocation{
		adapter: adapter, invocation: invocation,
		sessionRef: &sharedSession{value: invocation.Session()},
	}
}

func (invocation *executorInvocation) currentSession() *Session {
	return invocation.sessionRef.value
}

func (invocation *executorInvocation) updateSession(session *Session) {
	invocation.sessionRef.value = session
}

func (invocation *executorInvocation) shareSession(prepared *executorInvocation) *executorInvocation {
	prepared.sessionRef = invocation.sessionRef
	return prepared
}

func (invocation *executorInvocation) Command() []string {
	return invocation.invocation.Argv()
}

func (invocation *executorInvocation) Environment() map[string]string {
	return invocation.invocation.Env()
}

func (invocation *executorInvocation) ProtocolFile() *rundomain.ProtocolFile {
	return invocation.invocation.ProtocolFile()
}

func (invocation *executorInvocation) CanContinueSession() bool {
	return invocation.adapter.CanContinueSession(invocation.currentSession())
}

func (invocation *executorInvocation) NewOutputCapture() rundomain.ProcessOutputCapture {
	provider, ok := invocation.adapter.(interface {
		NewOutputCapture(Invocation) streamingCapture
	})
	if !ok {
		return nil
	}
	invocation.invocation.capture = provider.NewOutputCapture(invocation.invocation)
	return invocation.invocation.capture
}

func (invocation *executorInvocation) PrepareProcessRecovery(
	request rundomain.ProcessRecoveryRequest,
) (rundomain.AgentInvocation, error) {
	recovery, ok := invocation.adapter.(ProcessRecoveryAdapter)
	if !ok {
		return nil, &CaptureError{Code: CaptureBoundaryError, Detail: "process recovery is unavailable for this Adapter"}
	}
	if !invocation.CanContinueSession() {
		return nil, &CaptureError{Code: CaptureBoundaryError, Detail: "process recovery requires an adapter session"}
	}
	phase := Phase(request.Phase)
	input := InputReference{Kind: InputBlackboardFile, Path: request.InputPath}
	if request.Task == rundomain.TaskBootstrap {
		input = InputReference{Kind: InputBootstrapInline, Inline: request.InlineInput}
	}
	prepared, err := recovery.PrepareRecovery(ProcessRecoveryRequest{
		AgentRequest: AgentRequest{
			InvocationID: request.InvocationID, Task: request.Task, Phase: phase, Prompt: request.Prompt, Input: input,
			MaxProposedIntents: request.MaxProposedIntents,
		},
		Session: invocation.currentSession(),
	})
	if err != nil {
		return nil, err
	}
	return invocation.shareSession(newExecutorInvocation(invocation.adapter, prepared)), nil
}

func (invocation *executorInvocation) ObserveContinuationSession(ctx context.Context, output rundomain.ProcessOutput) bool {
	session := invocation.adapter.ObserveSession(ctx, invocation.invocation, output)
	invocation.updateSession(session)
	return invocation.CanContinueSession()
}

func (invocation *executorInvocation) Capture(ctx context.Context, output rundomain.ProcessOutput) (rundomain.CapturedResult, error) {
	captured, err := invocation.adapter.Capture(ctx, invocation.invocation, output)
	invocation.updateSession(captured.Session)
	result := rundomain.CapturedResult{
		Candidate: captured.Candidate, ResultHash: captured.ResultHash,
		CaptureFailureReason: captured.CaptureFailureReason,
		CaptureDiagnostic:    captured.CaptureDiagnostic,
		ResultEvidenceSource: captured.ResultEvidenceSource,
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

type bootstrapConcludeInvocation struct {
	*executorInvocation
	conclude ConcludeAdapter
}

func (invocation *bootstrapConcludeInvocation) PrepareBootstrapConclude(
	request rundomain.BootstrapConcludeRequest,
) (rundomain.AgentInvocation, error) {
	if !invocation.CanContinueSession() {
		return nil, &CaptureError{Code: CaptureBoundaryError, Detail: "Bootstrap fallback requires an adapter session"}
	}
	prepared, err := invocation.conclude.PrepareConclude(ConcludeRequest{
		AgentRequest: AgentRequest{
			InvocationID: request.InvocationID, Task: rundomain.TaskBootstrap, Phase: PhaseBootstrapConclude, Prompt: request.Prompt,
			Input:              InputReference{Kind: InputBootstrapInline, Inline: request.InlineInput},
			MaxProposedIntents: request.MaxProposedIntents,
		},
		Session: invocation.currentSession(),
	})
	if err != nil {
		return nil, err
	}
	return invocation.shareSession(newExecutorInvocation(invocation.adapter, prepared)), nil
}

var _ rundomain.ReasonAdapter = (*reasonExecutorAdapter)(nil)
var _ rundomain.ReasonFinalizationInvocation = (*reasonFinalizationInvocation)(nil)
var _ rundomain.ExploreAdapter = (*exploreExecutorAdapter)(nil)
var _ rundomain.ExploreConcludeInvocation = (*exploreConcludeInvocation)(nil)
var _ rundomain.BootstrapAdapter = (*bootstrapExecutorAdapter)(nil)
var _ rundomain.AgentInvocation = (*executorInvocation)(nil)
var _ rundomain.BootstrapConcludeInvocation = (*bootstrapConcludeInvocation)(nil)
