package worker

import (
	"encoding/json"
	"fmt"
)

// mockRule defines a conditional rule that can force a specific mock outcome.
type mockRule struct {
	FactIDsGTE       *int   `json:"fact_ids_gte,omitempty"`
	FactIDsLTE       *int   `json:"fact_ids_lte,omitempty"`
	OpenIntentsEmpty *bool  `json:"open_intents_empty,omitempty"`
	Force            string `json:"force"`
}

// mockPhaseConfig defines behavior for one phase of the mock driver.
type mockPhaseConfig struct {
	Delay    [2]float64         `json:"delay"`    // min, max seconds
	Outcomes map[string]float64 `json:"outcomes"` // outcome name → weight
	Rules    []mockRule         `json:"rules,omitempty"`
}

// mockBehavior holds all phase configs for a mock worker.
type mockBehavior struct {
	Bootstrap         mockPhaseConfig `json:"bootstrap"`
	BootstrapConclude mockPhaseConfig `json:"bootstrap_conclude"`
	Reason            mockPhaseConfig `json:"reason"`
	ExploreExecute    mockPhaseConfig `json:"explore_execute"`
	ExploreConclude   mockPhaseConfig `json:"explore_conclude"`
	Healthcheck       mockPhaseConfig `json:"healthcheck"`
}

type phaseEntry struct {
	key     string
	target  *mockPhaseConfig
	allowed map[string]bool
}

// defaultMockBehavior returns the default behavior when no MOCK_ env vars are set.
func defaultMockBehavior() mockBehavior {
	return mockBehavior{
		Bootstrap: mockPhaseConfig{
			Delay:    [2]float64{0, 0},
			Outcomes: map[string]float64{"fact": 0.7, "complete": 0.2, "rejected": 0.1},
		},
		BootstrapConclude: mockPhaseConfig{
			Delay:    [2]float64{0, 0},
			Outcomes: map[string]float64{"fact": 0.9, "rejected": 0.1},
		},
		Reason: mockPhaseConfig{
			Delay:    [2]float64{0, 0},
			Outcomes: map[string]float64{"intent": 0.5, "complete": 0.2, "noop": 0.1, "rejected": 0.1, "command_fail": 0.1},
		},
		ExploreExecute: mockPhaseConfig{
			Delay:    [2]float64{0, 0},
			Outcomes: map[string]float64{"fact": 0.8, "rejected": 0.1, "command_fail": 0.1},
		},
		ExploreConclude: mockPhaseConfig{
			Delay:    [2]float64{0, 0},
			Outcomes: map[string]float64{"fact": 0.9, "rejected": 0.1},
		},
		Healthcheck: mockPhaseConfig{
			Delay:    [2]float64{0, 0},
			Outcomes: map[string]float64{"ok": 1.0},
		},
	}
}

// parseMockBehavior parses MOCK_* env vars from the worker config into a mockBehavior.
// Missing keys use defaults. Invalid JSON causes an error.
func parseMockBehavior(env map[string]string) (mockBehavior, error) {
	behavior := defaultMockBehavior()

	phases := []phaseEntry{
		{"MOCK_BOOTSTRAP", &behavior.Bootstrap, map[string]bool{"fact": true, "complete": true, "rejected": true, "invalid_json": true, "invalid_payload": true, "command_fail": true}},
		{"MOCK_BOOTSTRAP_CONCLUDE", &behavior.BootstrapConclude, map[string]bool{"fact": true, "rejected": true, "invalid_json": true, "invalid_payload": true, "command_fail": true}},
		{"MOCK_REASON", &behavior.Reason, map[string]bool{"complete": true, "intent": true, "noop": true, "rejected": true, "invalid_json": true, "invalid_payload": true, "command_fail": true}},
		{"MOCK_EXPLORE_EXECUTE", &behavior.ExploreExecute, map[string]bool{"fact": true, "rejected": true, "invalid_json": true, "invalid_payload": true, "command_fail": true}},
		{"MOCK_EXPLORE_CONCLUDE", &behavior.ExploreConclude, map[string]bool{"fact": true, "rejected": true, "invalid_json": true, "invalid_payload": true, "command_fail": true}},
		{"MOCK_HEALTHCHECK", &behavior.Healthcheck, map[string]bool{"ok": true, "fail": true}},
	}

	for _, p := range phases {
		raw, ok := env[p.key]
		if !ok {
			continue
		}

		var cfg mockPhaseConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return behavior, fmt.Errorf("invalid JSON in %s: %w", p.key, err)
		}

		// Validate delay
		if cfg.Delay[0] < 0 || cfg.Delay[1] < 0 {
			return behavior, fmt.Errorf("%s.delay values must be non-negative", p.key)
		}
		if cfg.Delay[1] < cfg.Delay[0] {
			return behavior, fmt.Errorf("%s.delay[1] must be >= delay[0]", p.key)
		}

		// Validate outcomes
		if len(cfg.Outcomes) == 0 {
			return behavior, fmt.Errorf("%s.outcomes must not be empty", p.key)
		}
		var sum float64
		for name, weight := range cfg.Outcomes {
			if !p.allowed[name] {
				return behavior, fmt.Errorf("%s.outcomes has unsupported key %q", p.key, name)
			}
			if weight < 0 {
				return behavior, fmt.Errorf("%s.outcomes[%q] weight must be non-negative", p.key, name)
			}
			sum += weight
		}
		// Allow small floating point tolerance
		if sum < 0.99 || sum > 1.01 {
			return behavior, fmt.Errorf("%s.outcomes probabilities must sum to 1.0, got %.4f", p.key, sum)
		}

		// Validate rules
		for i, rule := range cfg.Rules {
			if rule.Force == "" {
				return behavior, fmt.Errorf("%s.rules[%d].force is required", p.key, i)
			}
			if !p.allowed[rule.Force] {
				return behavior, fmt.Errorf("%s.rules[%d].force has unsupported outcome %q", p.key, i, rule.Force)
			}
		}

		*p.target = cfg
	}

	return behavior, nil
}
