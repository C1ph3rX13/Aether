package run

import "strings"

type ExploreExecution struct {
	PromptTemplateHash     string
	Prompt                 string
	FallbackPrompt         string
	RecoveryPrompt         string
	RecoveryConcludePrompt string
	FinalizationPrompt     string
	Input                  BlackboardInput
	RenderedInput          *RenderedInput
	Adapter                ExploreAdapter
}

type ExploreRequest struct {
	InvocationID       string
	Prompt             string
	InputPath          string
	MaxProposedIntents int
}

type ExploreConcludeRequest struct {
	InvocationID       string
	Prompt             string
	InputPath          string
	MaxProposedIntents int
}

type ExploreAdapter interface {
	TypeName() string
	PrepareExplore(ExploreRequest) (AgentInvocation, error)
}

type ExploreConcludeInvocation interface {
	AgentInvocation
	PrepareExploreConclude(ExploreConcludeRequest) (AgentInvocation, error)
}

func exploreExecutionEvidence(
	spec ExecutionSpecSnapshot,
	execution ExploreExecution,
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
		return nil, "", "Blackboard Input identity"
	}
	data, hash, valid := validatedRenderedExecutionInput(execution.Input, execution.RenderedInput)
	if !valid {
		return nil, "", "rendered Input bytes or hash"
	}
	return data, hash, ""
}
