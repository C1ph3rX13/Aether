package run

import "strings"

type BootstrapExecution struct {
	PromptTemplateHash     string
	Prompt                 string
	FallbackPrompt         string
	RecoveryPrompt         string
	RecoveryConcludePrompt string
	FinalizationPrompt     string
	Input                  BootstrapInput
	RenderedInput          *RenderedInput
	Adapter                BootstrapAdapter
}

type BootstrapRequest struct {
	InvocationID       string
	Prompt             string
	InlineInput        string
	MaxProposedIntents int
}

type BootstrapConcludeRequest struct {
	InvocationID       string
	Prompt             string
	InlineInput        string
	MaxProposedIntents int
}

type BootstrapAdapter interface {
	TypeName() string
	PrepareBootstrap(BootstrapRequest) (AgentInvocation, error)
}

// BootstrapConcludeInvocation is implemented only when the prepared Adapter
// can continue the same opaque Agent session for fallback.
type BootstrapConcludeInvocation interface {
	AgentInvocation
	PrepareBootstrapConclude(BootstrapConcludeRequest) (AgentInvocation, error)
}

func bootstrapExecutionEvidence(
	spec ExecutionSpecSnapshot,
	execution BootstrapExecution,
) ([]byte, RenderedInputHash, string) {
	if execution.Adapter == nil || execution.Adapter.TypeName() != spec.Worker.Adapter {
		return nil, "", "Worker Adapter"
	}
	if strings.TrimSpace(execution.Prompt) == "" || len(execution.Prompt) > maximumReasonPromptBytes ||
		strings.TrimSpace(execution.FallbackPrompt) == "" || len(execution.FallbackPrompt) > maximumReasonPromptBytes {
		return nil, "", "rendered Prompt"
	}
	if spec.Timeouts.RecoveryPhaseSeconds > 0 &&
		(strings.TrimSpace(execution.RecoveryPrompt) == "" || len(execution.RecoveryPrompt) > maximumReasonPromptBytes ||
			strings.TrimSpace(execution.RecoveryConcludePrompt) == "" || len(execution.RecoveryConcludePrompt) > maximumReasonPromptBytes) {
		return nil, "", "recovery Prompt"
	}
	if spec.ResultSchema != ResultSchemaV1 {
		return nil, "", "Result Schema"
	}
	if execution.PromptTemplateHash != spec.PromptTemplateHash {
		return nil, "", "Prompt Template identity"
	}
	if !execution.Input.MatchesInputSnapshot(spec.Input) {
		return nil, "", "Bootstrap Input identity"
	}
	data, hash, valid := validatedRenderedExecutionInput(execution.Input, execution.RenderedInput)
	if !valid {
		return nil, "", "rendered Input bytes or hash"
	}
	return data, hash, ""
}
