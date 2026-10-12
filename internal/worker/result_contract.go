package worker

import (
	"encoding/json"
	"fmt"
	"slices"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

const ResultOutputContractSchemaV2 = "aether.result-output/v2"

// OutcomeSubmissionSchemaV1 is the Adapter-owned submission package.
// The model supplies only the business outcome; the Adapter
// adds the RunResult envelope after strict decoding.
const OutcomeSubmissionSchemaV1 = "aether.outcome/v1"

type invocationPreparation uint8

const (
	preparationPrimary invocationPreparation = iota
	preparationConclude
	preparationRecovery
)

type resultPhaseRule struct {
	task        rundomain.TaskType
	phase       Phase
	preparation invocationPreparation
	outcomes    []rundomain.OutcomeKind
}

var (
	reasonOutcomes = []rundomain.OutcomeKind{
		rundomain.OutcomeCompleteProject, rundomain.OutcomeProposeIntents, rundomain.OutcomeNoChange, rundomain.OutcomeDecline,
	}
	concludeOutcomes  = []rundomain.OutcomeKind{rundomain.OutcomeConcludeIntent, rundomain.OutcomeDecline}
	bootstrapOutcomes = []rundomain.OutcomeKind{
		rundomain.OutcomeCompleteProject, rundomain.OutcomeConcludeIntent, rundomain.OutcomeDecline,
	}
	// This is the single Worker-owned Task/Phase matrix. Outcome generation,
	// capture, preparation eligibility and static identity use these rules.
	resultPhaseRules = []resultPhaseRule{
		{rundomain.TaskReason, PhaseReason, preparationPrimary, reasonOutcomes},
		{rundomain.TaskReason, PhaseReasonFinalization, preparationConclude, reasonOutcomes},
		{rundomain.TaskExplore, PhaseExplore, preparationPrimary, concludeOutcomes},
		{rundomain.TaskExplore, PhaseExploreConclude, preparationConclude, concludeOutcomes},
		{rundomain.TaskExplore, PhaseExploreRecovery, preparationRecovery, concludeOutcomes},
		{rundomain.TaskExplore, PhaseExploreConcludeRecovery, preparationRecovery, concludeOutcomes},
		{rundomain.TaskBootstrap, PhaseBootstrap, preparationPrimary, bootstrapOutcomes},
		{rundomain.TaskBootstrap, PhaseBootstrapConclude, preparationConclude, concludeOutcomes},
		{rundomain.TaskBootstrap, PhaseBootstrapRecovery, preparationRecovery, bootstrapOutcomes},
		{rundomain.TaskBootstrap, PhaseBootstrapConcludeRecovery, preparationRecovery, concludeOutcomes},
	}
)

func resultRuleFor(task rundomain.TaskType, phase Phase) (resultPhaseRule, bool) {
	for _, rule := range resultPhaseRules {
		if rule.task == task && rule.phase == phase {
			return rule, true
		}
	}
	return resultPhaseRule{}, false
}

func (contract ResultContract) allowedOutcomes() []rundomain.OutcomeKind {
	rule, _ := resultRuleFor(contract.Task, contract.Phase)
	outcomes := slices.Clone(rule.outcomes)
	if contract.MaxProposedIntents == 0 {
		outcomes = slices.DeleteFunc(outcomes, func(kind rundomain.OutcomeKind) bool {
			return kind == rundomain.OutcomeProposeIntents
		})
	}
	return outcomes
}

func outcomeSubmissionJSONSchema(contract ResultContract) map[string]any {
	branches := make([]map[string]any, 0, 4)
	for _, kind := range contract.allowedOutcomes() {
		switch kind {
		case rundomain.OutcomeDecline:
			branches = append(branches, outcomeDeclineSchema())
		case rundomain.OutcomeNoChange:
			branches = append(branches, outcomeNoChangeSchema())
		case rundomain.OutcomeProposeIntents:
			branches = append(branches, outcomeProposeIntentsSchema(contract.MaxProposedIntents))
		case rundomain.OutcomeConcludeIntent:
			branches = append(branches, outcomeConcludeSchema())
		case rundomain.OutcomeCompleteProject:
			if contract.Task == rundomain.TaskReason {
				branches = append(branches, outcomeCompleteReasonSchema())
			} else {
				branches = append(branches, outcomeCompleteBootstrapSchema())
			}
		}
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"outcome": map[string]any{"anyOf": branches},
		},
		"required": []string{"outcome"},
	}
}

func outcomeDeclineSchema() map[string]any {
	return outcomeObjectSchema(map[string]any{
		"kind":   outcomeKindSchema(rundomain.OutcomeDecline),
		"reason": outcomeStringSchema(),
	}, []string{"kind", "reason"})
}

func outcomeNoChangeSchema() map[string]any {
	return outcomeObjectSchema(map[string]any{
		"kind": outcomeKindSchema(rundomain.OutcomeNoChange),
	}, []string{"kind"})
}

func outcomeProposeIntentsSchema(maxIntents int) map[string]any {
	return outcomeObjectSchema(map[string]any{
		"kind": outcomeKindSchema(rundomain.OutcomeProposeIntents),
		"intents": map[string]any{
			"type": "array", "minItems": 1, "maxItems": maxIntents, "items": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"from":        sourceFactIDsSchema(),
					"description": outcomeStringSchema(),
				},
				"required": []string{"from", "description"},
			},
		},
	}, []string{"kind", "intents"})
}

func outcomeCompleteReasonSchema() map[string]any {
	return outcomeObjectSchema(map[string]any{
		"kind":        outcomeKindSchema(rundomain.OutcomeCompleteProject),
		"description": outcomeStringSchema(),
		"from":        sourceFactIDsSchema(),
	}, []string{"kind", "description", "from"})
}

func outcomeCompleteBootstrapSchema() map[string]any {
	return outcomeObjectSchema(map[string]any{
		"kind":        outcomeKindSchema(rundomain.OutcomeCompleteProject),
		"description": outcomeStringSchema(),
		"fact": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"description": outcomeStringSchema()},
			"required":   []string{"description"},
		},
	}, []string{"kind", "description", "fact"})
}

func outcomeConcludeSchema() map[string]any {
	return outcomeObjectSchema(map[string]any{
		"kind": outcomeKindSchema(rundomain.OutcomeConcludeIntent),
		"fact": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"description": outcomeStringSchema()},
			"required":   []string{"description"},
		},
	}, []string{"kind", "fact"})
}

func outcomeObjectSchema(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties, "required": required,
	}
}

func outcomeKindSchema(kind rundomain.OutcomeKind) map[string]any {
	return map[string]any{"type": "string", "enum": []string{string(kind)}}
}

func outcomeStringSchema() map[string]any {
	// Length and byte limits remain authoritative in RunResult validation.
	return map[string]any{"type": "string"}
}

func sourceFactIDsSchema() map[string]any {
	return map[string]any{
		"type": "array", "minItems": 1, "maxItems": 64,
		"items": map[string]any{"type": "string"},
	}
}

func structuredOutputSchema(contract ResultContract) ([]byte, error) {
	if err := contract.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(outcomeSubmissionJSONSchema(contract))
}

type ResultContract struct {
	Schema             string
	Task               rundomain.TaskType
	Phase              Phase
	MaxProposedIntents int
}

func resultContractForRequest(request AgentRequest) (ResultContract, error) {
	contract := ResultContract{
		Schema: ResultOutputContractSchemaV2, Task: request.Task, Phase: request.Phase, MaxProposedIntents: request.MaxProposedIntents,
	}
	if err := contract.validate(); err != nil {
		return ResultContract{}, err
	}
	return contract, nil
}

func (contract ResultContract) validate() error {
	if contract.Schema != ResultOutputContractSchemaV2 {
		return fmt.Errorf("Result Contract schema is invalid")
	}
	if _, ok := resultRuleFor(contract.Task, contract.Phase); !ok {
		return fmt.Errorf("Result Contract task and phase do not match")
	}
	switch contract.Task {
	case rundomain.TaskReason:
		if contract.MaxProposedIntents < 0 {
			return fmt.Errorf("Reason Result Contract has invalid MaxProposedIntents")
		}
	case rundomain.TaskExplore, rundomain.TaskBootstrap:
		if contract.MaxProposedIntents != 0 {
			return fmt.Errorf("%s Result Contract cannot admit proposed Intents", contract.Task)
		}
	default:
		return fmt.Errorf("Result Contract task is invalid")
	}
	return nil
}

// normalizeOutcome is the shared semantic check after each submission format
// has been strictly decoded. Run retains structure and task validation;
// Worker additionally enforces this Invocation's phase eligibility.
func (contract ResultContract) normalizeOutcome(raw json.RawMessage) (json.RawMessage, string, error) {
	if err := contract.validate(); err != nil {
		return nil, "", &CaptureError{Code: CaptureBoundaryError, Detail: err.Error()}
	}
	wrapped := make([]byte, 0, len(raw)+len(`{"schema":"aether.run-result/v1","outcome":}`))
	wrapped = append(wrapped, `{"schema":"aether.run-result/v1","outcome":`...)
	wrapped = append(wrapped, raw...)
	wrapped = append(wrapped, '}')
	result, canonical, hash, err := rundomain.DecodeRunResult(wrapped, contract.Task, contract.MaxProposedIntents)
	if err != nil {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: fmt.Sprintf("outcome failed RunResult validation: %v", err)}
	}
	if !slices.Contains(contract.allowedOutcomes(), result.Outcome.Kind) {
		return nil, "", &CaptureError{Code: CaptureInvalidCandidate, Detail: "outcome kind is not allowed for this task phase"}
	}
	return canonical, hash, nil
}
