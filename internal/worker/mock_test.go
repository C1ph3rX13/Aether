package worker

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func makeMockWorker(env map[string]string) WorkerConfig {
	return WorkerConfig{
		Name:       "mock_test",
		Type:       WorkerTypeMock,
		TaskTypes:  []TaskType{"bootstrap", "reason", "explore"},
		MaxRunning: 1,
		Priority:   0,
		Env:        env,
	}
}

func TestParseMockBehaviorDefaults(t *testing.T) {
	behavior, err := parseMockBehavior(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if behavior.Bootstrap.Outcomes["fact"] != 0.7 {
		t.Errorf("default bootstrap fact weight: got %f, want 0.7", behavior.Bootstrap.Outcomes["fact"])
	}
	if behavior.Reason.Outcomes["intent"] != 0.5 {
		t.Errorf("default reason intent weight: got %f, want 0.5", behavior.Reason.Outcomes["intent"])
	}
}

func TestParseMockBehaviorValidJSON(t *testing.T) {
	env := map[string]string{
		"MOCK_BOOTSTRAP": `{"delay":[1,3],"outcomes":{"fact":0.6,"complete":0.3,"rejected":0.1}}`,
	}
	behavior, err := parseMockBehavior(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if behavior.Bootstrap.Delay[0] != 1 || behavior.Bootstrap.Delay[1] != 3 {
		t.Errorf("bootstrap delay: got %v, want [1,3]", behavior.Bootstrap.Delay)
	}
	if behavior.Bootstrap.Outcomes["fact"] != 0.6 {
		t.Errorf("bootstrap fact weight: got %f, want 0.6", behavior.Bootstrap.Outcomes["fact"])
	}
}

func TestParseMockBehaviorInvalidJSON(t *testing.T) {
	env := map[string]string{
		"MOCK_REASON": "not json",
	}
	_, err := parseMockBehavior(env)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseMockBehaviorInvalidOutcome(t *testing.T) {
	env := map[string]string{
		"MOCK_BOOTSTRAP": `{"outcomes":{"nonexistent_outcome":1.0}}`,
	}
	_, err := parseMockBehavior(env)
	if err == nil {
		t.Fatal("expected error for invalid outcome name")
	}
}

func TestParseMockBehaviorInvalidPayloadOutcome(t *testing.T) {
	env := map[string]string{
		"MOCK_REASON": `{"delay":[0,0],"outcomes":{"invalid_payload":0.5,"intent":0.5}}`,
	}
	behavior, err := parseMockBehavior(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if behavior.Reason.Outcomes["invalid_payload"] != 0.5 {
		t.Errorf("expected invalid_payload weight 0.5, got %f", behavior.Reason.Outcomes["invalid_payload"])
	}
}

func TestParseMockBehaviorRules(t *testing.T) {
	env := map[string]string{
		"MOCK_REASON": `{"delay":[0,0],"outcomes":{"intent":0.5,"complete":0.5},"rules":[{"fact_ids_gte":3,"force":"complete"},{"open_intents_empty":true,"force":"noop"}]}`,
	}
	behavior, err := parseMockBehavior(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(behavior.Reason.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(behavior.Reason.Rules))
	}
	if behavior.Reason.Rules[0].Force != "complete" {
		t.Errorf("rule[0] force: got %q, want 'complete'", behavior.Reason.Rules[0].Force)
	}
	if behavior.Reason.Rules[0].FactIDsGTE == nil || *behavior.Reason.Rules[0].FactIDsGTE != 3 {
		t.Error("rule[0] fact_ids_gte should be 3")
	}
}

func TestParseMockBehaviorRulesInvalidForce(t *testing.T) {
	env := map[string]string{
		"MOCK_REASON": `{"delay":[0,0],"outcomes":{"intent":1.0},"rules":[{"force":"nonexistent"}]}`,
	}
	_, err := parseMockBehavior(env)
	if err == nil {
		t.Fatal("expected error for invalid force outcome in rule")
	}
}

func TestParseMockBehaviorRulesMissingForce(t *testing.T) {
	env := map[string]string{
		"MOCK_REASON": `{"delay":[0,0],"outcomes":{"intent":1.0},"rules":[{"fact_ids_gte":3}]}`,
	}
	_, err := parseMockBehavior(env)
	if err == nil {
		t.Fatal("expected error for missing force in rule")
	}
}

func TestParseMockBehaviorProbabilitiesNotSummingToOne(t *testing.T) {
	env := map[string]string{
		"MOCK_REASON": `{"outcomes":{"intent":0.5,"complete":0.2}}`,
	}
	_, err := parseMockBehavior(env)
	if err == nil {
		t.Fatal("expected error for probabilities not summing to 1.0")
	}
}

func TestParseMockBehaviorNegativeDelay(t *testing.T) {
	env := map[string]string{
		"MOCK_BOOTSTRAP": `{"delay":[-1,2],"outcomes":{"fact":1.0}}`,
	}
	_, err := parseMockBehavior(env)
	if err == nil {
		t.Fatal("expected error for negative delay")
	}
}

func TestMockDriverSupportsConclude(t *testing.T) {
	definition, err := LookupAdapterDefinition("mock")
	if err != nil {
		t.Fatal(err)
	}
	d := definition.driver
	if !d.hasConclude() {
		t.Error("Mock driver should expose conclude capability")
	}
}

func TestMockDriverPrepareSession(t *testing.T) {
	d := NewMockDriver()
	session := d.PrepareSession()
	if session == nil || *session != "mock-session" {
		t.Error("MockDriver should return a mock session")
	}
}

func TestMockDriverBuildExecuteDefault(t *testing.T) {
	d := NewMockDriver()
	w := makeMockWorker(map[string]string{"MOCK_PHASE": "bootstrap"})
	result := d.BuildExecute(w, "test prompt", nil)

	if len(result.Argv) < 2 || result.Argv[0] != "python3" {
		t.Errorf("expected python3 command, got %v", result.Argv)
	}
	// Script should be the mock script
	script := result.Argv[2]
	if script == "" {
		t.Error("expected non-empty script")
	}
}

func TestMockDriverBuildConclude(t *testing.T) {
	d := NewMockDriver()
	tests := map[string]string{
		"bootstrap":          "bootstrap_conclude",
		"bootstrap_conclude": "bootstrap_conclude",
		"explore_execute":    "explore_conclude",
		"explore_conclude":   "explore_conclude",
	}
	for phase, want := range tests {
		t.Run(phase, func(t *testing.T) {
			w := makeMockWorker(map[string]string{"MOCK_PHASE": phase})
			argv := d.BuildConclude(w, "conclude prompt", "mock-session")
			if len(argv) < 5 || argv[0] != "python3" {
				t.Fatalf("expected python3 command, got %v", argv)
			}
			var prompt map[string]any
			if err := json.Unmarshal([]byte(argv[4]), &prompt); err != nil {
				t.Fatalf("decode prompt context: %v", err)
			}
			if prompt["phase"] != want {
				t.Fatalf("conclude phase = %q, want %q", prompt["phase"], want)
			}
		})
	}
}

func TestMockDriverWithCustomBehavior(t *testing.T) {
	d := NewMockDriver()
	env := map[string]string{
		"MOCK_PHASE":  "reason",
		"MOCK_REASON": `{"delay":[0,0],"outcomes":{"intent":1.0}}`,
	}
	w := makeMockWorker(env)
	result := d.BuildExecute(w, "test prompt", nil)

	if len(result.Argv) < 2 || result.Argv[0] != "python3" {
		t.Errorf("expected python3 command, got %v", result.Argv)
	}
	// The script should produce intent output for reason phase
	// (Testing the generated script structure, not execution)
	script := result.Argv[2]
	if script == "" {
		t.Error("expected non-empty script")
	}
}

func TestMockReasonNoopRemainsLegalWithoutOpenIntents(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required to execute the mock worker")
	}
	driver := NewMockDriver()
	worker := makeMockWorker(map[string]string{
		"MOCK_PHASE":             "reason",
		"AETHER_RESULT_PROTOCOL": OutcomeSubmissionSchemaV1,
		"MOCK_REASON":            `{"delay":[0,0],"outcomes":{"noop":1.0}}`,
	})
	result := driver.BuildExecute(worker, `{"phase":"reason","fact_ids":["origin"],"open_intents":[],"max_intents":3}`, nil)
	command := exec.Command(result.Argv[0], result.Argv[1:]...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute deterministic Reason mock: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `"kind": "no_change"`) {
		t.Fatalf("Reason mock output=%s, want no_change result", output)
	}
}
