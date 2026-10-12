package worker

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// ExecutionTarget is the small Project fact needed to render an Explore or
// Bootstrap prompt. The Worker module does not depend on Scheduler types.
type ExecutionTarget struct {
	ID          string
	Description string
}

type ExecutionBindingRequest struct {
	Task            rundomain.TaskType
	WorkerName      string
	BlackboardInput *rundomain.BlackboardInput
	BootstrapInput  *rundomain.BootstrapInput
	MaxIntents      int
}

type ExecutionMaterialEvidence string

const (
	ExecutionMaterialWorker         ExecutionMaterialEvidence = "Worker"
	ExecutionMaterialWorkerAdapter  ExecutionMaterialEvidence = "Worker Adapter"
	ExecutionMaterialPromptTemplate ExecutionMaterialEvidence = "Prompt Template"
	ExecutionMaterialInput          ExecutionMaterialEvidence = "Input discriminant"
	ExecutionMaterialRenderedInput  ExecutionMaterialEvidence = "rendered Input"
	ExecutionMaterialTarget         ExecutionMaterialEvidence = "Target Intent"
	ExecutionMaterialTask           ExecutionMaterialEvidence = "Task"
	ExecutionMaterialBinding        ExecutionMaterialEvidence = "Execution Binding"
)

// ExecutionMaterialError identifies only the non-sensitive evidence field
// whose deterministic material could not be reconstructed.
type ExecutionMaterialError struct {
	Evidence ExecutionMaterialEvidence
}

func (err *ExecutionMaterialError) Error() string {
	return fmt.Sprintf("Execution material %s could not be reproduced", err.Evidence)
}

func executionMaterialError(evidence ExecutionMaterialEvidence) error {
	return &ExecutionMaterialError{Evidence: evidence}
}

// BindExecution renders immutable Worker material and hands it to Run through
// one task-typed binding. Worker retains prompt, CLI, session, and capture
// ownership without exposing its task adapter assembly.
func (catalog *Catalog) BindExecution(
	binding rundomain.ExecutionBinding,
	request ExecutionBindingRequest,
	started rundomain.StartedRun,
) (rundomain.Executor, error) {
	if catalog == nil {
		return nil, executionMaterialError(ExecutionMaterialWorker)
	}
	if binding == nil {
		return nil, executionMaterialError(ExecutionMaterialBinding)
	}
	workerSnapshot, err := catalog.WorkerSnapshotForTask(request.WorkerName, request.Task)
	if err != nil {
		return nil, executionMaterialError(ExecutionMaterialWorker)
	}
	adapter, err := catalog.AgentAdapter(request.WorkerName)
	if err != nil {
		return nil, executionMaterialError(ExecutionMaterialWorkerAdapter)
	}
	prompts, ok := catalog.Prompt(request.Task)
	if !ok {
		return nil, executionMaterialError(ExecutionMaterialPromptTemplate)
	}
	promptTemplateHash, err := catalog.PromptTemplateHash(request.Task)
	if err != nil {
		return nil, executionMaterialError(ExecutionMaterialPromptTemplate)
	}
	if promptTemplateHash == "" {
		return nil, executionMaterialError(ExecutionMaterialPromptTemplate)
	}
	if workerSnapshot.Name == "" {
		return nil, executionMaterialError(ExecutionMaterialWorker)
	}

	switch request.Task {
	case rundomain.TaskReason:
		if request.BlackboardInput == nil {
			return nil, executionMaterialError(ExecutionMaterialInput)
		}
		input := *request.BlackboardInput
		renderedInput, err := renderExecutionInput(input)
		if err != nil {
			return nil, executionMaterialError(ExecutionMaterialRenderedInput)
		}
		prompt := renderReasonPrompt(prompts.Primary, input, request.MaxIntents)
		finalization := renderReasonPrompt(prompts.Finalization, input, request.MaxIntents)
		reasonAdapter, err := newReasonExecutorAdapter(adapter)
		if err != nil {
			return nil, executionMaterialError(ExecutionMaterialWorkerAdapter)
		}
		return binding.BindReason(started, rundomain.ReasonExecution{
			PromptTemplateHash: promptTemplateHash, Prompt: prompt, FinalizationPrompt: finalization,
			Input: input, RenderedInput: renderedInput, Adapter: reasonAdapter,
		})
	case rundomain.TaskExplore:
		if request.BlackboardInput == nil {
			return nil, executionMaterialError(ExecutionMaterialInput)
		}
		input := *request.BlackboardInput
		renderedInput, err := renderExecutionInput(input)
		if err != nil {
			return nil, executionMaterialError(ExecutionMaterialRenderedInput)
		}
		target, err := executionTargetFromInput(input)
		if err != nil {
			return nil, executionMaterialError(ExecutionMaterialTarget)
		}
		prompt := renderExplorePrompt(prompts.Primary, target)
		fallback := renderExplorePrompt(prompts.Fallback, target)
		recovery := renderExplorePrompt(prompts.RecoveryPrimary, target)
		recoveryConclude := renderExplorePrompt(prompts.RecoveryConclude, target)
		finalization := renderExplorePrompt(prompts.Finalization, target)
		exploreAdapter, err := newExploreExecutorAdapter(adapter)
		if err != nil {
			return nil, executionMaterialError(ExecutionMaterialWorkerAdapter)
		}
		return binding.BindExplore(started, rundomain.ExploreExecution{
			PromptTemplateHash: promptTemplateHash,
			Prompt:             prompt, FallbackPrompt: fallback,
			RecoveryPrompt: recovery, RecoveryConcludePrompt: recoveryConclude,
			FinalizationPrompt: finalization, Input: input,
			RenderedInput: renderedInput, Adapter: exploreAdapter,
		})
	case rundomain.TaskBootstrap:
		if request.BootstrapInput == nil {
			return nil, executionMaterialError(ExecutionMaterialInput)
		}
		input := *request.BootstrapInput
		renderedInput, err := renderExecutionInput(input)
		if err != nil {
			return nil, executionMaterialError(ExecutionMaterialRenderedInput)
		}
		prompt := renderBootstrapPrompt(prompts.Primary, input)
		fallback := renderBootstrapPrompt(prompts.Fallback, input)
		bootstrapAdapter, err := newBootstrapExecutorAdapter(adapter)
		if err != nil {
			return nil, executionMaterialError(ExecutionMaterialWorkerAdapter)
		}
		recovery := renderBootstrapPrompt(prompts.RecoveryPrimary, input)
		recoveryConclude := renderBootstrapPrompt(prompts.RecoveryConclude, input)
		finalization := renderBootstrapPrompt(prompts.Finalization, input)
		return binding.BindBootstrap(started, rundomain.BootstrapExecution{
			PromptTemplateHash: promptTemplateHash,
			Prompt:             prompt, FallbackPrompt: fallback, RecoveryPrompt: recovery,
			RecoveryConcludePrompt: recoveryConclude, FinalizationPrompt: finalization,
			Input: input, RenderedInput: renderedInput, Adapter: bootstrapAdapter,
		})
	default:
		return nil, executionMaterialError(ExecutionMaterialTask)
	}
}

type yamlExecutionInput interface {
	MarshalYAML() ([]byte, rundomain.RenderedInputHash, error)
}

func renderExecutionInput(input yamlExecutionInput) (*rundomain.RenderedInput, error) {
	data, hash, err := input.MarshalYAML()
	if err != nil {
		return nil, err
	}
	return &rundomain.RenderedInput{Bytes: data, Hash: hash}, nil
}

func executionTargetFromInput(input rundomain.BlackboardInput) (ExecutionTarget, error) {
	if input.TargetIntentID == "" {
		return ExecutionTarget{}, fmt.Errorf("Explore admitted input has no target Intent")
	}
	for _, intent := range input.Intents {
		if intent.ID == input.TargetIntentID {
			return ExecutionTarget{ID: intent.ID, Description: intent.Description}, nil
		}
	}
	return ExecutionTarget{}, fmt.Errorf("Explore admitted target Intent is absent from the Blackboard input")
}

func renderReasonPrompt(template string, input rundomain.BlackboardInput, maxIntents int) string {
	values := map[string]string{"max_intents": fmt.Sprint(maxIntents)}
	if strings.Contains(template, "{hints}") {
		values["hints"] = promptHintsJSON(activePromptHints(input.Hints))
	}
	if strings.Contains(template, "{fact_ids}") || strings.Contains(template, "{open_intents}") {
		facts := make([]string, 0, len(input.Facts))
		intents := make([]map[string]any, 0, len(input.Intents))
		for _, fact := range input.Facts {
			if fact.Status == "active" && fact.ID != "goal" {
				facts = append(facts, fact.ID)
			}
		}
		for _, intent := range input.Intents {
			if intent.Status == "open" {
				intents = append(intents, map[string]any{
					"id": intent.ID, "from": intent.From, "description": intent.Description,
					"creator": intent.Creator, "status": intent.Status,
				})
			}
		}
		values["fact_ids"] = mustPromptJSON(facts)
		values["open_intents"] = mustPromptJSON(intents)
	}
	return replacePrompt(template, values)
}

func renderExplorePrompt(template string, target ExecutionTarget) string {
	return replacePrompt(template, map[string]string{
		"intent_id": target.ID, "intent_description": target.Description,
	})
}

func renderBootstrapPrompt(template string, input rundomain.BootstrapInput) string {
	return replacePrompt(template, map[string]string{
		"intent_id": input.TargetIntentID, "origin": input.Origin, "goal": input.Goal, "hints": promptHintsJSON(activePromptHints(input.Hints)),
	})
}

func activePromptHints(hints []rundomain.InputHint) []rundomain.InputHint {
	active := make([]rundomain.InputHint, 0, len(hints))
	for _, hint := range hints {
		if hint.Status == "active" {
			active = append(active, hint)
		}
	}
	return active
}

func promptHintsJSON(hints []rundomain.InputHint) string {
	if hints == nil {
		hints = make([]rundomain.InputHint, 0)
	}
	return mustPromptJSON(hints)
}

func mustPromptJSON(value any) string {
	if value == nil {
		return "[]"
	}
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal prompt JSON: %v", err))
	}
	return string(data)
}

func replacePrompt(template string, values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := template
	for _, key := range keys {
		result = strings.ReplaceAll(result, "{"+key+"}", values[key])
	}
	return result
}
