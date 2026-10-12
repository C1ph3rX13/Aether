package run

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	BlackboardInputSchema       = "aether.blackboard/v1"
	BlackboardInputRoot         = "/run/aether/input"
	MaximumBlackboardInputBytes = 4 * 1024 * 1024
	maximumInputRecords         = 4096
	maximumInputTextBytes       = 64 * 1024
)

// BlackboardPath gives each Run phase an immutable path inside the shared
// Project Container. Hashing the Run ID keeps Docker paths safe without
// weakening the external Run ID contract.
func BlackboardPath(runID RunID, phase ExecutionPhase) string {
	digest := sha256.Sum256([]byte(runID))
	return fmt.Sprintf("%s/%x/%s/blackboard.yaml", BlackboardInputRoot, digest, phase)
}

func ValidBlackboardInputPath(value string) bool {
	if value == "" || path.Clean(value) != value || !strings.HasPrefix(value, BlackboardInputRoot+"/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(value, BlackboardInputRoot+"/"), "/")
	if len(parts) != 3 || len(parts[0]) != 64 || parts[2] != "blackboard.yaml" {
		return false
	}
	if _, err := hex.DecodeString(parts[0]); err != nil {
		return false
	}
	switch ExecutionPhase(parts[1]) {
	case PhaseReason, PhaseReasonFinalization, PhaseExplore, PhaseExploreConclude, PhaseExploreRecovery, PhaseExploreConcludeRecovery,
		PhaseBootstrapRecovery, PhaseBootstrapConcludeRecovery:
		return true
	default:
		return false
	}
}

type InputFact struct {
	ID                string `yaml:"id"`
	Description       string `yaml:"description"`
	Status            string `yaml:"status"`
	Supersedes        string `yaml:"supersedes,omitempty"`
	ReplacementFactID string `yaml:"replacement_fact_id,omitempty"`
}

type InputIntent struct {
	ID          string   `yaml:"id"`
	From        []string `yaml:"from"`
	To          string   `yaml:"to,omitempty"`
	Description string   `yaml:"description"`
	Creator     string   `yaml:"creator"`
	Status      string   `yaml:"status"`
}

type InputHint struct {
	ID        string `yaml:"id" json:"id"`
	Content   string `yaml:"content" json:"content"`
	Creator   string `yaml:"creator" json:"creator"`
	CreatedAt string `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	Status    string `yaml:"status" json:"status"`
}

type InputProject struct {
	Title            string `yaml:"title"`
	Origin           string `yaml:"origin"`
	Goal             string `yaml:"goal"`
	BootstrapEnabled bool   `yaml:"bootstrap_enabled"`
}

// BlackboardInput is the deterministic, one-Run view given to Reason/Explore.
// It carries business state but excludes persistence and execution-coordination fields.
type BlackboardInput struct {
	Schema         string        `yaml:"schema"`
	Generation     int           `yaml:"generation"`
	Revision       int64         `yaml:"revision"`
	TargetIntentID string        `yaml:"target_intent_id,omitempty"`
	Project        InputProject  `yaml:"project"`
	Facts          []InputFact   `yaml:"facts"`
	Intents        []InputIntent `yaml:"intents"`
	Hints          []InputHint   `yaml:"hints"`
}

// Normalize validates and sorts the input so equivalent projections serialize identically.
func (input BlackboardInput) Normalize() (BlackboardInput, error) {
	if input.Schema == "" {
		input.Schema = BlackboardInputSchema
	}
	if input.Schema != BlackboardInputSchema || input.Generation <= 0 || input.Revision < 0 {
		return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard input identity is invalid")
	}
	if !validInputText(input.Project.Title) || !validInputText(input.Project.Origin) || !validInputText(input.Project.Goal) {
		return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard Project context is invalid")
	}
	if len(input.Facts) > maximumInputRecords || len(input.Intents) > maximumInputRecords || len(input.Hints) > maximumInputRecords {
		return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard input exceeds its record limit")
	}
	input.Facts = append([]InputFact(nil), input.Facts...)
	input.Intents = append([]InputIntent(nil), input.Intents...)
	input.Hints = append([]InputHint(nil), input.Hints...)
	for index := range input.Facts {
		if input.Facts[index].Status == "" {
			input.Facts[index].Status = "active"
		}
		if !validInputRecord(input.Facts[index].ID, input.Facts[index].Description) {
			return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard fact input is invalid")
		}
		if input.Facts[index].Status != "active" && input.Facts[index].Status != "retracted" {
			return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard fact status is invalid")
		}
	}
	for index := range input.Intents {
		intent := &input.Intents[index]
		if intent.Status == "" {
			intent.Status = "open"
		}
		if !validInputRecord(intent.ID, intent.Description) || !validInputIdentity(intent.Creator) ||
			len(intent.From) > maximumInputRecords {
			return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard intent input is invalid")
		}
		intent.From = append([]string(nil), intent.From...)
		for _, source := range intent.From {
			if source == "" || len(source) > 256 {
				return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard intent source is invalid")
			}
		}
		sort.Strings(intent.From)
		if duplicateInputStrings(intent.From) ||
			(intent.Status != "open" && intent.Status != "concluded" && intent.Status != "cancelled") {
			return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard intent state is invalid")
		}
		if (intent.Status == "concluded" && intent.To == "") ||
			(intent.Status != "concluded" && intent.To != "") {
			return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard intent target is invalid")
		}
	}
	for index := range input.Hints {
		if input.Hints[index].Status == "" {
			input.Hints[index].Status = "active"
		}
		if !validInputRecord(input.Hints[index].ID, input.Hints[index].Content) || input.Hints[index].Creator == "" || len(input.Hints[index].Creator) > 256 || len(input.Hints[index].CreatedAt) > 128 {
			return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard hint input is invalid")
		}
		if input.Hints[index].Status != "active" && input.Hints[index].Status != "withdrawn" {
			return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard hint status is invalid")
		}
	}
	sort.Slice(input.Facts, func(i, j int) bool { return input.Facts[i].ID < input.Facts[j].ID })
	sort.Slice(input.Intents, func(i, j int) bool { return input.Intents[i].ID < input.Intents[j].ID })
	sort.Slice(input.Hints, func(i, j int) bool { return input.Hints[i].ID < input.Hints[j].ID })
	if duplicateInputIDs(input.Facts, func(value InputFact) string { return value.ID }) ||
		duplicateInputIDs(input.Intents, func(value InputIntent) string { return value.ID }) ||
		duplicateInputIDs(input.Hints, func(value InputHint) string { return value.ID }) {
		return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard input IDs must be unique by entity type")
	}
	if inputContentBytes(input) > MaximumBlackboardInputBytes {
		return BlackboardInput{}, newError(ErrorInvalidArgument, "blackboard input exceeds its size limit")
	}
	return input, nil
}

func (input BlackboardInput) MarshalYAML() ([]byte, RenderedInputHash, error) {
	normalized, err := input.Normalize()
	if err != nil {
		return nil, "", err
	}
	data, err := yaml.Marshal(normalized)
	if err != nil {
		return nil, "", fmt.Errorf("marshal blackboard input: %w", err)
	}
	if len(data) > MaximumBlackboardInputBytes {
		return nil, "", newError(ErrorInvalidArgument, "blackboard YAML exceeds its size limit")
	}
	return data, RenderedInputHashFor(data), nil
}

func inputContentBytes(input BlackboardInput) int {
	size := len(input.Schema) + len(input.TargetIntentID) + len(input.Project.Title) +
		len(input.Project.Origin) + len(input.Project.Goal)
	for _, fact := range input.Facts {
		size += len(fact.ID) + len(fact.Description) + len(fact.Status) + len(fact.Supersedes) + len(fact.ReplacementFactID)
	}
	for _, intent := range input.Intents {
		size += len(intent.ID) + len(intent.To) + len(intent.Description) + len(intent.Creator) + len(intent.Status)
		for _, source := range intent.From {
			size += len(source)
		}
	}
	for _, hint := range input.Hints {
		size += len(hint.ID) + len(hint.Content) + len(hint.Creator) + len(hint.CreatedAt) + len(hint.Status)
	}
	return size
}

func validInputRecord(id, text string) bool {
	return validInputIdentity(id) && text != "" && len(text) <= maximumInputTextBytes
}

func validInputIdentity(value string) bool { return value != "" && len(value) <= 256 }

func duplicateInputStrings(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] == values[index] {
			return true
		}
	}
	return false
}

func duplicateInputIDs[T any](values []T, id func(T) string) bool {
	for index := 1; index < len(values); index++ {
		if id(values[index-1]) == id(values[index]) {
			return true
		}
	}
	return false
}
