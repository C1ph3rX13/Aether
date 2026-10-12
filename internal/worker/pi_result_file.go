package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/pelletier/go-toml/v2"
)

const piResultFilename = "result.toml"

const piResultOutputContract = `Pi Result File Contract: aether.pi-result-file/v1
Submit exactly one business outcome in the current Invocation's result.toml file. Use one [outcome] table and only the fields shown for the selected kind below. Only strings, arrays of strings, and the declared nested outcome tables are allowed; TOML dates, booleans, numbers, and unknown fields are invalid. The backend adds the RunResult envelope after strict validation.

Choose one of this Invocation's allowed kinds. Copy only its example and replace the placeholder descriptions and source IDs with confirmed evidence and eligible Fact IDs. Do not combine examples or add fields. For conclude_intent, use the singular [outcome.fact] table with its description; a facts array is invalid. Put findings, limitations, Goal progress, and artifact references inside the description. Do not add intent_id, status, summary, facts, next_steps, artifacts, schema, or Run/Invocation metadata. Parsing as TOML is not enough: the selected kind must have exactly its required fields.

Replace the complete contents of the exact file path supplied below; an earlier Invocation's file is not this submission. The business result must be in this file; chat text, stdout, Session entries, JSON, and other files are not submission channels. Write TOML file contents only, without Markdown fences or surrounding commentary. Use TOML basic or multiline strings and escape quotes and backslashes as TOML requires. The examples below are instructions, not evidence.

After the write tool confirms success, finish with a short final assistant message such as "Result submitted." and end this Invocation. Do not run optional checks, create another report, repeat the result in chat, or continue working. A successful file write alone is not a finished response; a final assistant response is still required. The backend validates the file after the process exits; do not claim that it has accepted or committed the result.`

// piResultTemplate is intentionally incomplete. The empty kind cannot pass
// RunResult validation. Static identity uses the phase's full capability;
// an Invocation uses the same renderer with its actual admitted outcomes.
func piResultTemplate(task rundomain.TaskType, outcomes []rundomain.OutcomeKind) []byte {
	var builder strings.Builder
	builder.WriteString("# Fill this file with the one outcome for this Invocation.\n")
	builder.WriteString("# Do not add fields outside the selected outcome kind.\n")
	builder.WriteString("[outcome]\n")
	builder.WriteString("kind = \"\"\n")
	builder.WriteString("# Allowed kinds: ")
	for index, kind := range outcomes {
		if index > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString(string(kind))
	}
	builder.WriteString(".\n")
	for _, kind := range outcomes {
		builder.WriteString("# Example for ")
		builder.WriteString(string(kind))
		builder.WriteString(" (replace placeholders):\n")
		for _, line := range strings.Split(strings.TrimSpace(piOutcomeExample(task, kind)), "\n") {
			builder.WriteString("# ")
			builder.WriteString(line)
			builder.WriteByte('\n')
		}
	}
	return []byte(builder.String())
}

// These examples are shared by the file template and the effective prompt,
// so both enter the existing submission identity with the same field shapes.
func piOutcomeExample(task rundomain.TaskType, kind rundomain.OutcomeKind) string {
	switch kind {
	case rundomain.OutcomeCompleteProject:
		if task == rundomain.TaskReason {
			return `[outcome]
kind = "complete_project"
description = "Explain why the cited Facts prove the Goal and address current verification requests."
from = ["eligible-fact-id"]
`
		}
		return `[outcome]
kind = "complete_project"
description = "Explain why the confirmed result proves the Goal and addresses current verification requests."
[outcome.fact]
description = "State the confirmed result and its supporting evidence."
`
	case rundomain.OutcomeProposeIntents:
		return `[outcome]
kind = "propose_intents"
[[outcome.intents]]
from = ["eligible-fact-id"]
description = "Describe one focused investigation and the evidence it should establish."
`
	case rundomain.OutcomeNoChange:
		return `[outcome]
kind = "no_change"
`
	case rundomain.OutcomeConcludeIntent:
		return `[outcome]
kind = "conclude_intent"
[outcome.fact]
description = "State the confirmed finding, supporting observation, limitations, and any existing artifact path."
`
	case rundomain.OutcomeDecline:
		return `[outcome]
kind = "decline"
reason = "Explain the concrete reason no supported result can be submitted."
`
	default:
		return ""
	}
}

func piResultProtocolFile(invocationID string, contract ResultContract) *rundomain.ProtocolFile {
	return &rundomain.ProtocolFile{
		InvocationID: invocationID,
		Filename:     piResultFilename,
		Writable:     true,
		Bytes:        piResultTemplate(contract.Task, contract.allowedOutcomes()),
	}
}

func piTomlSubmissionContract(file rundomain.ProtocolFile, contract ResultContract) string {
	var builder strings.Builder
	builder.WriteString(piResultOutputContract)
	fmt.Fprintf(&builder, "\n\nCurrent result file: %s\nAllowed kinds for this Invocation:", file.Path())
	for _, kind := range contract.allowedOutcomes() {
		fmt.Fprintf(&builder, "\n\n%s:\n```toml\n%s```", kind, piOutcomeExample(contract.Task, kind))
	}
	return builder.String()
}

func normalizePiTOML(raw []byte, contract ResultContract) (json.RawMessage, string, CaptureErrorCode, error) {
	if len(raw) > rundomain.MaximumResultBytes {
		return nil, "", CaptureCandidateTooLarge, &CaptureError{Code: CaptureCandidateTooLarge, Detail: "Pi result file exceeds its limit"}
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, "", CaptureNoCandidate, &CaptureError{Code: CaptureNoCandidate, Detail: "Pi result file is empty"}
	}
	if !utf8.Valid(trimmed) {
		return nil, "", CaptureInvalidCandidate, &CaptureError{Code: CaptureInvalidCandidate, Detail: "Pi result file is not valid UTF-8"}
	}
	var document map[string]any
	decoder := toml.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(&document); err != nil {
		return nil, "", CaptureInvalidCandidate, &CaptureError{Code: CaptureInvalidCandidate, Detail: "Pi result file is invalid TOML"}
	}
	if len(document) == 0 {
		return nil, "", CaptureNoCandidate, &CaptureError{Code: CaptureNoCandidate, Detail: "Pi result file has no outcome"}
	}
	if len(document) != 1 {
		return nil, "", CaptureInvalidCandidate, &CaptureError{Code: CaptureInvalidCandidate, Detail: "Pi result file has unknown top-level fields"}
	}
	value, ok := document["outcome"]
	if !ok {
		return nil, "", CaptureNoCandidate, &CaptureError{Code: CaptureNoCandidate, Detail: "Pi result file has no outcome"}
	}
	outcome, ok := value.(map[string]any)
	if !ok || len(outcome) == 0 {
		return nil, "", CaptureInvalidCandidate, &CaptureError{Code: CaptureInvalidCandidate, Detail: "Pi outcome table is invalid"}
	}
	for key := range outcome {
		if !piOutcomeFieldAllowed(key) {
			return nil, "", CaptureInvalidCandidate, &CaptureError{Code: CaptureInvalidCandidate, Detail: "Pi outcome has an unknown field"}
		}
	}
	if err := validatePiTOMLOutcomeTypes(outcome); err != nil {
		return nil, "", CaptureInvalidCandidate, &CaptureError{Code: CaptureInvalidCandidate, Detail: "Pi outcome fields have invalid types"}
	}
	encodedOutcome, err := json.Marshal(outcome)
	if err != nil {
		return nil, "", CaptureInvalidCandidate, &CaptureError{Code: CaptureInvalidCandidate, Detail: "Pi outcome fields have invalid types"}
	}
	canonical, hash, err := contract.normalizeOutcome(encodedOutcome)
	if err != nil {
		code := CaptureInvalidCandidate
		var captureErr *CaptureError
		if asCaptureError(err, &captureErr) {
			code = captureErr.Code
		}
		return nil, "", code, err
	}
	return canonical, hash, "", nil
}

func validatePiTOMLOutcomeTypes(outcome map[string]any) error {
	if _, ok := outcome["kind"].(string); !ok {
		return fmt.Errorf("kind must be a string")
	}
	for _, field := range []string{"description", "reason"} {
		if value, ok := outcome[field]; ok {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s must be a string", field)
			}
		}
	}
	if value, ok := outcome["from"]; ok {
		if err := validatePiTOMLStringArray(value); err != nil {
			return err
		}
	}
	if value, ok := outcome["fact"]; ok {
		fact, ok := value.(map[string]any)
		if !ok || len(fact) != 1 {
			return fmt.Errorf("fact must contain only description")
		}
		if description, ok := fact["description"].(string); !ok || description == "" {
			return fmt.Errorf("fact description must be a string")
		}
	}
	if value, ok := outcome["intents"]; ok {
		intents, ok := value.([]any)
		if !ok || len(intents) == 0 {
			return fmt.Errorf("intents must be an array")
		}
		for _, item := range intents {
			intent, ok := item.(map[string]any)
			if !ok || len(intent) != 2 {
				return fmt.Errorf("intent fields are invalid")
			}
			if description, ok := intent["description"].(string); !ok || description == "" {
				return fmt.Errorf("intent description must be a string")
			}
			if err := validatePiTOMLStringArray(intent["from"]); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePiTOMLStringArray(value any) error {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return fmt.Errorf("field must be an array of strings")
	}
	for _, item := range items {
		if _, ok := item.(string); !ok {
			return fmt.Errorf("field must be an array of strings")
		}
	}
	return nil
}

func piOutcomeFieldAllowed(field string) bool {
	switch field {
	case "kind", "description", "from", "fact", "intents", "reason":
		return true
	}
	return false
}
