package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	ResultSchemaV1     = "aether.run-result/v1"
	MaximumResultBytes = 64 * 1024
)

type OutcomeKind string

const (
	OutcomeCompleteProject OutcomeKind = "complete_project"
	OutcomeProposeIntents  OutcomeKind = "propose_intents"
	OutcomeNoChange        OutcomeKind = "no_change"
	OutcomeConcludeIntent  OutcomeKind = "conclude_intent"
	OutcomeDecline         OutcomeKind = "decline"
)

type FactResult struct {
	Description string `json:"description"`
}

type ProposedIntent struct {
	From        []string `json:"from"`
	Description string   `json:"description"`
}

type Outcome struct {
	Kind        OutcomeKind      `json:"kind"`
	Description string           `json:"description,omitempty"`
	From        []string         `json:"from,omitempty"`
	Fact        *FactResult      `json:"fact,omitempty"`
	Intents     []ProposedIntent `json:"intents,omitempty"`
	Reason      string           `json:"reason,omitempty"`
}

type RunResult struct {
	Schema  string  `json:"schema"`
	Outcome Outcome `json:"outcome"`
}

func DecodeRunResult(candidate []byte, task TaskType, maxProposedIntents int) (RunResult, []byte, string, error) {
	if len(candidate) == 0 || len(candidate) > MaximumResultBytes {
		return RunResult{}, nil, "", newError(ErrorInvalidResult, "run result is empty or exceeds its size limit")
	}
	var envelope struct {
		Schema  string          `json:"schema"`
		Outcome json.RawMessage `json:"outcome"`
	}
	if err := decodeStrict(candidate, &envelope); err != nil {
		return RunResult{}, nil, "", invalidResult("invalid run result envelope", err)
	}
	if envelope.Schema != ResultSchemaV1 || len(envelope.Outcome) == 0 || string(envelope.Outcome) == "null" {
		return RunResult{}, nil, "", newError(ErrorInvalidResult, "unsupported schema or missing outcome")
	}
	var discriminator struct {
		Kind OutcomeKind `json:"kind"`
	}
	if err := json.Unmarshal(envelope.Outcome, &discriminator); err != nil || discriminator.Kind == "" {
		return RunResult{}, nil, "", newError(ErrorInvalidResult, "outcome kind is required")
	}

	outcome, err := decodeOutcome(envelope.Outcome, task, discriminator.Kind, maxProposedIntents)
	if err != nil {
		return RunResult{}, nil, "", err
	}
	result := RunResult{Schema: ResultSchemaV1, Outcome: outcome}
	canonical, err := json.Marshal(result)
	if err != nil {
		return RunResult{}, nil, "", fmt.Errorf("marshal normalized run result: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return result, canonical, hex.EncodeToString(digest[:]), nil
}

func decodeOutcome(raw []byte, task TaskType, kind OutcomeKind, maxProposedIntents int) (Outcome, error) {
	switch kind {
	case OutcomeCompleteProject:
		var value struct {
			Kind        OutcomeKind `json:"kind"`
			Description string      `json:"description"`
			From        []string    `json:"from,omitempty"`
			Fact        *FactResult `json:"fact,omitempty"`
		}
		if err := decodeStrict(raw, &value); err != nil {
			return Outcome{}, invalidResult("invalid complete_project outcome", err)
		}
		fields := make(map[string]json.RawMessage)
		if err := json.Unmarshal(raw, &fields); err != nil {
			return Outcome{}, invalidResult("invalid complete_project fields", err)
		}
		_, hasFrom := fields["from"]
		factJSON, hasFact := fields["fact"]
		value.Description = strings.TrimSpace(value.Description)
		if !validDescription(value.Description, 16*1024) {
			return Outcome{}, newError(ErrorInvalidResult, "completion description is required and bounded")
		}
		switch task {
		case TaskReason:
			if !hasFrom || hasFact {
				return Outcome{}, newError(ErrorInvalidResult, "reason complete_project requires from and forbids fact")
			}
			if err := validateSources(value.From); err != nil {
				return Outcome{}, err
			}
			sort.Strings(value.From)
		case TaskBootstrap:
			if hasFrom || !hasFact || value.Fact == nil || string(factJSON) == "null" {
				return Outcome{}, newError(ErrorInvalidResult, "bootstrap complete_project requires fact and forbids from")
			}
			value.Fact.Description = strings.TrimSpace(value.Fact.Description)
			if !validDescription(value.Fact.Description, 16*1024) {
				return Outcome{}, newError(ErrorInvalidResult, "bootstrap fact description is required and bounded")
			}
		default:
			return Outcome{}, taskOutcomeError(task, kind)
		}
		return Outcome{Kind: kind, Description: value.Description, From: value.From, Fact: value.Fact}, nil

	case OutcomeProposeIntents:
		if task != TaskReason {
			return Outcome{}, taskOutcomeError(task, kind)
		}
		var value struct {
			Kind    OutcomeKind      `json:"kind"`
			Intents []ProposedIntent `json:"intents"`
		}
		if err := decodeStrict(raw, &value); err != nil {
			return Outcome{}, invalidResult("invalid propose_intents outcome", err)
		}
		if maxProposedIntents <= 0 || len(value.Intents) == 0 || len(value.Intents) > maxProposedIntents {
			return Outcome{}, newError(ErrorInvalidResult, "proposed intent count is outside the Run limit")
		}
		for index := range value.Intents {
			value.Intents[index].Description = strings.TrimSpace(value.Intents[index].Description)
			if !validDescription(value.Intents[index].Description, 4*1024) {
				return Outcome{}, newError(ErrorInvalidResult, "proposed intent description is required and bounded")
			}
			if err := validateSources(value.Intents[index].From); err != nil {
				return Outcome{}, err
			}
			sort.Strings(value.Intents[index].From)
		}
		return Outcome{Kind: kind, Intents: value.Intents}, nil

	case OutcomeNoChange:
		if task != TaskReason {
			return Outcome{}, taskOutcomeError(task, kind)
		}
		var value struct {
			Kind OutcomeKind `json:"kind"`
		}
		if err := decodeStrict(raw, &value); err != nil {
			return Outcome{}, invalidResult("invalid no_change outcome", err)
		}
		return Outcome{Kind: kind}, nil

	case OutcomeConcludeIntent:
		if task != TaskExplore && task != TaskBootstrap {
			return Outcome{}, taskOutcomeError(task, kind)
		}
		var value struct {
			Kind OutcomeKind `json:"kind"`
			Fact FactResult  `json:"fact"`
		}
		if err := decodeStrict(raw, &value); err != nil {
			return Outcome{}, invalidResult("invalid conclude_intent outcome", err)
		}
		value.Fact.Description = strings.TrimSpace(value.Fact.Description)
		if !validDescription(value.Fact.Description, 16*1024) {
			return Outcome{}, newError(ErrorInvalidResult, "conclusion fact description is required and bounded")
		}
		return Outcome{Kind: kind, Fact: &value.Fact}, nil

	case OutcomeDecline:
		var value struct {
			Kind   OutcomeKind `json:"kind"`
			Reason string      `json:"reason"`
		}
		if err := decodeStrict(raw, &value); err != nil {
			return Outcome{}, invalidResult("invalid decline outcome", err)
		}
		value.Reason = strings.TrimSpace(value.Reason)
		if !validDescription(value.Reason, 2*1024) {
			return Outcome{}, newError(ErrorInvalidResult, "decline reason is required and bounded")
		}
		return Outcome{Kind: kind, Reason: value.Reason}, nil

	default:
		return Outcome{}, newError(ErrorInvalidResult, "unknown outcome kind")
	}
}

func decodeStrict(raw []byte, destination any) error {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("array is not closed")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func validateSources(sources []string) error {
	if len(sources) == 0 || len(sources) > 64 {
		return newError(ErrorInvalidResult, "source fact references are required and bounded")
	}
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if source == "" || len(source) > 256 || strings.TrimSpace(source) != source || source == "goal" {
			return newError(ErrorInvalidResult, "source fact reference is invalid")
		}
		if _, duplicate := seen[source]; duplicate {
			return newError(ErrorInvalidResult, "source fact references must be unique")
		}
		seen[source] = struct{}{}
	}
	return nil
}

func validDescription(value string, maximum int) bool {
	return value != "" && len(value) <= maximum
}

func invalidResult(detail string, cause error) *DomainError {
	return newError(ErrorInvalidResult, detail+": "+cause.Error())
}

func taskOutcomeError(task TaskType, kind OutcomeKind) *DomainError {
	return newError(ErrorInvalidResult, fmt.Sprintf("%s outcome is not allowed for %s", kind, task))
}
