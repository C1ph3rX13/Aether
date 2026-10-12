package run

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

const (
	BootstrapInputSchema       = "aether.bootstrap/v1"
	MaximumBootstrapInputBytes = 64 * 1024
)

// BootstrapInput is the small, deterministic inline context for the initial fast path.
type BootstrapInput struct {
	Schema         string      `yaml:"schema"`
	Generation     int         `yaml:"generation"`
	Revision       int64       `yaml:"revision"`
	TargetIntentID string      `yaml:"target_intent_id"`
	Origin         string      `yaml:"origin"`
	Goal           string      `yaml:"goal"`
	Hints          []InputHint `yaml:"hints"`
}

func (input BootstrapInput) Normalize() (BootstrapInput, error) {
	if input.Schema == "" {
		input.Schema = BootstrapInputSchema
	}
	if input.Schema != BootstrapInputSchema || input.Generation <= 0 || input.Revision < 0 ||
		!validInputIdentity(input.TargetIntentID) || !validInputText(input.Origin) || !validInputText(input.Goal) {
		return BootstrapInput{}, newError(ErrorInvalidArgument, "Bootstrap input identity or facts are invalid")
	}
	normalized, err := (BlackboardInput{
		Schema: BlackboardInputSchema, Generation: input.Generation, Revision: input.Revision, Hints: input.Hints,
		Project: InputProject{Title: "Bootstrap", Origin: input.Origin, Goal: input.Goal},
	}).Normalize()
	if err != nil {
		return BootstrapInput{}, err
	}
	input.Hints = normalized.Hints
	if len(input.Origin)+len(input.Goal)+inputContentBytes(BlackboardInput{Hints: input.Hints}) > MaximumBootstrapInputBytes {
		return BootstrapInput{}, newError(ErrorInvalidArgument, "Bootstrap input exceeds its size limit")
	}
	return input, nil
}

func (input BootstrapInput) MarshalYAML() ([]byte, RenderedInputHash, error) {
	normalized, err := input.Normalize()
	if err != nil {
		return nil, "", err
	}
	data, err := yaml.Marshal(normalized)
	if err != nil {
		return nil, "", fmt.Errorf("marshal Bootstrap input: %w", err)
	}
	if len(data) > MaximumBootstrapInputBytes {
		return nil, "", newError(ErrorInvalidArgument, "Bootstrap YAML exceeds its size limit")
	}
	return data, RenderedInputHashFor(data), nil
}

func validInputText(value string) bool {
	return value != "" && len(value) <= maximumInputTextBytes
}
